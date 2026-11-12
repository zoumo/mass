package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

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

func TestTicket14ExplicitClaudeCreateHTTP(t *testing.T) {
	svc, _, _ := setup(t, nil)
	path, work := nativeScenario(t, "claude", "explicit-create")
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "explicit"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "explicit")
	require.NoError(t, err)
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	c := newHTTPClient(t, server.URL, "")
	input := runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}
	var got resource.SessionCreated
	require.NoError(t, c.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{
		Metadata:     resource.Metadata{Namespace: "default", Name: "first"},
		Spec:         resource.SessionSpec{RuntimeRef: "explicit", SessionSpec: runtime.SessionSpec{Cwd: work}},
		InitialInput: &input,
	}, &got), "explicit input must release the native init wait")
	_, err = uuid.Parse(got.Status.SessionID)
	require.NoError(t, err)
	require.NotNil(t, got.InitialInputReceipt)
	require.NotEmpty(t, got.InitialInputReceipt.Submission)
	require.Equal(t, runtime.Yes(), got.InitialInputReceipt.Acceptance.Sent)
	ref := resource.SessionRef{SessionID: got.Status.SessionID}
	require.Eventually(t, func() bool {
		page, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
		if e != nil {
			return false
		}
		for _, event := range page.Events {
			if event.Type() == "agent.message" {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	persisted, err := svc.GetSession(ref)
	require.NoError(t, err)
	raw, err := json.Marshal(persisted)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "initialInput")
	require.NotContains(t, string(raw), "hello")
}

func TestTicket14ExplicitCreateCLI(t *testing.T) {
	svc, _, _ := setup(t, nil)
	path, work := nativeScenario(t, "claude", "explicit-create")
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	cmd := exec.CommandContext(ctx(t), ctl, "--url", server.URL, "-o", "json", "session", "create", "first", "-n", "default", "--runtime", "agent", "--cwd", work, "--text", "hello")
	raw, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", raw)
	var result resource.SessionCreated
	require.NoError(t, json.Unmarshal(raw, &result))
	_, err = uuid.Parse(result.Status.SessionID)
	require.NoError(t, err)
	require.NotNil(t, result.InitialInputReceipt)
	require.Equal(t, runtime.Yes(), result.InitialInputReceipt.Acceptance.Sent)
	require.Nil(t, result.InitialInputReceipt.Acceptance.NativeAccepted)
}

// Strict peers consume precisely the supplied block array; rejected creates must
// not advance their native protocol or reserve the target name.
func TestTicket14StructuredCreateAndValidation(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, _ := setup(t, nil)
			mode := "explicit-create"
			bin := mock
			if kind == "claude" {
				mode = "explicit-structured"
				bin = claudeMock
			}
			path, work := nativeScenario(t, kind, mode)
			_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
			require.NoError(t, err)
			defer h.Shutdown()
			server := httptest.NewServer(h)
			defer server.Close()
			input := resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}}
			for _, invalid := range []runtime.Input{
				{},
				{Content: []events.ContentBlock{{Image: &events.ContentBlockImage{Data: "aA==", MIMEType: "image/png"}}}},
				{Content: []events.ContentBlock{{Text: &events.ContentBlockText{Text: "hello", Annotations: &events.Annotations{Audience: []string{"user"}}}}}},
			} {
				input.InitialInput = &invalid
				_, err = svc.CreateSessionWithInput(ctx(t), input)
				require.Error(t, err)
				page, e := svc.ListSessions(resource.SessionQuery{})
				require.NoError(t, e)
				require.Empty(t, page.Items)
			}
			initial := []events.ContentBlock{events.Text("hello")}
			if kind == "codex" {
				initial = append(initial, events.Text("second block"))
			}
			input.InitialInput = &runtime.Input{Content: initial}
			inputPath := filepath.Join(t.TempDir(), "input.json")
			raw, err := json.Marshal(input.InitialInput)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(inputPath, raw, 0o600))
			cmd := exec.CommandContext(ctx(t), ctl, "--url", server.URL, "-o", "json", "session", "create", "first", "-n", "default", "--runtime", "agent", "--cwd", work, "--input-file", inputPath)
			raw, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", raw)
			var got resource.SessionCreated
			require.NoError(t, json.Unmarshal(raw, &got))
			require.NotNil(t, got.InitialInputReceipt)
			receipt := got.InitialInputReceipt
			require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
			require.Nil(t, receipt.Acceptance.NativeRecorded)
			if kind == "codex" {
				require.Equal(t, "turn-1", receipt.Submission)
				require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
			} else {
				require.Nil(t, receipt.Acceptance.NativeAccepted)
			}
			ref := resource.SessionRef{SessionID: got.Status.SessionID}
			require.Eventually(t, func() bool {
				r, e := svc.GetSession(ref)
				if e != nil || r.Status.State != events.Idle {
					return false
				}
				page, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				if e != nil {
					return false
				}
				for _, event := range page.Events {
					if event.Type() == "agent.message" {
						return true
					}
				}
				return false
			}, 5*time.Second, 10*time.Millisecond)
			history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			count := 0
			for _, e := range history.Events {
				if e.Type() == "agent.message" {
					count++
					b, err := json.Marshal(e)
					require.NoError(t, err)
					if kind == "claude" {
						require.Equal(t, "first-answer:0", e.ItemID)
						require.Contains(t, string(b), "explicit first answer")
					}
				}
			}
			require.Equal(t, 1, count, "complete native answer is persisted once")
			persisted, err := svc.GetSession(ref)
			require.NoError(t, err)
			raw, err = json.Marshal(persisted)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "initialInput")
			require.NotContains(t, string(raw), "second block")
			var annotated runtime.Input
			require.NoError(t, json.Unmarshal([]byte(`{"content":[{"type":"text","text":"must not be stripped","annotations":{"audience":["user"]}}]}`), &annotated))
			_, err = svc.SendPrompt(ctx(t), ref, annotated)
			var unsupported *runtime.OperationError
			require.ErrorAs(t, err, &unsupported)
			require.Equal(t, "unsupported", unsupported.Code, "normal and initial input must share lossless validation")

			_, err = svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
		})
	}
}

func TestTicket14PreInitPermissionIsExplicitFailure(t *testing.T) {
	svc, _, _ := setup(t, nil)
	path, work := nativeScenario(t, "claude", "explicit-permission")
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	result, err := svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}})
	require.Error(t, err)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "outcome_unknown", public.Code)
	require.Equal(t, "pre_identity_native_observation", public.Reason)
	require.Empty(t, result.Status.SessionID)
	require.Nil(t, result.InitialInputReceipt)
	page, err := svc.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	_, err = svc.StopRuntime(ctx(t), "agent")
	require.NoError(t, err, "owned process and opening reservation are settled, not the caller input side effects")
}

func TestTicket14EarlyCompletionContinuedEventsAndResumeNoReplay(t *testing.T) {
	bin := filepath.Join(root, "ticket14-claude-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/claude-continuity").CombinedOutput()
	require.NoError(t, err, "%s", out)
	evidence := filepath.Join(t.TempDir(), "inputs.jsonl")
	svc, _, _ := setup(t, nil)
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	configDir := t.TempDir()
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence, "EXPLICIT_CREATE": "1", "CLAUDE_CONFIG_DIR": configDir}}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	got, err := svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("first")}}})
	require.NoError(t, err)
	require.NotNil(t, got.InitialInputReceipt)
	ref := resource.SessionRef{SessionID: got.Status.SessionID}
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.State == events.Idle }, 5*time.Second, 10*time.Millisecond)
	first, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, first.Events, 1)
	require.Equal(t, got.InitialInputReceipt.Submission, first.Events[0].Meta["claude"].(map[string]any)["submission"])
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	c := newHTTPClient(t, server.URL, "")
	observer, err := c.Subscribe(ctx(t), ref)
	require.NoError(t, err)
	defer observer.Close()
	var receipt runtime.InputReceipt
	require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("second")}}, &receipt))
	require.NotEqual(t, got.InitialInputReceipt.Submission, receipt.Submission)
	var streamed events.SessionEvent
	for {
		e, err := observer.Next(ctx(t))
		require.NoError(t, err)
		if e.Type() == "agent.message" {
			streamed = e
		}
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, history.Events, 2)
	persisted, err := json.Marshal(history.Events[1])
	require.NoError(t, err)
	wire, err := json.Marshal(streamed)
	require.NoError(t, err)
	require.JSONEq(t, string(persisted), string(wire))
	require.Equal(t, "message-2:0", history.Events[1].ItemID)
	_, err = svc.CloseSession(ctx(t), ref)
	require.NoError(t, err)
	project := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(work, "-")
	transcriptDir := filepath.Join(configDir, "projects", project)
	require.NoError(t, os.MkdirAll(transcriptDir, 0o700))
	row, err := json.Marshal(map[string]any{
		"type": "user", "sessionId": ref.SessionID, "cwd": work, "uuid": "saved-user",
		"message": map[string]any{"role": "user", "content": "saved"},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(transcriptDir, ref.SessionID+".jsonl"), append(row, '\n'), 0o600))
	var resumed resource.SessionResumed
	require.NoError(t, c.ResumeSessionWithInput(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("resume")}}, &resumed))
	require.NotNil(t, resumed.InitialInputReceipt)
	var resumedMessage bool
	for {
		e, err := observer.Next(ctx(t))
		require.NoError(t, err)
		if e.Type() == "agent.message" {
			resumedMessage = true
		}
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle && resumedMessage {
			break
		}
	}
	require.True(t, resumedMessage)
	_, err = svc.CloseSession(ctx(t), ref)
	require.NoError(t, err)
	data, err := os.ReadFile(evidence)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 3, "resume must not replay create input")
	var a, b, resumedInput map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &a))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &b))
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &resumedInput))
	require.Equal(t, true, a["after_init"])
	require.Equal(t, a["pid"], b["pid"])
	require.Equal(t, "first", a["text"])
	require.Equal(t, "second", b["text"])
	require.Equal(t, "resume", resumedInput["text"])
	require.Equal(t, true, resumedInput["after_init"])
}

func TestTicket14UnrelatedResultsKeepCurrentWorkOpen(t *testing.T) {
	bin := filepath.Join(root, "ticket14-claude-bounds-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/claude-continuity").CombinedOutput()
	require.NoError(t, err, "%s", out)
	for _, mode := range []string{"initial-foreign", "initial-child"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _ := setup(t, nil)
			evidence := filepath.Join(t.TempDir(), "inputs.jsonl")
			work, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence, "EXPLICIT_CREATE": "1"}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			got, err := svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text(mode)}}})
			require.NoError(t, err)
			require.NotNil(t, got.InitialInputReceipt)
			ref := resource.SessionRef{SessionID: got.Status.SessionID}
			require.Eventually(t, func() bool {
				page, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				if e != nil || len(page.Events) != 1 {
					return false
				}
				state, e := svc.GetSession(ref)
				return e == nil && state.Status.State == events.Running
			}, 5*time.Second, 10*time.Millisecond, "foreign and child results must retain the observed running state")
			receipt, err := svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("follow-up")}})
			require.NoError(t, err)
			require.Equal(t, "applied_best_effort", string(receipt.Result))
			require.Eventually(t, func() bool {
				raw, e := os.ReadFile(evidence)
				return e == nil && strings.Count(string(raw), "\n") == 2 && strings.Contains(string(raw), "follow-up")
			}, 5*time.Second, 10*time.Millisecond, "SDK must receive the follow-up without a MASS idle wait")
			_, err = svc.Cancel(ctx(t), ref)
			require.NoError(t, err)
			_, err = svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
			_, err = svc.StopRuntime(ctx(t), "agent")
			require.NoError(t, err)
		})
	}
}

func TestTicket14IdentityCommitOwnsFirstFacts(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		for _, uncertain := range []bool{false, true} {
			name := kind + "/committed"
			if uncertain {
				name = kind + "/unknown"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				st, err := file.Open(dir, false)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, st.Close()) })
				var meta metastore.MetaStore = st
				if uncertain {
					meta = &unknownIdentity{MetaStore: st}
				}
				gate := &identityGate{MetaStore: meta, entered: make(chan struct{}), release: make(chan struct{})}
				var once sync.Once
				release := func() { once.Do(func() { close(gate.release) }) }
				defer release()
				svc := service.New(gate, st, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}, "codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}}))
				t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))) })
				bin := mock
				mode := "explicit-create"
				if kind == "claude" {
					bin = claudeMock
					mode = "explicit-structured"
				}
				path, work := nativeScenario(t, kind, mode)
				_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}})
				require.NoError(t, err)
				_, err = svc.StartRuntime(ctx(t), "agent")
				require.NoError(t, err)
				initial := []events.ContentBlock{events.Text("hello")}
				if kind == "codex" {
					initial = append(initial, events.Text("second block"))
				}
				input := resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &runtime.Input{Content: initial}}
				type result struct {
					value resource.SessionCreated
					err   error
				}
				done := make(chan result, 1)
				go func() { v, e := svc.CreateSessionWithInput(ctx(t), input); done <- result{v, e} }()
				select {
				case <-gate.entered:
				case <-ctx(t).Done():
					t.Fatal("native create never reached identity commit")
				}
				ref := resource.SessionRef{Namespace: "default", Name: "first"}
				_, err = svc.GetSession(ref)
				require.ErrorIs(t, err, store.ErrNotFound)
				_, err = svc.SubscribeEvents(ref)
				require.ErrorIs(t, err, store.ErrNotFound)
				_, err = svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.ErrorIs(t, err, store.ErrNotFound)
				select {
				case <-done:
					t.Fatal("create returned before durable identity commit")
				default:
				}
				release()
				var got result
				select {
				case got = <-done:
				case <-ctx(t).Done():
					t.Fatal("create did not settle")
				}
				if uncertain {
					require.ErrorContains(t, got.err, "unknown identity commit")
					t.Cleanup(func() {
						_ = svc.ReconcileOpening(ctx(t), "agent", runtime.Address{Namespace: "default", SessionName: "first"})
					})
					require.Nil(t, got.value.InitialInputReceipt, "failed operation must not return a successful receipt")
					_, err = svc.CreateSessionWithInput(ctx(t), input)
					require.ErrorIs(t, err, store.ErrConflict)
					require.NoError(t, svc.ReconcileOpening(ctx(t), "agent", runtime.Address{Namespace: "default", SessionName: "first"}))
				} else {
					require.NoError(t, got.err)
					require.NotNil(t, got.value.InitialInputReceipt)
				}
				wantMessages := 1
				require.Eventually(t, func() bool {
					page, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
					if e != nil {
						return false
					}
					n := 0
					for _, event := range page.Events {
						if event.Type() == "agent.message" {
							n++
						}
					}
					return n == wantMessages
				}, 5*time.Second, 10*time.Millisecond)
				page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, err)
				persisted, err := svc.GetSession(ref)
				require.NoError(t, err)
				for _, e := range page.Events {
					require.Equal(t, persisted.Status.SessionID, e.SessionID)
					require.Equal(t, "first", e.SessionName)
					require.Equal(t, events.Source{}, e.Source())
				}
				if !uncertain {
					_, err = svc.CloseSession(ctx(t), ref)
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestTicket14CodexLateInitialReceiptKeepsOwner(t *testing.T) {
	bin := filepath.Join(root, "ticket14-codex-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/ticket14-codex-peer").CombinedOutput()
	require.NoError(t, err, "%s", out)
	for _, stop := range []bool{false, true} {
		t.Run(strconv.FormatBool(stop), func(t *testing.T) {
			dir := t.TempDir()
			release := func() { require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)) }
			svc, _, _ := setup(t, nil)
			// Always release the authored peer before Service cleanup, including failure.
			t.Cleanup(release)
			_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"EVIDENCE": dir}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			input := resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: dir}}, InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("caller first")}}}
			caller, cancel := context.WithCancel(ctx(t))
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := svc.CreateSessionWithInput(caller, input); done <- e }()
			require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(dir, "dispatched")); return e == nil }, 5*time.Second, 10*time.Millisecond)
			cancel()
			select {
			case e := <-done:
				require.ErrorContains(t, e, "outcome_unknown")
			case <-ctx(t).Done():
				t.Fatal("abandoned caller did not return")
			}
			_, err = svc.CreateSessionWithInput(ctx(t), input)
			require.ErrorIs(t, err, store.ErrConflict, "original dispatch reserves its name")
			var stopped chan error
			if stop {
				stopped = make(chan error, 1)
				go func() { _, e := svc.StopRuntime(ctx(t), "agent"); stopped <- e }()
				select {
				case e := <-stopped:
					t.Fatalf("stop returned before late native receipt: %v", e)
				case <-time.After(100 * time.Millisecond):
				}
			}
			release()
			var stopErr error
			if stop {
				select {
				case e := <-stopped:
					stopErr = e
				case <-ctx(t).Done():
					t.Fatal("stop failed to settle late receipt")
				}
			} else {
				input.Metadata.Name = "healthy"
				got, e := svc.CreateSessionWithInput(ctx(t), input)
				require.NoError(t, e)
				require.Equal(t, "native-2", got.Status.SessionID)
				require.Equal(t, "turn-native-2", got.InitialInputReceipt.Submission)
				_, e = svc.CloseSession(ctx(t), resource.SessionRef{SessionID: got.Status.SessionID})
				require.NoError(t, e)
				_, stopErr = svc.StopRuntime(ctx(t), "agent")
			}
			if stopErr != nil {
				// A receipt can race the independent completion observer. A known
				// unsettled close retains the opening for explicit reconciliation;
				// it never authorizes repeating turn/start or replacing the owner.
				var outcome *runtime.OperationError
				require.ErrorAs(t, stopErr, &outcome)
				require.Equal(t, "outcome_unknown", outcome.Code)
				require.Equal(t, "runtime_stop_unsettled", outcome.Reason)
				t.Logf("public Stop retained original opening after receipt: %v", stopErr)
				var reconcileErr error
				require.Eventually(t, func() bool {
					reconcileErr = svc.ReconcileOpening(ctx(t), "agent", runtime.Address{Namespace: "default", SessionName: "first"})
					if reconcileErr != nil && !errors.Is(reconcileErr, runtime.ErrWorkUnsettled) {
						require.NoError(t, reconcileErr)
					}
					return reconcileErr == nil
				}, 5*time.Second, 10*time.Millisecond, "only confirmed WorkUnsettled (no close dispatched) permits another reconciliation: %v", reconcileErr)
				_, err = svc.StopRuntime(ctx(t), "agent")
				require.NoError(t, err)
			}
			_, err = svc.GetSession(resource.SessionRef{Namespace: "default", Name: "first"})
			require.ErrorIs(t, err, store.ErrNotFound, "abandoned create must release its native owner without publishing an uncommitted resource")
			raw, err := os.ReadFile(filepath.Join(dir, "pid"))
			require.NoError(t, err)
			pid, err := strconv.Atoi(string(raw))
			require.NoError(t, err)
			require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "owned shared peer reaped after all original receipt/cleanup responsibilities settled")
			require.FileExists(t, filepath.Join(dir, "stdin-closed"))
			raw, err = os.ReadFile(filepath.Join(dir, "wire.jsonl"))
			require.NoError(t, err)
			starts, closes := 0, 0
			for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
				var v struct {
					Method string
					Params map[string]any
				}
				require.NoError(t, json.Unmarshal([]byte(line), &v))
				if v.Method == "turn/start" && v.Params["threadId"] == "native-1" {
					starts++
				}
				if v.Method == "thread/unsubscribe" && v.Params["threadId"] == "native-1" {
					closes++
				}
			}
			require.Equal(t, 1, starts, "never retry unknown first dispatch")
			require.Equal(t, 1, closes, "release only original native identity once")
			t.Logf("resource ledger: peer pid=%d absent (ESRCH); initial dispatch=1, unsubscribe=1", pid)
		})
	}
}
