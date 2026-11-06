package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

var ticket20Build struct {
	sync.Once
	bin string
	err error
	out []byte
}

func ticket20Peer(t *testing.T) string {
	t.Helper()
	ticket20Build.Do(func() {
		ticket20Build.bin = filepath.Join(root, "ticket20-recovery-peer")
		ticket20Build.out, ticket20Build.err = exec.CommandContext(ctx(t), "go", "build", "-o", ticket20Build.bin, "./testdata/ticket14-recovery-peer").CombinedOutput()
	})
	require.NoError(t, ticket20Build.err, "%s", ticket20Build.out)
	return ticket20Build.bin
}

// The public runtime stream proves terminal observation before the authored
// peer releases its response. No scheduling delay stands in for that ordering.
type ticket20TerminalFactory struct {
	runtime.Factory
	release string
	seen    chan events.SessionEvent
}

func (f ticket20TerminalFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	d, err := f.Factory.New(name, config)
	if err != nil {
		return nil, err
	}
	return ticket20TerminalDriver{Driver: d, CreationDriver: d.(runtime.CreationDriver), ResumptionDriver: d.(runtime.ResumptionDriver), release: f.release, seen: f.seen}, nil
}

type ticket20TerminalDriver struct {
	runtime.Driver
	runtime.CreationDriver
	runtime.ResumptionDriver
	release string
	seen    chan events.SessionEvent
}

func (d ticket20TerminalDriver) ValidateInitialInput(input runtime.Input) error {
	return d.ResumptionDriver.ValidateInitialInput(input)
}

func (d ticket20TerminalDriver) Events() (events.Stream, error) {
	stream, err := d.Driver.Events()
	return ticket20TerminalStream{Stream: stream, release: d.release, seen: d.seen}, err
}

type ticket20TerminalStream struct {
	events.Stream
	release string
	seen    chan events.SessionEvent
}

func (s ticket20TerminalStream) Next(ctx context.Context) (events.SessionEvent, error) {
	e, err := s.Stream.Next(ctx)
	state, ok := e.Payload().(events.StateUpdate)
	terminal := ok && state.State != nil && *state.State == events.Idle
	if err == nil && e.Source().Work() == "turn-2" && (terminal || e.Type() == "agent.message") {
		s.seen <- e
		if terminal {
			err = os.WriteFile(s.release, nil, 0o600)
		}
	}
	return e, err
}

func TestTicket20ExplicitResumeHTTP(t *testing.T) {
	bin := ticket20Peer(t)
	for _, scenario := range []struct{ kind, mode string }{{"claude", "early-terminal"}, {"codex", "early-terminal"}, {"codex", "terminal-before-receipt"}} {
		t.Run(scenario.kind+"/"+scenario.mode, func(t *testing.T) {
			kind := scenario.kind
			work, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			native := t.TempDir()
			st, err := file.Open(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, st.Close()) })
			vault := t.TempDir()
			factories := map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: vault}, "codex": codex.Factory{RecoveryDir: vault}}
			seen := make(chan events.SessionEvent, 2)
			if scenario.mode == "terminal-before-receipt" {
				release := filepath.Join(native, "release-receipt")
				factories[kind] = ticket20TerminalFactory{Factory: factories[kind], release: release, seen: seen}
				// Release a gated peer even when a public assertion fails.
				defer func() { assert.NoError(t, os.WriteFile(release, nil, 0o600)) }()
			}
			svc := service.New(st, st, runtime.NewRegistry(factories))
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, svc.Shutdown(cleanup))
			})
			env := map[string]any{"PEER_KIND": kind, "PEER_DIR": native, "WORK_DIR": work, "RESUME_INPUT_MODE": scenario.mode, "SAVED_TRANSCRIPT": filepath.Join(native, "transcript.jsonl")}
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": env}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			first := runtime.Input{Content: []events.ContentBlock{events.Text("first create input")}}
			created, err := svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "original"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &first})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: created.Status.SessionID}
			wait := func(n int) store.HistoryPage {
				var page store.HistoryPage
				require.Eventually(t, func() bool {
					page, err = svc.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
					if err != nil || len(page.Events) != n {
						return false
					}
					r, e := svc.GetSession(ref)
					return e == nil && r.Status.State == events.Idle
				}, 5*time.Second, 10*time.Millisecond)
				return page
			}
			before := wait(1)
			_, err = svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
			h, err := transport.New(svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			t.Cleanup(h.Shutdown)
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			c := client.New(server.URL, "")
			t.Cleanup(func() { assert.NoError(t, c.Close()) })
			observer, err := c.Observe(ctx(t), ref)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, observer.Close()) })
			originalWire, err := os.ReadFile(filepath.Join(native, "wire.jsonl"))
			require.NoError(t, err)
			for _, invalid := range []string{`{"initialInput":{"content":[]}}`, `{"initialInput":null}`, `{"initialInput":{"content":[{"type":"image","data":"aA==","mimeType":"image/png"}]}}`, `{"initialInput":{"content":[{"type":"text","text":"not sent","annotations":{"audience":["user"]}}]}}`} {
				req, e := http.NewRequestWithContext(ctx(t), http.MethodPost, server.URL+"/apis/v1/sessions/"+ref.SessionID+"/resume", strings.NewReader(invalid))
				require.NoError(t, e)
				req.Header.Set("Content-Type", "application/json")
				response, e := http.DefaultClient.Do(req)
				require.NoError(t, e)
				_, e = io.Copy(io.Discard, response.Body)
				assert.NoError(t, e)
				assert.NoError(t, response.Body.Close())
				require.Contains(t, []int{400, 422}, response.StatusCode)
				wire, e := os.ReadFile(filepath.Join(native, "wire.jsonl"))
				require.NoError(t, e)
				require.Equal(t, originalWire, wire, "invalid/unsupported input must not restore a native connection")
			}
			body := `{"initialInput":{"content":[{"type":"text","text":"new resume input"}]}}`
			req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, server.URL+"/apis/v1/sessions/"+ref.SessionID+"/resume", bytes.NewBufferString(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, response.Body.Close()) })
			raw, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode, "%s", raw)
			var got struct {
				resource.Session
				InitialInputReceipt *runtime.InputReceipt `json:"initialInputReceipt"`
			}
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, ref.SessionID, got.Status.SessionID)
			require.NotNil(t, got.InitialInputReceipt)
			require.NotEmpty(t, got.InitialInputReceipt.Submission)
			require.Equal(t, runtime.Yes(), got.InitialInputReceipt.Acceptance.Sent)
			if kind == "claude" {
				require.Nil(t, got.InitialInputReceipt.Acceptance.NativeAccepted)
			} else {
				require.Equal(t, runtime.Yes(), got.InitialInputReceipt.Acceptance.NativeAccepted)
			}
			var runtimeMessage events.SessionEvent
			if scenario.mode == "terminal-before-receipt" {
				require.FileExists(t, filepath.Join(native, "receipt-gated"), "peer must hold the resume receipt until the public stream observes completion")
				require.Equal(t, "turn-2", got.InitialInputReceipt.Submission)
				for _, typ := range []string{"agent.message", "session.state_update"} {
					select {
					case observed := <-seen:
						require.Equal(t, typ, observed.Type())
						require.Equal(t, got.InitialInputReceipt.Submission, observed.Source().Work())
						require.Equal(t, ref.SessionID, observed.SessionID)
						if typ == "agent.message" {
							runtimeMessage = observed
						}
					default:
						t.Fatal("resume receipt returned without observing answer and terminal on the public runtime stream")
					}
				}
			}
			after := wait(2)
			if scenario.mode == "terminal-before-receipt" {
				require.Equal(t, runtimeMessage.ItemID, after.Events[1].ItemID)
				require.Equal(t, runtimeMessage.Payload(), after.Events[1].Payload())
			}
			require.Equal(t, before.Events[0], after.Events[0])
			require.Equal(t, "new resume input", after.Events[1].Payload().(events.AgentMessage).Content[0].Text)
			for {
				streamed, e := observer.Next()
				require.NoError(t, e)
				if streamed.Type() != "agent.message" {
					continue
				}
				persisted, e := json.Marshal(after.Events[1])
				require.NoError(t, e)
				wire, e := json.Marshal(streamed)
				require.NoError(t, e)
				require.JSONEq(t, string(persisted), string(wire))
				require.Equal(t, events.Source{}, streamed.Source())
				if kind == "claude" {
					require.Equal(t, got.InitialInputReceipt.Submission, streamed.Meta["claude"].(map[string]any)["submission"])
				}
				break
			}
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continued input")}})
			require.NoError(t, err, "early terminal must not be reset when installing receipt")
			wait(3)
			_, err = svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
			wire, err := os.ReadFile(filepath.Join(native, "wire.jsonl"))
			require.NoError(t, err)
			var texts []string
			for line := range strings.SplitSeq(strings.TrimSpace(string(wire)), "\n") {
				var call ticket12Call
				require.NoError(t, json.Unmarshal([]byte(line), &call))
				if call.Method == "input" {
					texts = append(texts, call.Params["text"].(string))
				}
			}
			require.Equal(t, []string{"first create input", "new resume input", "continued input"}, texts)
		})
	}
}

type ticket20Fixture struct {
	svc             *service.Service
	data, native    string
	work, vault     string
	runtimeResource resource.Runtime
	ref             resource.SessionRef
	close           func()
	wrapFactory     func(runtime.Factory) runtime.Factory
	wrapMetadata    func(metastore.MetaStore) metastore.MetaStore
}

func ticket20Open(t *testing.T, f *ticket20Fixture, create bool) {
	t.Helper()
	st, err := file.Open(f.data, false)
	require.NoError(t, err)
	closeStore := sync.OnceFunc(func() { assert.NoError(t, st.Close()) })
	t.Cleanup(closeStore)
	factories := map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: f.vault}, "codex": codex.Factory{RecoveryDir: f.vault}}
	if f.wrapFactory != nil {
		for kind, factory := range factories {
			factories[kind] = f.wrapFactory(factory)
		}
	}
	var meta metastore.MetaStore = st
	if f.wrapMetadata != nil {
		meta = f.wrapMetadata(meta)
	}
	f.svc = service.New(meta, st, runtime.NewRegistry(factories))
	svc := f.svc
	closeService := sync.OnceFunc(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, svc.Shutdown(cleanup))
	})
	t.Cleanup(closeService)
	f.close = func() { closeService(); closeStore() }
	if create {
		_, err = svc.CreateRuntime(f.runtimeResource)
		require.NoError(t, err)
	}
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
}

func ticket20Setup(t *testing.T, kind, mode string) *ticket20Fixture {
	t.Helper()
	f := &ticket20Fixture{data: t.TempDir(), native: t.TempDir(), vault: t.TempDir()}
	var err error
	f.work, err = filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	config := t.TempDir()
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(f.work, "-")
	project := filepath.Join(config, "projects", key)
	require.NoError(t, os.MkdirAll(project, 0o700))
	const id = "14141414-1414-4141-8141-141414141414"
	f.ref = resource.SessionRef{SessionID: id}
	env := map[string]any{"PEER_KIND": kind, "PEER_DIR": f.native, "WORK_DIR": f.work, "RESUME_INPUT_MODE": mode, "CLAUDE_CONFIG_DIR": config, "SAVED_TRANSCRIPT": filepath.Join(project, id+".jsonl")}
	f.runtimeResource = resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": ticket20Peer(t), "envPolicy": "replace", "env": env}}}
	ticket20Open(t, f, true)
	first := runtime.Input{Content: []events.ContentBlock{events.Text("original input")}}
	created, err := f.svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "original"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: f.work}}, InitialInput: &first})
	require.NoError(t, err)
	require.Equal(t, id, created.Status.SessionID)
	ticket20Wait(t, f, 1)
	_, err = f.svc.CloseSession(ctx(t), f.ref)
	require.NoError(t, err)
	return f
}

func ticket20Wait(t *testing.T, f *ticket20Fixture, n int) store.HistoryPage {
	t.Helper()
	var page store.HistoryPage
	var state resource.Session
	var err error
	require.Eventually(t, func() bool {
		page, err = f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{Types: []string{"agent.message"}})
		if err != nil || len(page.Events) != n {
			return false
		}
		state, err = f.svc.GetSession(f.ref)
		return err == nil && state.Status.State == events.Idle
	}, 5*time.Second, 10*time.Millisecond, "expected %d durable answers and idle", n)
	return page
}

func ticket20Wire(t *testing.T, f *ticket20Fixture) []ticket12Call {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.native, "wire.jsonl"))
	require.NoError(t, err)
	var calls []ticket12Call
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var call ticket12Call
		require.NoError(t, json.Unmarshal([]byte(line), &call))
		calls = append(calls, call)
	}
	return calls
}

func ticket20Released(t *testing.T, f *ticket20Fixture) {
	t.Helper()
	opened, released := map[int]bool{}, map[int]bool{}
	for _, call := range ticket20Wire(t, f) {
		if call.Method == "opened" {
			opened[call.PID] = true
		}
		if call.Method == "released" {
			released[call.PID] = true
		}
	}
	require.Equal(t, opened, released)
	for pid := range opened {
		require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "owned peer %d remains", pid)
	}
}

func TestTicket20SubmittedMismatchIsUnknownNotUnsent(t *testing.T) {
	for _, mode := range []string{"wrong-id", "wrong-cwd"} {
		t.Run(mode, func(t *testing.T) {
			f := ticket20Setup(t, "claude", mode)
			before, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
			require.NoError(t, err)
			subscription, err := f.svc.SubscribeEvents(f.ref)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, subscription.Close()) })
			got, err := f.svc.ResumeSessionWithInput(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("already sent")}})
			require.Error(t, err)
			var op *runtime.OperationError
			require.ErrorAs(t, err, &op)
			require.Equal(t, "outcome_unknown", op.Code)
			require.Equal(t, "resume_input_unverified", op.Reason)
			require.Nil(t, got.InitialInputReceipt)
			state, err := f.svc.GetSession(f.ref)
			require.NoError(t, err)
			require.Equal(t, f.ref.SessionID, state.Status.SessionID)
			require.Equal(t, events.SessionState("offline"), state.Status.State)
			after, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.Equal(t, before, after, "pre-identity terminal facts must not escape a mismatch")
			select {
			case event := <-subscription.Events():
				t.Fatalf("unverified event published: %s", event.Type())
			default:
			}
			var texts []string
			for _, call := range ticket20Wire(t, f) {
				if call.Method == "input" {
					texts = append(texts, call.Params["text"].(string))
				}
			}
			require.Equal(t, []string{"original input", "already sent"}, texts, "native write happened; release cannot imply NotSent")
			f.close()
			ticket20Released(t, f)
		})
	}
}

func TestTicket20StructuredCLIAndDurableRecoveryNeverReplay(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			f := ticket20Setup(t, kind, "early-terminal")
			h, err := transport.New(f.svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			t.Cleanup(h.Shutdown)
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			path := filepath.Join(t.TempDir(), "new-input.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"content":[{"type":"text","text":"new "},{"type":"text","text":"explicit input"}]}`), 0o600))
			cmd := exec.CommandContext(ctx(t), ctl, "--url", server.URL, "-o", "json", "session", "resume", "original", "-n", "default", "--input-file", path)
			raw, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", raw)
			var resumed resource.SessionResumed
			require.NoError(t, json.Unmarshal(raw, &resumed))
			require.Equal(t, f.ref.SessionID, resumed.Status.SessionID)
			require.NotNil(t, resumed.InitialInputReceipt)
			before := ticket20Wait(t, f, 2)
			require.Equal(t, "new explicit input", before.Events[1].Payload().(events.AgentMessage).Content[0].Text)
			for _, call := range ticket20Wire(t, f) {
				if call.Method == "input" && call.Params["text"] == "new explicit input" {
					blocks := call.Params["content"].([]any)
					require.Len(t, blocks, 2, "structured input must not be flattened")
					require.Equal(t, "new ", blocks[0].(map[string]any)["text"])
					require.Equal(t, "explicit input", blocks[1].(map[string]any)["text"])
				}
			}
			_, err = f.svc.CloseSession(ctx(t), f.ref)
			require.NoError(t, err)
			f.close()
			ticket20Open(t, f, false)
			durable, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{Types: []string{"agent.message"}})
			require.NoError(t, err)
			require.Equal(t, before, durable)
			h2, err := transport.New(f.svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			t.Cleanup(h2.Shutdown)
			server2 := httptest.NewServer(h2)
			t.Cleanup(server2.Close)
			// Legacy empty-body clients still resume without sending business input.
			req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, server2.URL+"/apis/v1/sessions/"+f.ref.SessionID+"/resume", http.NoBody)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, readErr := io.ReadAll(resp.Body)
			assert.NoError(t, resp.Body.Close())
			require.NoError(t, readErr)
			require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
			require.NotContains(t, string(body), "initialInputReceipt")
			_, err = f.svc.SendPrompt(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("after restart")}})
			require.NoError(t, err)
			ticket20Wait(t, f, 3)
			_, err = f.svc.CloseSession(ctx(t), f.ref)
			require.NoError(t, err)
			// The generated client carries explicit input by ID as well as CLI's name route.
			c := client.New(server2.URL, "")
			t.Cleanup(func() { assert.NoError(t, c.Close()) })
			var result resource.SessionResumed
			err = c.ResumeSessionWithInput(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("second opt-in")}}, &result)
			require.NoError(t, err)
			require.NotNil(t, result.InitialInputReceipt)
			ticket20Wait(t, f, 4)
			_, err = f.svc.CloseSession(ctx(t), f.ref)
			require.NoError(t, err)
			f.close()

			// Import actual native saved history into a new durable MASS authority.
			f.data = t.TempDir()
			f.vault = t.TempDir()
			ticket20Open(t, f, true)
			_, err = f.svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "default", Name: "imported"}, RuntimeRef: "agent", Cwd: f.work, SessionID: f.ref.SessionID})
			require.NoError(t, err)
			var state resource.Session
			require.Eventually(t, func() bool {
				state, err = f.svc.GetSession(f.ref)
				return err == nil && (state.Status.HistoryLoad.Phase == "completed" || state.Status.HistoryLoad.Phase == "failed")
			}, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, "completed", state.Status.HistoryLoad.Phase, "%+v", state.Status.HistoryLoad)
			require.True(t, state.Status.HistoryLoad.DeliveryCompleted)
			page, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 8)
			for i, text := range []string{"original input", "new explicit input", "after restart", "second opt-in"} {
				require.Equal(t, text, page.Events[2*i].Payload().(events.UserMessage).Content[0].Text)
				require.Equal(t, text, page.Events[2*i+1].Payload().(events.AgentMessage).Content[0].Text)
			}
			_, err = f.svc.SendPrompt(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("after native load")}})
			require.NoError(t, err)
			ticket20Wait(t, f, 5)
			f.close()
			var texts []string
			for _, call := range ticket20Wire(t, f) {
				if call.Method == "input" {
					require.Equal(t, f.ref.SessionID, call.Params["sessionId"])
					texts = append(texts, call.Params["text"].(string))
				}
			}
			require.Equal(t, []string{"original input", "new explicit input", "after restart", "second opt-in", "after native load"}, texts)
			ticket20Released(t, f)
		})
	}
}

func TestTicket20PendingInputCallerAbandonRetainsStopOwnership(t *testing.T) {
	for _, stop := range []string{"Stop", "Shutdown"} {
		t.Run(stop, func(t *testing.T) {
			f := ticket20Setup(t, "claude", "no-init")
			before, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
			require.NoError(t, err)
			caller, cancel := context.WithCancel(ctx(t))
			t.Cleanup(cancel)
			result := make(chan error, 1)
			go func() {
				_, e := f.svc.ResumeSessionWithInput(caller, f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("new pending input")}})
				result <- e
			}()
			require.Eventually(t, func() bool {
				for _, call := range ticket20Wire(t, f) {
					if call.Method == "input" && call.Params["text"] == "new pending input" {
						return true
					}
				}
				return false
			}, 5*time.Second, 10*time.Millisecond)
			cancel()
			select {
			case err = <-result:
				var op *runtime.OperationError
				require.ErrorAs(t, err, &op)
				require.Equal(t, "lifecycle_wait_abandoned", op.Reason)
			case <-ctx(t).Done():
				t.Fatal("caller failed to abandon wait")
			}
			_, err = f.svc.SendPrompt(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not send")}})
			require.Error(t, err)
			if stop == "Stop" {
				_, err = f.svc.StopRuntime(ctx(t), "agent")
			} else {
				err = f.svc.Shutdown(ctx(t))
			}
			require.NoError(t, err)
			state, err := f.svc.GetSession(f.ref)
			require.NoError(t, err)
			require.Equal(t, f.ref.SessionID, state.Status.SessionID)
			require.Equal(t, events.SessionState("offline"), state.Status.State)
			after, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.Equal(t, before, after)
			f.close()
			ticket20Released(t, f)
		})
	}
}

// The optional public runtime capability may be absent even when ordinary
// restoration works. Embedding only Driver deliberately does not grant it.
type (
	ticket20OrdinaryDriver  struct{ runtime.Driver }
	ticket20OrdinaryFactory struct{ runtime.Factory }
)

func (f ticket20OrdinaryFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	driver, err := f.Factory.New(name, config)
	return ticket20OrdinaryDriver{driver}, err
}

func TestTicket20UnsupportedCapabilityHasNoNativeSideEffects(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			f := ticket20Setup(t, kind, "")
			f.close()
			f.wrapFactory = func(factory runtime.Factory) runtime.Factory { return ticket20OrdinaryFactory{factory} }
			ticket20Open(t, f, false)
			before, err := f.svc.GetSession(f.ref)
			require.NoError(t, err)
			wire, err := os.ReadFile(filepath.Join(f.native, "wire.jsonl"))
			require.NoError(t, err)
			_, err = f.svc.ResumeSessionWithInput(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}})
			var op *runtime.OperationError
			require.ErrorAs(t, err, &op)
			require.Equal(t, "unsupported", op.Code)
			after, err := f.svc.GetSession(f.ref)
			require.NoError(t, err)
			require.Equal(t, before, after)
			actual, err := os.ReadFile(filepath.Join(f.native, "wire.jsonl"))
			require.NoError(t, err)
			require.Equal(t, wire, actual)
			_, err = f.svc.ResumeSession(ctx(t), f.ref)
			require.NoError(t, err, "absence of opt-in capability must not prohibit ordinary restore")
			f.close()
			ticket20Released(t, f)
		})
	}
}

type ticket20ResumeCommit struct {
	metastore.MetaStore
	once             sync.Once
	entered, release chan struct{}
	fail             bool
}

func (m *ticket20ResumeCommit) UpdateSession(session resource.Session) (resource.Session, error) {
	var err error
	if session.Status.AppliedGeneration != 0 {
		m.once.Do(func() {
			close(m.entered)
			<-m.release
			if m.fail {
				err = errors.New("injected resume identity commit failure")
			}
		})
	}
	if err != nil {
		return session, err
	}
	return m.MetaStore.UpdateSession(session)
}

func TestTicket20ResumeCommitGatesFactsAndReceipt(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		for _, fail := range []bool{false, true} {
			name := kind + "/committed"
			if fail {
				name = kind + "/failed"
			}
			t.Run(name, func(t *testing.T) {
				f := ticket20Setup(t, kind, "early-terminal")
				f.close()
				gate := &ticket20ResumeCommit{entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
				release := sync.OnceFunc(func() { close(gate.release) })
				f.wrapMetadata = func(meta metastore.MetaStore) metastore.MetaStore { gate.MetaStore = meta; return gate }
				ticket20Open(t, f, false)
				// Release before Service/store cleanup even when an assertion fails.
				t.Cleanup(release)
				before, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
				require.NoError(t, err)
				sub, err := f.svc.SubscribeEvents(f.ref)
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, sub.Close()) })
				type outcome struct {
					value resource.SessionResumed
					err   error
				}
				done := make(chan outcome, 1)
				go func() {
					value, e := f.svc.ResumeSessionWithInput(ctx(t), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("before commit")}})
					done <- outcome{value, e}
				}()
				select {
				case <-gate.entered:
				case <-ctx(t).Done():
					t.Fatal("resume never reached binding commit")
				}
				page, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
				require.NoError(t, err)
				require.Equal(t, before, page)
				select {
				case <-done:
					t.Fatal("receipt escaped before commit")
				default:
				}
				select {
				case <-sub.Events():
					t.Fatal("fact escaped before commit")
				default:
				}
				release()
				var got outcome
				select {
				case got = <-done:
				case <-ctx(t).Done():
					t.Fatal("resume did not settle")
				}
				if fail {
					t.Cleanup(func() {
						cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						// An assertion failure must still settle the owned unpublished
						// facts before Service and authority cleanup run.
						_ = f.svc.ReconcileOpening(cleanup, "agent", runtime.Address{Namespace: "default", SessionName: "original"})
					})
					require.ErrorContains(t, got.err, "injected resume identity commit failure")
					require.Nil(t, got.value.InitialInputReceipt)
					var op *runtime.OperationError
					require.ErrorAs(t, got.err, &op)
					require.Equal(t, "outcome_unknown", op.Code)
					require.NoError(t, f.svc.ReconcileOpening(ctx(t), "agent", runtime.Address{Namespace: "default", SessionName: "original"}))
				} else {
					require.NoError(t, got.err)
					require.NotNil(t, got.value.InitialInputReceipt)
				}
				// Already accepted facts remain owned on commit failure; reconciliation
				// drains the same facts without dispatching the input again.
				require.Eventually(t, func() bool {
					page, err = f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{Types: []string{"agent.message"}})
					return err == nil && len(page.Events) == 2
				}, 5*time.Second, 10*time.Millisecond)
				require.Equal(t, "before commit", page.Events[1].Payload().(events.AgentMessage).Content[0].Text)
				f.close()
				ticket20Released(t, f)
			})
		}
	}
}
