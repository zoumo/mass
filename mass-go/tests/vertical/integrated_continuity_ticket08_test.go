package vertical_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestTicket08ContinuousInputWithApproval(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "continuity-peer")
			out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/ticket08-continuity-peer").CombinedOutput()
			require.NoError(t, err, string(out))
			binary, err := os.ReadFile(bin)
			require.NoError(t, err)
			t.Logf("owned authored peer sha256=%x", sha256.Sum256(binary))
			evidence := filepath.Join(t.TempDir(), "inputs.jsonl")
			// Registered before Service cleanup so failure paths also read release back.
			t.Cleanup(func() {
				snapshot, readErr := exec.CommandContext(ctx(t), "ps", "-axo", "pid=,args=").Output()
				require.NoError(t, readErr)
				require.NotContains(t, string(snapshot), bin, "owned peer must be released even after a failed assertion")
			})
			svc, _, _ := setup(t, nil)
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_KIND": kind, "PEER_EVIDENCE": evidence}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			opened, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "continuous"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: opened.Status.SessionID}
			h, err := transport.New(svc, transport.Options{Token: "synthetic"})
			require.NoError(t, err)
			defer h.Shutdown()
			server := httptest.NewServer(h)
			defer server.Close()
			c := newHTTPClient(t, server.URL, "synthetic")
			watch, err := c.Observe(ctx(t), ref)
			require.NoError(t, err)
			defer watch.Close()
			var observed []events.SessionEvent
			var previous string
			for n := 1; n <= 3; n++ {
				cmd := exec.CommandContext(ctx(t), ctl, "--url", server.URL, "-o", "json", "session", "prompt", ref.SessionID, "--text", fmt.Sprintf("input-%d", n))
				cmd.Env = []string{"MASS_TOKEN=synthetic"}
				out, err = cmd.Output()
				require.NoError(t, err)
				var receipt runtime.InputReceipt
				require.NoError(t, json.Unmarshal(out, &receipt))
				require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
				require.NotEqual(t, previous, receipt.Submission)
				previous = receipt.Submission
				if kind == "claude" {
					require.Nil(t, receipt.Acceptance.NativeAccepted)
				} else {
					require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
				}
				answered, message := false, false
				for {
					e, err := watch.Next()
					require.NoError(t, err)
					if p, ok := e.Payload().(events.InteractionRequested); ok {
						require.Equal(t, 2, n, "only the middle input needs approval")
						var answer runtime.InteractionReceipt
						require.Error(t, c.Answer(ctx(t), ref, p.Interaction, map[string]any{"outcome": "selected", "optionId": "not-offered"}, &answer))
						valid := map[string]any{"outcome": "selected", "optionId": "allow-once"}
						require.NoError(t, c.Answer(ctx(t), ref, p.Interaction, valid, &answer))
						require.Equal(t, runtime.Yes(), answer.Evidence.Sent)
						require.Nil(t, answer.Evidence.NativeAccepted)
						require.Error(t, c.Answer(ctx(t), ref, p.Interaction, valid, nil), "consumed authority cannot be reused")
						answered = true
					}
					if p, ok := e.Payload().(events.AgentMessage); ok {
						require.Equal(t, []events.ContentBlock{events.Text(fmt.Sprintf("complete-%d", n))}, p.Content)
						require.Nil(t, e.Replay)
						observed = append(observed, e)
						message = true
					}
					if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && message {
						require.Equal(t, n == 2, answered, "middle work must wait for current native approval")
						break
					}
				}
			}
			var page store.HistoryPage
			require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
			require.Equal(t, observed, page.Events, "complete SSE facts and public durable history must agree")
			require.NoError(t, c.ArchiveSession(ctx(t), ref, nil))
			require.NoError(t, c.StopRuntime(ctx(t), "agent", nil))
			data, err := os.ReadFile(evidence)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.Len(t, lines, 3)
			var pid int
			for i, line := range lines {
				var entry struct{ PID, Input int }
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				require.Equal(t, i+1, entry.Input)
				if i == 0 {
					pid = entry.PID
				}
				require.Equal(t, pid, entry.PID, "all three inputs use one owned native process")
			}
			out, err = exec.CommandContext(ctx(t), "ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output()
			require.Error(t, err)
			require.Empty(t, out, "owned native process has been reaped")
		})
	}
}
