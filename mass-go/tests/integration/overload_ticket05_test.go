package integration_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
			ownedRoot, rootErr := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, rootErr)
			// Keep the executable visible to TestMain's failure cleanup scan.
			peerDir := filepath.Join(root, "overload-"+kind)
			require.NoError(t, os.Mkdir(peerDir, 0o700))
			t.Cleanup(func() {
				assert.NoError(t, os.RemoveAll(peerDir))
				_, err := os.Stat(peerDir)
				assert.True(t, os.IsNotExist(err))
			})
			bin := filepath.Join(peerDir, "peer")
			out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/lifecycle-peer").CombinedOutput()
			require.NoError(t, err, string(out))
			st, err := file.Open(filepath.Join(ownedRoot, "data"), false)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, st.Close()) })
			slowID := "damaged"
			if kind == "claude" {
				slowID = "claude-damaged"
			}
			gate := &nativeOverloadGate{EventStore: st, id: slowID, release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			reg := runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: filepath.Join(ownedRoot, "private")}, "claude": claude.Factory{RecoveryDir: filepath.Join(ownedRoot, "private")}})
			svc := service.New(st, gate, reg)
			pidFile := filepath.Join(ownedRoot, "owned-pid")
			expectProcesses := false
			t.Cleanup(func() {
				release()
				cleanup, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancelCleanup()
				assert.NoError(t, svc.Shutdown(cleanup), "owned shutdown must be confirmed even after a failed assertion")
				owned, globErr := filepath.Glob(pidFile + ".*")
				assert.NoError(t, globErr)
				if expectProcesses {
					expected := 1 // Codex multiplexes both sessions in one owned process.
					if kind == "claude" {
						expected = 2
					}
					assert.Len(t, owned, expected)
				}
				for _, path := range owned {
					pid, readErr := os.ReadFile(path)
					if !assert.NoError(t, readErr) {
						continue
					}
					if assert.Eventually(t, func() bool {
						output, psErr := exec.CommandContext(cleanup, "ps", "-p", strings.TrimSpace(string(pid)), "-o", "pid=").Output()
						return psErr != nil && len(output) == 0 && cleanup.Err() == nil
					}, 3*time.Second, 20*time.Millisecond, "owned native process must be reaped: %s", pid) {
						t.Logf("PASS owned native process reaped pid=%s", pid)
					}
				}
			})
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_PID": pidFile, "PEER_KIND": kind, "PEER_CONTROL": "overload"}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			refs := map[string]resource.SessionRef{}
			for _, name := range []string{"healthy", "damaged"} {
				work := filepath.Join(ownedRoot, name)
				require.NoError(t, os.Mkdir(work, 0o700))
				r, e := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
				require.NoError(t, e)
				refs[name] = resource.SessionRef{SessionID: r.Status.SessionID}
			}
			expectProcesses = true
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
			if os.Getenv("MASS_REVIEW_OVERLOAD_FAIL") == kind {
				t.Fatal("injected failure after saturation before gate release")
			}
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
			t.Log("PASS synthetic native provider: healthy SSE/HTTP/history completes after slow saturation before gate release; 160 ordered full facts retained; native processes reaped")
		})
	}
}

// A subprocess makes a fatal assertion observable without failing this parent.
// The child uses the actual native adapters and fails while storage is gated.
func TestOverloadFailureCleanupReadback(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			bounded, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(bounded, executable, "-test.v", "-test.run=^TestTicket05NativeProvidersIsolateSaturatedSession$/"+kind+"$")
			cmd.Env = append(os.Environ(), "MASS_REVIEW_OVERLOAD_FAIL="+kind)
			output, runErr := cmd.CombinedOutput()
			t.Log(string(output))
			require.Error(t, runErr, "child must retain the deliberately injected assertion failure")
			require.NoError(t, bounded.Err())
			require.Contains(t, string(output), "injected failure after saturation before gate release")
			expected := 1
			if kind == "claude" {
				expected = 2
			}
			require.Equal(t, expected, strings.Count(string(output), "PASS owned native process reaped pid="), "all provider-owned PIDs independently read back after failure")
			require.NotContains(t, string(output), "Error Trace:", "cleanup must add no assertion failures to the deliberately failed child")
			require.Contains(t, string(output), "PASS no owned test-peer/application processes remain")
			require.NotContains(t, string(output), "FAIL: leaked owned child")
		})
	}
}
