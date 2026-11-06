package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

	"github.com/zoumo/mass/mass-go/pkg/events"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	metafile "github.com/zoumo/mass/mass-go/pkg/metastore/file"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestTicket14ExplicitInputDurableReopenAndNativeLoadNeverReplay(t *testing.T) {
	bin := filepath.Join(root, "ticket14-recovery-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/ticket14-recovery-peer").CombinedOutput()
	require.NoError(t, err, "%s", out)
	binary, err := os.ReadFile(bin)
	require.NoError(t, err)
	t.Logf("owned recovery peer sha256=%x", sha256.Sum256(binary))
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			work, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			native := t.TempDir()
			config := t.TempDir()
			key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(work, "-")
			project := filepath.Join(config, "projects", key)
			require.NoError(t, os.MkdirAll(project, 0o700))
			const id = "14141414-1414-4141-8141-141414141414"
			ref := resource.SessionRef{SessionID: id}
			env := map[string]any{"PEER_KIND": kind, "PEER_DIR": native, "WORK_DIR": work, "CLAUDE_CONFIG_DIR": config, "SAVED_TRANSCRIPT": filepath.Join(project, id+".jsonl")}
			runtimeResource := resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": env}}}
			// Close and reopen BOTH file authorities and the Service, not just a
			// driver in the same registry. Native saved history survives separately.
			open := func(dir string, create bool) (*service.Service, func()) {
				meta, e := metafile.Open(dir, false)
				require.NoError(t, e)
				closeMeta := sync.OnceFunc(func() { assert.NoError(t, meta.Close(), "close metadata authority") })
				t.Cleanup(closeMeta)
				history, e := eventfile.Open(dir, false)
				require.NoError(t, e)
				closeHistory := sync.OnceFunc(func() { assert.NoError(t, history.Close(), "close event authority") })
				t.Cleanup(closeHistory)
				vault := filepath.Join(dir, "private")
				svc := service.New(meta, history, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: vault}, "codex": codex.Factory{RecoveryDir: vault}}))
				closeService := sync.OnceFunc(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					assert.NoError(t, svc.Shutdown(cleanup), "shutdown owned Service")
				})
				t.Cleanup(closeService)
				closeOwned := func() {
					closeService()
					closeHistory()
					closeMeta()
				}
				if create {
					_, e = svc.CreateRuntime(runtimeResource)
					require.NoError(t, e)
				}
				_, e = svc.StartRuntime(ctx(t), "agent")
				require.NoError(t, e)
				return svc, closeOwned
			}
			waitHistory := func(svc *service.Service, n int) store.HistoryPage {
				var page store.HistoryPage
				var session resource.Session
				require.Eventually(t, func() bool {
					page, err = svc.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
					if err != nil || len(page.Events) != n {
						return false
					}
					session, err = svc.GetSession(ref)
					return err == nil && session.Status.State == events.Idle
				}, 5*time.Second, 10*time.Millisecond, "expected %d durable answers and idle", n)
				return page
			}
			input := func(text string) runtime.Input {
				return runtime.Input{Content: []events.ContentBlock{events.Text(text)}}
			}
			data := t.TempDir()
			svc, closeFirst := open(data, true)
			firstInput := input("first explicit input")
			created, err := svc.CreateSessionWithInput(ctx(t), resource.SessionCreateInput{Metadata: resource.Metadata{Namespace: "default", Name: "first"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}, InitialInput: &firstInput})
			require.NoError(t, err)
			require.Equal(t, id, created.Status.SessionID)
			require.NotNil(t, created.InitialInputReceipt)
			first := waitHistory(svc, 1)
			require.Equal(t, "first explicit input", first.Events[0].Payload().(events.AgentMessage).Content[0].Text)
			_, err = svc.ArchiveSession(ctx(t), ref)
			require.NoError(t, err)
			_, err = svc.ResumeSession(ctx(t), ref)
			require.NoError(t, err)
			_, err = svc.SendPrompt(ctx(t), ref, input("after archive resume"))
			require.NoError(t, err)
			beforeRestart := waitHistory(svc, 2)
			_, err = svc.ArchiveSession(ctx(t), ref)
			require.NoError(t, err)
			closeFirst()

			reopened, closeReopened := open(data, false)
			durable, err := reopened.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
			require.NoError(t, err)
			require.Equal(t, beforeRestart, durable, "durable facts survive actual authority reopen unchanged")
			restored, err := reopened.ResumeSession(ctx(t), ref)
			require.NoError(t, err)
			require.Equal(t, id, restored.Status.SessionID)
			_, err = reopened.SendPrompt(ctx(t), ref, input("after durable restart"))
			require.NoError(t, err)
			restarted := waitHistory(reopened, 3)
			require.Equal(t, first.Events[0], restarted.Events[0], "original fact must not be rewritten or duplicated")
			_, err = reopened.ArchiveSession(ctx(t), ref)
			require.NoError(t, err)
			closeReopened()

			// Import the history actually written by those three native inputs into
			// a fresh MASS authority; no preset transcript or metadata-only proof.
			loaded, closeLoaded := open(t.TempDir(), true)
			_, err = loaded.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "default", Name: "imported"}, RuntimeRef: "agent", Cwd: work, SessionID: id})
			require.NoError(t, err)
			var state resource.Session
			require.Eventually(t, func() bool {
				state, err = loaded.GetSession(ref)
				return err == nil && (state.Status.HistoryLoad.Phase == "completed" || state.Status.HistoryLoad.Phase == "failed")
			}, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, "completed", state.Status.HistoryLoad.Phase, "%+v", state.Status.HistoryLoad)
			require.True(t, state.Status.HistoryLoad.DeliveryCompleted)
			imported, err := loaded.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, imported.Events, 6)
			for i, text := range []string{"first explicit input", "after archive resume", "after durable restart"} {
				require.Equal(t, text, imported.Events[2*i].Payload().(events.UserMessage).Content[0].Text)
				require.Equal(t, text, imported.Events[2*i+1].Payload().(events.AgentMessage).Content[0].Text)
			}
			_, err = loaded.SendPrompt(ctx(t), ref, input("after native load"))
			require.NoError(t, err)
			continued := waitHistory(loaded, 4)
			require.Equal(t, "after native load", continued.Events[3].Payload().(events.AgentMessage).Content[0].Text)
			_, err = loaded.ArchiveSession(ctx(t), ref)
			require.NoError(t, err)
			closeLoaded()

			wire, err := os.ReadFile(filepath.Join(native, "wire.jsonl"))
			require.NoError(t, err)
			var texts []string
			opened, released := map[int]bool{}, map[int]bool{}
			starts, resumes, reads := 0, 0, 0
			for line := range strings.SplitSeq(strings.TrimSpace(string(wire)), "\n") {
				var call ticket12Call
				require.NoError(t, json.Unmarshal([]byte(line), &call))
				switch call.Method {
				case "opened":
					opened[call.PID] = true
				case "released":
					released[call.PID] = true
				case "thread/start":
					starts++
				case "thread/resume":
					resumes++
				case "thread/read":
					reads++
				case "input":
					require.Equal(t, id, call.Params["sessionId"])
					texts = append(texts, call.Params["text"].(string))
				}
			}
			require.Equal(t, []string{"first explicit input", "after archive resume", "after durable restart", "after native load"}, texts, "no archive/resume/restart/load may resubmit creation input")
			if kind == "codex" {
				require.Equal(t, 1, starts)
				require.Equal(t, 3, resumes)
				require.Positive(t, reads)
			}
			require.Equal(t, opened, released, "every owned peer observed stdin EOF")
			for pid := range opened {
				require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "peer %d remains", pid)
			}
		})
	}
}
