package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestTicket17ClaudeProvenMessageLinkage(t *testing.T) {
	bin := filepath.Join(root, "ticket17-continuity-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/claude-continuity").CombinedOutput()
	require.NoError(t, err, "%s", out)
	for _, mode := range []string{"proof", "different", "no-start", "conflict", "prior-work", "retired", "unproven", "child-first", "child", "unlinked-start", "child-start", "foreign-start", "stopped-preview", "repeated-start", "saturation"} {
		t.Run(mode, func(t *testing.T) {
			st, err := file.Open(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, st.Close()) })
			registry := runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: t.TempDir()}})
			svc := service.New(st, st, registry)
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := svc.Shutdown(cleanup)
				if mode == "foreign-start" && err != nil {
					assert.ErrorContains(t, err, "session_identity_mismatch")
				} else {
					assert.NoError(t, err)
				}
			})
			h, err := transport.New(svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			t.Cleanup(h.Shutdown)
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			c := newHTTPClient(t, server.URL, "")
			evidence := filepath.Join(t.TempDir(), "inputs.jsonl")
			config := t.TempDir()
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence, "CLAUDE_CONFIG_DIR": config}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			opened, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "linked"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: opened.Status.SessionID}
			prepareClaudeSavedSession(t, config, cwd, ref.SessionID)
			// One subscription spans up to 16 inputs, not one ten-second call.
			observationCtx, cancelObservation := context.WithTimeout(t.Context(), 17*10*time.Second)
			t.Cleanup(cancelObservation)
			stopOpeningDeadline := context.AfterFunc(ctx(t), cancelObservation)
			observer, err := c.Observe(observationCtx, ref)
			stopOpeningDeadline()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, observer.Close()) })
			var live []events.SessionEvent
			forwardingGaps := 0
			sendWith := func(text string, proven bool, submit func(*runtime.InputReceipt) error) {
				t.Helper()
				workCtx := ctx(t)
				// Keep each send-to-idle observation bounded even on a quiet SSE stream.
				stopWorkDeadline := context.AfterFunc(workCtx, cancelObservation)
				defer stopWorkDeadline()
				var receipt runtime.InputReceipt
				require.NoError(t, submit(&receipt))
				require.NotEmpty(t, receipt.Submission)
				seen := 0
				sawContent := false
				for {
					e, err := observer.Next()
					require.NoError(t, err)
					if e.Type() == "session.error" {
						requireClaudeForwardingGap(t, e)
						forwardingGaps++
					}
					if e.Type() == "agent.message" || e.Type() == "agent.thinking" || e.Type() == "agent.message_chunk" || e.Type() == "agent.thinking_chunk" {
						sawContent = true
						if strings.HasPrefix(text, "linkage-") {
							seen++
							meta := e.Meta["claude"].(map[string]any)
							// Identity comes from the SDK's native response/block coordinates.
							// Native UUIDs remain evidence; MASS must not add submission attribution.
							require.NotEmpty(t, meta["responseId"])
							item := fmt.Sprint(meta["responseId"]) + ":" + fmt.Sprint(meta["blockIndex"])
							if parent, ok := meta["parentToolUseId"].(string); ok && parent != "" {
								item = parent + ":" + item
							}
							assert.Equal(t, item, e.ItemID)
							assert.NotContains(t, meta, "submission")
							assert.NotContains(t, meta, "attribution")
							raw, ok := meta["raw"].(map[string]any)
							require.True(t, ok, "SDK native JSON metadata must survive SSE")
							if raw["type"] == "assistant" {
								assert.Equal(t, meta["responseId"], raw["message"].(map[string]any)["id"])
								assert.Equal(t, fmt.Sprint(meta["blockIndex"]), fmt.Sprint(raw["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["apiBlockIndex"]))
							}
						}
						assert.Equal(t, events.Source{}, e.Source())
						if e.Type() == "agent.message" || e.Type() == "agent.thinking" {
							live = append(live, e)
						}
					}
					if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && sawContent {
						break
					}
				}
				require.LessOrEqual(t, forwardingGaps, 1)
				if strings.HasPrefix(text, "linkage-") {
					if text == "linkage-saturation" {
						assert.Equal(t, 32, seen, "all native facts survive repeated response identities")
					} else if proven {
						assert.Equal(t, 4, seen, "both previews and complete thinking/text retain SDK block identity")
					} else {
						assert.GreaterOrEqual(t, seen, 2)
					}
				}
			}
			send := func(text string, proven bool) {
				sendWith(text, proven, func(receipt *runtime.InputReceipt) error {
					return c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text(text)}}, receipt)
				})
			}
			send("warmup", false)
			// A second known work makes prior-work conflicts independent of
			// the first submission's special no-UUID native contract.
			send("warmup-again", false)
			if mode == "foreign-start" {
				before, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, err)
				_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("linkage-" + mode)}})
				require.NoError(t, err)
				driver, err := registry.Get("agent")
				require.NoError(t, err)
				observation := driver.(interface {
					SessionObservation(string) runtime.SessionObservation
				}).SessionObservation(ref.SessionID)
				select {
				case <-observation.Done():
				case <-ctx(t).Done():
					t.Fatal("foreign session identity was not rejected")
				}
				require.ErrorContains(t, observation.Err(), "session_identity_mismatch")
				after, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, err)
				require.Equal(t, before, after)
				data, err := os.ReadFile(evidence)
				require.NoError(t, err)
				for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
					var input struct {
						PID int `json:"pid"`
					}
					require.NoError(t, json.Unmarshal([]byte(line), &input))
					require.Eventually(t, func() bool { return errors.Is(syscall.Kill(input.PID, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond)
				}
				return
			}
			send("linkage-"+mode, mode == "proof" || mode == "stopped-preview")
			if mode == "saturation" {
				// Public terminal observations pace the native flow without
				// intentionally overflowing the independent delivery queue.
				for range 8 {
					send("linkage-saturation", false)
				}
				// No unretained ID may acquire proof after saturation, even
				// through a later linked start or a new admitted work.
				send("linkage-proof", false)
				send("linkage-explicit", false)
				send("linkage-unproven", false)
				send("linkage-retired", false)
				send("linkage-repeated-start", false)
			}
			if mode == "proof" {
				send("linkage-proof", true)
				require.NoError(t, c.CloseSession(ctx(t), ref, nil))
				// Same native ID on a new owned process cannot inherit old proof.
				sendWith("linkage-no-start", false, func(receipt *runtime.InputReceipt) error {
					var resumed resource.SessionResumed
					if err := c.ResumeSessionWithInput(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("linkage-no-start")}}, &resumed); err != nil {
						return err
					}
					if resumed.InitialInputReceipt == nil {
						return errors.New("resume did not return an initial input receipt")
					}
					*receipt = *resumed.InitialInputReceipt
					return nil
				})
			}
			var history []events.SessionEvent
			query := store.HistoryQuery{}
			for {
				var page store.HistoryPage
				require.NoError(t, c.History(ctx(t), ref, query, &page))
				history = append(history, page.Events...)
				require.LessOrEqual(t, len(history), len(live))
				if page.Next == "" {
					break
				}
				require.NotEmpty(t, page.Events)
				require.NotEqual(t, query.After, page.Next)
				query.After = page.Next
			}
			require.Len(t, history, len(live))
			for i, e := range history {
				assert.Equal(t, live[i].EventID, e.EventID)
				assert.Equal(t, live[i].Meta, e.Meta, "no terminal backfill; exact raw provenance persisted")
				assert.Equal(t, live[i].Payload(), e.Payload())
			}
			require.NoError(t, c.CloseSession(ctx(t), ref, nil))
			_, err = svc.StopRuntime(ctx(t), "agent")
			require.NoError(t, err)
			data, err := os.ReadFile(evidence)
			require.NoError(t, err)
			for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
				var input struct {
					PID int `json:"pid"`
				}
				require.NoError(t, json.Unmarshal([]byte(line), &input))
				require.ErrorIs(t, syscall.Kill(input.PID, 0), syscall.ESRCH, "owned peer must have exited")
			}
			t.Log("PASS owned native peers exited; SSE/history retained identical facts")
		})
	}
}
