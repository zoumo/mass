package vertical_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

type nativeOverloadGate struct {
	eventstore.EventStore
	id      string
	release chan struct{}
	once    sync.Once
}

func (g *nativeOverloadGate) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == g.id {
		g.once.Do(func() { <-g.release })
	}
	return g.EventStore.Append(id, records)
}

func TestTicket05NativeProvidersIsolateSaturatedSession(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			root, rootErr := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, rootErr)
			bin := filepath.Join(root, "peer")
			out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/lifecycle-peer").CombinedOutput()
			require.NoError(t, err, string(out))
			st, err := file.Open(filepath.Join(root, "data"), false)
			require.NoError(t, err)
			defer st.Close()
			slowID := "damaged"
			if kind == "claude" {
				slowID = "claude-damaged"
			}
			gate := &nativeOverloadGate{EventStore: st, id: slowID, release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			reg := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(root, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(root, "private")}})
			svc := service.New(st, gate, reg)
			defer func() { release(); _ = svc.Shutdown(ctx(t)) }()
			pidFile := filepath.Join(root, "owned-pid")
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_PID": pidFile, "PEER_KIND": kind, "PEER_CONTROL": "overload"}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			refs := map[string]resource.SessionRef{}
			for _, name := range []string{"healthy", "damaged"} {
				work := filepath.Join(root, name)
				require.NoError(t, os.Mkdir(work, 0o700))
				r, e := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
				require.NoError(t, e)
				refs[name] = resource.SessionRef{SessionID: r.Status.SessionID}
			}
			h, err := transport.New(svc, transport.Options{Token: "synthetic"})
			require.NoError(t, err)
			defer h.Shutdown()
			server := httptest.NewServer(h)
			defer server.Close()
			c := client.New(server.URL, "synthetic")
			defer c.Close()
			observation, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			watch, err := c.Observe(observation, refs["healthy"])
			require.NoError(t, err)
			defer watch.Close()
			input := runtime.Input{Content: []events.ContentBlock{events.Text("synthetic")}}
			require.NoError(t, c.SendPrompt(observation, refs["damaged"], input, nil))
			require.Eventually(t, func() bool {
				var r resource.Session
				return c.GetSession(observation, refs["damaged"], &r) == nil && r.Status.State == "closing"
			}, 3*time.Second, 10*time.Millisecond)
			require.ErrorContains(t, c.SendPrompt(observation, refs["damaged"], input, nil), "session_delivery_overloaded")
			require.NoError(t, c.SendPrompt(observation, refs["healthy"], input, nil))
			for {
				e, eErr := watch.Next()
				require.NoError(t, eErr)
				if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle {
					break
				}
			}
			var healthy, slow store.HistoryPage
			require.NoError(t, c.History(observation, refs["healthy"], store.HistoryQuery{}, &healthy))
			require.Len(t, healthy.Events, 1)
			require.NoError(t, c.History(observation, refs["damaged"], store.HistoryQuery{}, &slow))
			require.Empty(t, slow.Events, "healthy native turn completed after saturation while slow storage remained gated")
			release()
			_, err = svc.ArchiveSession(observation, refs["damaged"])
			require.NoError(t, err)
			require.NoError(t, c.History(observation, refs["damaged"], store.HistoryQuery{Limit: 200}, &slow))
			require.Len(t, slow.Events, 160)
			for i, e := range slow.Events {
				message, ok := e.Payload().(events.AgentMessage)
				require.True(t, ok)
				require.Equal(t, fmt.Sprintf("final-%d", i), message.MessageID)
				require.Equal(t, []events.ContentBlock{events.Text("complete synthetic fact")}, message.Content)
			}
			require.NoError(t, svc.Shutdown(observation))
			owned, err := filepath.Glob(pidFile + ".*")
			require.NoError(t, err)
			require.NotEmpty(t, owned)
			for _, path := range owned {
				pid, e := os.ReadFile(path)
				require.NoError(t, e)
				require.Eventually(t, func() bool {
					output, e := exec.CommandContext(observation, "ps", "-p", string(pid), "-o", "pid=").Output()
					return e != nil && len(output) == 0 && observation.Err() == nil
				}, 3*time.Second, 20*time.Millisecond, "owned native process must be reaped")
			}
			t.Log("PASS synthetic native provider: healthy SSE/HTTP/history completes after slow saturation before gate release; 160 ordered full facts retained; native processes reaped")
		})
	}
}
