package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

var (
	ticket08Build    sync.Once
	ticket08Binary   string
	ticket08BuildErr error
)

type ticket08Gate struct {
	eventstore.EventStore
	item             string
	entered, release chan struct{}
	once             sync.Once
}

// Commit an admitted prefix before blocking the target record. Provider batches
// may differ; the public EventStore boundary defines the same fault point.
func (g *ticket08Gate) Append(id string, batch []events.SessionEvent) (*string, error) {
	var appendErr error
	for i, e := range batch {
		if e.ItemID != g.item && (e.Replay == nil || e.Replay.NativeRecordID != g.item) {
			continue
		}
		g.once.Do(func() {
			if i > 0 {
				_, appendErr = g.EventStore.Append(id, batch[:i])
				if appendErr != nil {
					return
				}
				batch = batch[i:]
			}
			close(g.entered)
			<-g.release
		})
		if appendErr != nil {
			return nil, appendErr
		}
		break
	}
	return g.EventStore.Append(id, batch)
}

type ticket08Factory struct {
	runtime.Factory
	requests  chan events.InteractionRequested
	configure func(*ticket08Driver)
}

func (f ticket08Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	d, err := f.Factory.New(name, c)
	if err != nil {
		return nil, err
	}
	wrapped := &ticket08Driver{Driver: d, requests: f.requests}
	if f.configure != nil {
		f.configure(wrapped)
	}
	return wrapped, nil
}

type ticket08Driver struct {
	runtime.Driver
	requests      chan events.InteractionRequested
	cancelEntered chan struct{}
	cancelOnce    sync.Once
	unknown       atomic.Bool
	injected      atomic.Bool
}

func (d *ticket08Driver) CancelHistoryLoad(ctx context.Context, id, load string) error {
	if d.cancelEntered != nil {
		d.cancelOnce.Do(func() { close(d.cancelEntered) })
	}
	err := d.Driver.CancelHistoryLoad(ctx, id, load)
	if err == nil && d.unknown.Load() {
		d.injected.Store(true)
		return runtime.Unavailable("injected cleanup acknowledgement lost")
	}
	return err
}

func (d *ticket08Driver) Events() (events.Stream, error) {
	s, err := d.Driver.Events()
	return ticket08Stream{s, d.requests}, err
}

type ticket08Stream struct {
	events.Stream
	requests chan events.InteractionRequested
}

func (s ticket08Stream) Next(c context.Context) (events.SessionEvent, error) {
	e, err := s.Stream.Next(c)
	if p, ok := e.Payload().(events.InteractionRequested); ok {
		s.requests <- p
	}
	return e, err
}

func ticket08HTTP(t *testing.T, kind string, gate *ticket08Gate, configure ...func(*ticket08Driver)) (*client.Client, chan events.InteractionRequested, string) {
	t.Helper()
	ticket08Build.Do(func() {
		ticket08Binary = filepath.Join(root, "ticket08-lifecycle-peer")
		out, err := exec.CommandContext(ctx(t), "go", "build", "-o", ticket08Binary, "./testdata/ticket08-lifecycle-peer").CombinedOutput()
		if err != nil {
			ticket08BuildErr = fmt.Errorf("build: %w: %s", err, out)
		}
	})
	require.NoError(t, ticket08BuildErr)
	binary, err := os.ReadFile(ticket08Binary)
	require.NoError(t, err)
	t.Logf("owned authored peer sha256=%x", sha256.Sum256(binary))
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	gate.EventStore = st
	var native runtime.Factory = codex.Factory{RecoveryDir: t.TempDir()}
	if kind == "claude" {
		native = claude.Factory{RecoveryDir: t.TempDir()}
	}
	requests := make(chan events.InteractionRequested, 8)
	var option func(*ticket08Driver)
	if len(configure) > 0 {
		option = configure[0]
	}
	var owned *ticket08Driver
	wrappedOption := func(d *ticket08Driver) {
		owned = d
		if option != nil {
			option(d)
		}
	}
	svc := service.New(st, gate, runtime.NewRegistry(map[string]runtime.Factory{kind: ticket08Factory{native, requests, wrappedOption}}))
	project, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	evidence := t.TempDir()
	config := t.TempDir()
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(project, "-")
	dir := filepath.Join(config, "projects", key)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	f, err := os.Create(filepath.Join(dir, "11111111-1111-4111-8111-111111111111.jsonl"))
	require.NoError(t, err)
	for i, id := range []string{"saved-first", "saved-last"} {
		var parent any
		if i > 0 {
			parent = "saved-first"
		}
		require.NoError(t, json.NewEncoder(f).Encode(map[string]any{"type": "assistant", "uuid": id, "parentUuid": parent, "sessionId": "11111111-1111-4111-8111-111111111111", "cwd": project, "message": map[string]any{"id": id, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "retained " + id}}}}))
	}
	require.NoError(t, f.Close())
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": ticket08Binary, "envPolicy": "replace", "env": map[string]any{"KIND": kind, "PROJECT": project, "EVIDENCE": evidence, "CLAUDE_CONFIG_DIR": config}}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	server := httptest.NewServer(h)
	c := newHTTPClient(t, server.URL, "")
	t.Cleanup(func() {
		server.Close()
		h.Shutdown()
		shutdownErr := svc.Shutdown(ctx(t))
		if owned.injected.Load() {
			require.ErrorContains(t, shutdownErr, "injected cleanup acknowledgement lost", "the Service still owns unresolved cleanup")
			require.NoError(t, owned.Driver.Stop(ctx(t)), "explicit teardown releases underlying native resources; not proof the earlier Service stop succeeded")
		} else {
			require.NoError(t, shutdownErr)
		}
		pids, readErr := os.ReadDir(evidence)
		require.NoError(t, readErr)
		require.NotEmpty(t, pids)
		for _, entry := range pids {
			pid := strings.TrimPrefix(entry.Name(), "pid-")
			out, psErr := exec.CommandContext(ctx(t), "ps", "-p", pid, "-o", "pid=").CombinedOutput()
			var absent *exec.ExitError
			require.ErrorAs(t, psErr, &absent)
			require.Equal(t, 1, absent.ExitCode())
			require.Empty(t, strings.TrimSpace(string(out)))
		}
		t.Log("cleanup readback: all owned peer PIDs absent after native release; RPC listener closed")
	})
	t.Cleanup(func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	})
	return c, requests, project
}

func ticket08Wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx(t).Done():
		t.Fatal("gate not reached")
	}
}

func ticket08Request(t *testing.T, ch <-chan events.InteractionRequested) events.InteractionRequested {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-ctx(t).Done():
		t.Fatal("interaction not delivered")
		return events.InteractionRequested{}
	}
}

func ticket08Session(t *testing.T, c *client.Client, project, name string) resource.SessionRef {
	t.Helper()
	cwd := filepath.Join(project, name)
	require.NoError(t, os.MkdirAll(cwd, 0o700))
	var s resource.Session
	require.NoError(t, c.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}}, &s))
	return resource.SessionRef{SessionID: s.Status.SessionID}
}

func TestTicket08CloseRevokesQueuedAuthorityAndDrains(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			gate := &ticket08Gate{item: messageItemID(kind, "before-close"), entered: make(chan struct{}), release: make(chan struct{})}
			c, requests, project := ticket08HTTP(t, kind, gate)
			ref := ticket08Session(t, c, project, "active")
			healthy := ticket08Session(t, c, project, "healthy")
			observer, err := c.Subscribe(ctx(t), ref)
			require.NoError(t, err)
			defer observer.Close()
			require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, nil))
			current := ticket08Request(t, requests)
			ticket08Wait(t, gate.entered)
			for {
				e, nextErr := observer.Next(ctx(t))
				require.NoError(t, nextErr)
				if p, ok := e.Payload().(events.InteractionRequested); ok && p.Interaction == current.Interaction {
					break
				}
			}
			queued := ticket08Request(t, requests)
			require.NotEqual(t, current.Interaction, queued.Interaction)
			require.Equal(t, "current", current.Request.ToolCallID)
			require.Equal(t, "queued", queued.Request.ToolCallID)
			closed := make(chan error, 1)
			go func() { closed <- c.CloseSession(ctx(t), ref, nil) }()
			deadline := ctx(t)
			for {
				var s resource.Session
				require.NoError(t, c.GetSession(deadline, ref, &s))
				if string(s.Status.State) == "closing" {
					break
				}
				require.NoError(t, deadline.Err())
			}
			for _, p := range []events.InteractionRequested{current, queued} {
				answerErr := c.Answer(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "allow-once"}, nil)
				var public *transport.Error
				require.ErrorAs(t, answerErr, &public)
				require.Equal(t, "conflict", public.Code, "valid old answer must be rejected by lifecycle authority, not malformed input")
			}
			close(gate.release)
			select {
			case err := <-closed:
				require.NoError(t, err)
			case <-ctx(t).Done():
				t.Fatal("close failed to drain")
			}
			var page store.HistoryPage
			require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
			facts := map[string]string{}
			expectedFacts := []string{"before-close", "queued-last", "cancel-final"}
			for _, e := range page.Events {
				if p, ok := e.Payload().(events.AgentMessage); ok {
					for _, id := range expectedFacts {
						if p.MessageID != messageItemID(kind, id) {
							continue
						}
						require.Equal(t, []events.ContentBlock{events.Text("complete " + id)}, p.Content)
						facts[id] = e.EventID
					}
				}
			}
			for _, id := range []string{"before-close", "queued-last"} {
				require.NotEmpty(t, facts[id], "lost complete fact %s", id)
			}
			require.Empty(t, facts["cancel-final"], "close must not inject a native cancellation")
			seen := map[string]bool{}
			for !seen[facts["queued-last"]] {
				e, nextErr := observer.Next(ctx(t))
				require.NoError(t, nextErr)
				seen[e.EventID] = true
			}
			for _, id := range []string{"before-close", "queued-last"} {
				require.True(t, seen[facts[id]], "fact must be published before retiring binding")
			}
			for _, p := range []events.InteractionRequested{current, queued} {
				answerErr := c.Answer(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "allow-once"}, nil)
				var public *transport.Error
				require.ErrorAs(t, answerErr, &public)
				require.Equal(t, "conflict", public.Code, "valid old answer must be rejected by lifecycle authority, not malformed input")
			}
			healthyObserver, err := c.Subscribe(ctx(t), healthy)
			require.NoError(t, err)
			defer healthyObserver.Close()
			require.NoError(t, c.SendPrompt(ctx(t), healthy, runtime.Input{Content: []events.ContentBlock{events.Text("still healthy")}}, nil))
			healthyID := messageItemID(kind, "healthy-fact")
			for {
				e, nextErr := healthyObserver.Next(ctx(t))
				require.NoError(t, nextErr)
				if p, ok := e.Payload().(events.AgentMessage); ok && p.MessageID == healthyID {
					break
				}
			}
			require.NoError(t, c.StopRuntime(ctx(t), "agent", nil))
		})
	}
}

func TestTicket08StopDuringLoadPreservesFactsAndCleanupResponsibility(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%t", kind, unknown), func(t *testing.T) {
				gate := &ticket08Gate{item: "saved-last", entered: make(chan struct{}), release: make(chan struct{})}
				entered := make(chan struct{})
				var driver *ticket08Driver
				c, _, project := ticket08HTTP(t, kind, gate, func(d *ticket08Driver) { driver = d; d.cancelEntered = entered; d.unknown.Store(unknown) })
				defer driver.unknown.Store(false)
				ref := resource.SessionRef{SessionID: "11111111-1111-4111-8111-111111111111"}
				var accepted resource.SessionLoadAccepted
				require.NoError(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: project, SessionID: ref.SessionID}, &accepted))
				ticket08Wait(t, gate.entered)
				var before store.HistoryPage
				require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &before))
				require.Len(t, before.Events, 1)
				require.Equal(t, "saved-first", before.Events[0].Replay.NativeRecordID)
				stopped := make(chan error, 1)
				go func() { stopped <- c.StopRuntime(ctx(t), "agent", nil) }()
				ticket08Wait(t, entered)
				require.Error(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}}, nil))
				close(gate.release)
				select {
				case err := <-stopped:
					if unknown {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				case <-ctx(t).Done():
					t.Fatal("stop failed to drain")
				}
				var session resource.Session
				require.NoError(t, c.GetSession(ctx(t), ref, &session))
				require.NotNil(t, session.Status.HistoryLoad)
				status := session.Status.HistoryLoad
				require.Equal(t, accepted.LoadID, status.LoadID)
				require.Equal(t, "canceled", status.Phase)
				require.False(t, status.DeliveryCompleted, "an interrupted import is never complete")
				require.Equal(t, !unknown, status.CleanupCompleted)
				var after store.HistoryPage
				require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &after))
				require.Len(t, after.Events, 2)
				require.Equal(t, before.Events[0].EventID, after.Events[0].EventID, "committed prefix cannot be replaced on stop")
				for i, id := range []string{"saved-first", "saved-last"} {
					require.Equal(t, id, after.Events[i].Replay.NativeRecordID)
					require.Equal(t, accepted.LoadID, after.Events[i].Replay.LoadID)
				}
				var runtimeState resource.Runtime
				require.NoError(t, c.GetRuntime(ctx(t), "agent", &runtimeState))
				if unknown {
					require.NotEqual(t, "stopped", runtimeState.Status.State)
					require.NotNil(t, status.Error)
				} else {
					require.Equal(t, "stopped", runtimeState.Status.State)
				}
			})
		}
	}
}
