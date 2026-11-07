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
			svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: t.TempDir()}}))
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, svc.Shutdown(cleanup))
			})
			h, err := transport.New(svc, transport.Options{LocalOnly: true})
			require.NoError(t, err)
			t.Cleanup(h.Shutdown)
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			c := newHTTPClient(t, server.URL, "")
			evidence := filepath.Join(t.TempDir(), "inputs.jsonl")
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": map[string]any{"PEER_EVIDENCE": evidence}}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			opened, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "linked"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: opened.Status.SessionID}
			// One subscription spans up to 16 inputs, not one ten-second call.
			observationCtx, cancelObservation := context.WithTimeout(t.Context(), 17*10*time.Second)
			t.Cleanup(cancelObservation)
			stopOpeningDeadline := context.AfterFunc(ctx(t), cancelObservation)
			observer, err := c.Observe(observationCtx, ref)
			stopOpeningDeadline()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, observer.Close()) })
			var live []events.SessionEvent
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
				startID := ""
				for {
					e, err := observer.Next()
					require.NoError(t, err)
					require.NotEqual(t, "session.error", e.Type())
					if proven && e.Type() == "session.state_update" {
						meta := e.Meta["claude"].(map[string]any)
						var native struct {
							Type  string `json:"type"`
							UUID  string `json:"user_message_uuid"`
							Event struct {
								Type    string `json:"type"`
								Message struct {
									ID string `json:"id"`
								} `json:"message"`
							} `json:"event"`
						}
						require.NoError(t, json.Unmarshal([]byte(meta["nativeRaw"].(string)), &native))
						if native.Event.Type == "message_start" {
							assert.Equal(t, receipt.Submission, native.UUID)
							startID = native.Event.Message.ID
						}
					}
					if e.Type() == "agent.message" || e.Type() == "agent.thinking" || e.Type() == "agent.message_chunk" || e.Type() == "agent.thinking_chunk" {
						if strings.HasPrefix(text, "linkage-") {
							seen++
							meta := e.Meta["claude"].(map[string]any)
							linked := proven && (text != "linkage-stopped-preview" || (e.Type() != "agent.message_chunk" && e.Type() != "agent.thinking_chunk"))
							if linked {
								require.NotEmpty(t, startID)
								assert.Equal(t, startID, meta["responseId"])
								assert.Equal(t, startID+":"+fmt.Sprint(meta["blockIndex"]), e.ItemID)
								assert.Equal(t, receipt.Submission, meta["submission"], "native start/assistant ID equality proves ownership")
								assert.Equal(t, "native_message_id", meta["attribution"])
							} else {
								assert.Equal(t, "unknown", meta["attribution"])
								assert.NotContains(t, meta, "submission")
							}
							var raw map[string]any
							require.NoError(t, json.Unmarshal([]byte(meta["nativeRaw"].(string)), &raw))
							if raw["type"] == "assistant" && proven {
								assert.NotContains(t, raw, "user_message_uuid")
								assert.Equal(t, meta["responseId"], raw["message"].(map[string]any)["id"])
								assert.NotEqual(t, e.ItemID, raw["uuid"])
								assert.Equal(t, fmt.Sprint(meta["blockIndex"]), fmt.Sprint(raw["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["apiBlockIndex"]))
							}
						}
						assert.Equal(t, events.Source{}, e.Source())
						if e.Type() == "agent.message" || e.Type() == "agent.thinking" {
							live = append(live, e)
						}
					}
					if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle {
						break
					}
				}
				if strings.HasPrefix(text, "linkage-") {
					if text == "linkage-saturation" {
						assert.Equal(t, 32, seen, "all native facts survive attribution saturation")
					} else if proven {
						assert.Equal(t, 4, seen, "both previews and complete thinking/text must be associated")
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
