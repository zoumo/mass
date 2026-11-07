package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type ClaudeSuite struct{ suite.Suite }

type messageObservation struct {
	content  []events.ContentBlock
	message  string
	response string
	index    int
	parent   any
}

func messageItemID(kind, responseID string) string {
	if kind == "claude" {
		return responseID + ":0"
	}
	return responseID
}

func TestExplicitEnvironmentLaunchWithoutScenarioConfig(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			path, work := nativeScenario(t, kind, "basic")
			bin := mock
			if kind == "claude" {
				bin = claudeMock
			}
			var observation *codexTurnObservation
			if kind == "codex" {
				observation = newCodexTurnObservation()
				defer observation.releaseAll()
			}
			svc, _, _ := setupWithCodexObservation(t, prototype.AutoApprovePermissions, observation)
			// The executable is still strictly synthetic. Only launch configuration
			// uses the real-runtime shape: scenario is not a required config field.
			_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{
				"executable": bin, "envPolicy": "replace", "env": map[string]any{strings.ToUpper(kind) + "_MOCK_SCENARIO": path},
			}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(ctx(t), "agent")
			require.NoError(t, err)
			r, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: work}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: r.Status.SessionID}
			sub, err := svc.SubscribeEvents(ref)
			require.NoError(t, err)
			defer sub.Close()
			receipt, err := svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			require.NoError(t, err)
			require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
			if kind == "codex" {
				require.Equal(t, "turn-1", receipt.Submission)
				require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
				observation.releaseInitial()
			} else {
				require.Nil(t, receipt.Acceptance.NativeAccepted)
				require.Nil(t, receipt.Acceptance.NativeRecorded)
			}
			for {
				e, err := nextSubscription(sub, ctx(t))
				require.NoError(t, err)
				if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
					if observation != nil {
						work, observed := observation.work.Load(e.EventID)
						require.True(t, observed, "public idle must match an observed Driver event UUID")
						if work == "" {
							// Opening idle has no submitted turn. Keep later native
							// facts gated so it cannot accidentally satisfy history.
							before, queryErr := svc.ListHistoryEvents(ref, store.HistoryQuery{})
							require.NoError(t, queryErr)
							require.Empty(t, before.Events)
							observation.releaseRest()
							continue
						}
						require.Equal(t, receipt.Submission, work, "terminal must belong to the accepted native turn")
						require.Equal(t, "completed", state.StopReason)
					}
					break
				}
			}
			history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.NotEmpty(t, history.Events)
			_, err = svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
		})
	}
}

func nativeScenario(t *testing.T, kind, mode string) (string, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + kind + "-" + mode + ".json")
	require.NoError(t, err)
	var scenario map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&scenario))
	return writeNativeScenario(t, scenario)
}

func writeNativeScenario(t *testing.T, scenario map[string]any) (string, string) {
	t.Helper()
	work := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(work))
		_, err := os.Stat(work)
		require.True(t, os.IsNotExist(err))
	})
	// Only cwd values change. Native request matching remains strict.
	var replace func(any)
	replace = func(v any) {
		switch value := v.(type) {
		case map[string]any:
			for k, v := range value {
				if k == "cwd" && v == "/synthetic" {
					value[k] = work
				} else {
					replace(v)
				}
			}
		case []any:
			for _, v := range value {
				replace(v)
			}
		}
	}
	replace(scenario)
	data, err := json.Marshal(scenario)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path, work
}

func setup(t *testing.T, policy service.InteractionPolicy) (*service.Service, *runtime.Registry, string) {
	t.Helper()
	return setupWithCodexObservation(t, policy, nil)
}

func setupWithCodexObservation(t *testing.T, policy service.InteractionPolicy, observation *codexTurnObservation) (*service.Service, *runtime.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	var codexFactory runtime.Factory = codex.Factory{RecoveryDir: filepath.Join(dir, "private")}
	if observation != nil {
		codexFactory = observedCodexFactory{Factory: codexFactory, observation: observation}
	}
	reg := runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}, "codex": codexFactory})
	svc := service.NewWithPolicy(st, st, reg, policy)
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(ctx(t))); require.NoError(t, st.Close()) })
	return svc, reg, dir
}

func openNative(t *testing.T, svc *service.Service, kind, mode, name string) resource.Session {
	t.Helper()
	path, work := nativeScenario(t, kind, mode)
	bin := mock
	if kind == "claude" {
		bin = claudeMock
	}
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: resource.RuntimeSpec{Type: kind, Config: runtime.Config{"executable": bin, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), name)
	require.NoError(t, err)
	r, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: name, SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	return r
}

func (claudeSuite *ClaudeSuite) TestBothAgentsInteractions() {
	for _, kind := range []string{"claude", "codex"} {
		for _, mode := range []string{"basic", "question", "manual"} {
			claudeSuite.Run(kind+"/"+mode, func() {
				t := claudeSuite.T()
				policy := service.InteractionPolicy(prototype.AutoApprovePermissions)
				scenarioMode := mode
				if mode == "manual" {
					policy = nil
					scenarioMode = "basic"
				}
				svc, _, dir := setup(t, policy)
				r := openNative(t, svc, kind, scenarioMode, "demo")
				require.Equal(t, kind+"-session", r.Status.SessionID)
				ref := resource.SessionRef{SessionID: r.Status.SessionID}
				before, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, err)
				require.Empty(t, before.Events)
				sub, err := svc.SubscribeEvents(ref)
				require.NoError(t, err)
				defer sub.Close()
				receipt, err := svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
				require.NoError(t, err)
				require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
				requests, chunks := 0, 0
				seen := map[string]int{}
				var interaction string
				for {
					e, err := nextSubscription(sub, ctx(t))
					require.NoError(t, err)
					if problem, ok := e.Payload().(events.SessionError); ok {
						t.Fatalf("native session failure: %s %s", problem.Message, problem.Reason)
					}
					seen[e.Type()]++
					if p, ok := e.Payload().(events.InteractionRequested); ok {
						requests++
						interaction = p.Interaction
						require.False(t, e.Persistent())
						if mode == "question" {
							require.Equal(t, "form", p.Request.Kind)
							_, handled := prototype.AutoApprovePermissions(p)
							require.False(t, handled, "question must not be auto-approved")
							var content map[string]any
							if kind == "claude" {
								content = map[string]any{"answers": map[string]any{"Which format?": "json"}}
							} else {
								content = map[string]any{"answers": map[string]any{"format": map[string]any{"answers": []any{"json"}}}}
							}
							accepted, err := svc.AnswerInteraction(ctx(t), ref, p.Interaction, map[string]any{"action": "accept", "content": content})
							require.NoError(t, err)
							require.Equal(t, runtime.Yes(), accepted.Evidence.Sent)
							require.Nil(t, accepted.Evidence.NativeAccepted)
							_, err = svc.AnswerInteraction(ctx(t), ref, p.Interaction, map[string]any{"action": "accept", "content": content})
							require.Error(t, err, "no double answer")
						} else {
							require.Equal(t, "permission", p.Request.Kind)
							if mode == "manual" {
								page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
								require.NoError(t, err)
								for _, event := range page.Events {
									require.NotEqual(t, "agent.tool_result", event.Type(), "no automatic permission policy")
								}
								acceptance, err := svc.AnswerInteraction(ctx(t), ref, p.Interaction, map[string]any{"outcome": "selected", "optionId": "allow-once"})
								require.NoError(t, err)
								require.Equal(t, runtime.Yes(), acceptance.Evidence.Sent)
								require.Nil(t, acceptance.Evidence.NativeAccepted)
							}
						}
					}
					if e.Type() == "agent.message_chunk" {
						chunks++
					}
					if e.Persistent() {
						// Every publication is already readable, with the same UUID and payload.
						reopened, err := file.Open(dir, true)
						require.NoError(t, err)
						page, err := reopened.ListHistory(e.SessionID, store.HistoryQuery{})
						require.NoError(t, err)
						require.NoError(t, reopened.Close())
						a, err := json.Marshal(e)
						require.NoError(t, err)
						var saved events.SessionEvent
						for _, candidate := range page.Events {
							if candidate.EventID == e.EventID {
								saved = candidate
								break
							}
						}
						b, err := json.Marshal(saved)
						require.NoError(t, err)
						require.JSONEq(t, string(a), string(b))
					}
					if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && seen["agent.message"] > 0 {
						break
					}
				}
				require.NotEmpty(t, interaction)
				require.Positive(t, chunks)
				if kind == "codex" && mode != "question" {
					require.Equal(t, 2, requests)
				} else {
					require.Equal(t, 1, requests)
				}
				page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, err)
				if kind == "claude" {
					require.Equal(t, 1, seen["agent.thinking"])
					require.Equal(t, 2, seen["agent.message"])
					for _, e := range page.Events {
						require.NotEqual(t, "delta-only:0", e.ItemID, "delta-only content must not enter history")
						switch p := e.Payload().(type) {
						case events.AgentMessage:
							switch e.ItemID {
							case "msg-8:2":
								require.Equal(t, []events.ContentBlock{events.Text("complete answer")}, p.Content)
							case "msg-8:4":
								require.Equal(t, []events.ContentBlock{events.Text("second block")}, p.Content)
							default:
								t.Fatalf("unexpected agent message identity %q", e.ItemID)
							}
							require.Equal(t, e.ItemID, p.MessageID)
						case events.AgentThinking:
							require.Equal(t, "msg-8:0", e.ItemID)
							require.Equal(t, []events.ContentBlock{events.Text("reason one")}, p.Content)
							require.Equal(t, e.ItemID, p.MessageID)
						case events.ToolResult:
							switch p.ID {
							case "value-0":
								require.Nil(t, p.RawOutput)
								require.Nil(t, p.Content)
								require.Equal(t, "unavailable", p.Output.Availability)
							case "value-1":
								require.NotNil(t, p.RawOutput)
								require.Nil(t, *p.RawOutput)
							case "value-2":
								require.Empty(t, *p.RawOutput)
								require.Equal(t, []events.ContentBlock{events.Text("")}, p.Content)
							case "value-3":
								require.Equal(t, []any{}, *p.RawOutput)
								require.Equal(t, []events.ContentBlock{}, p.Content)
							case "value-4":
								var output []struct {
									Value struct {
										Large json.Number `json:"large"`
									} `json:"value"`
								}
								raw, err := json.Marshal(p.RawOutput)
								require.NoError(t, err)
								decoder := json.NewDecoder(bytes.NewReader(raw))
								decoder.UseNumber()
								require.NoError(t, decoder.Decode(&output))
								require.Len(t, output, 1)
								require.Equal(t, json.Number("9007199254740993"), output[0].Value.Large)
							}
							if p.ID != "tool-1" || mode != "question" {
								require.Equal(t, "failed", p.Status, "approval does not make execution successful")
							}
						}
					}
				}
				require.NoError(t, svc.Shutdown(ctx(t)))
				t.Log("PASS synthetic no-input identity, separate input, original handles, full facts and durable readback; question is explicit; cleanup follows")
			})
		}
	}
}

func (claudeSuite *ClaudeSuite) TestClaudeFailedOpenIsolation() {
	t := claudeSuite.T()
	svc, reg, _ := setup(t, prototype.AutoApprovePermissions)
	healthy := openNative(t, svc, "claude", "question", "healthy")
	// Same native ID on a second process: reject only that new process.
	_, err := svc.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "duplicate"}, Spec: resource.SessionSpec{RuntimeRef: "healthy", SessionSpec: runtime.SessionSpec{Cwd: t.TempDir()}}})
	require.Error(t, err)
	path, work := nativeScenario(t, "claude", "missing-disconnect")
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "missing"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "missing")
	require.NoError(t, err)
	wait, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = svc.CreateSession(wait, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "missing"}, Spec: resource.SessionSpec{RuntimeRef: "missing", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = svc.GetSession(resource.SessionRef{Namespace: "default", Name: "missing"})
	require.ErrorIs(t, err, store.ErrNotFound)
	d, err := reg.Get("healthy")
	require.NoError(t, err)
	// Unsupported SDK options reject without starting a replacement process.
	_, err = d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "bad"}, runtime.SessionSpec{Cwd: work, SystemPrompt: "unsupported"})
	require.ErrorContains(t, err, "does not support")
	ref := resource.SessionRef{SessionID: healthy.Status.SessionID}
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	defer sub.Close()
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	for {
		e, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			_, err = svc.AnswerInteraction(ctx(t), ref, p.Interaction, map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"Which format?": "json"}}})
			require.NoError(t, err)
		}
		if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle {
			break
		}
	}
	require.NoError(t, svc.Shutdown(ctx(t)))
	t.Log("PASS duplicate and missing init cleanup leave healthy Session usable; no hidden prompt or fabricated identity")
}

func (claudeSuite *ClaudeSuite) TestClaudeLiveBlockItemIdentity() {
	t := claudeSuite.T()
	svc, _, _ := setup(t, prototype.AutoApprovePermissions)
	r := openNative(t, svc, "claude", "block-item-identity", "blocks")
	ref := resource.SessionRef{SessionID: r.Status.SessionID}
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	defer sub.Close()
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)

	chunks := map[string][]messageObservation{}
	full := map[string]messageObservation{}
	fullCount := map[string]int{}
	tools := map[string][]string{}
	for {
		e, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		require.NotEqual(t, "session.error", e.Type(), "%+v", e.Payload())
		switch p := e.Payload().(type) {
		case events.ThinkingChunk:
			chunks[e.ItemID] = append(chunks[e.ItemID], observedMessage(t, e, p.Content, p.MessageID))
		case events.MessageChunk:
			chunks[e.ItemID] = append(chunks[e.ItemID], observedMessage(t, e, p.Content, p.MessageID))
		case events.AgentThinking:
			full[e.ItemID] = observedMessage(t, e, p.Content, p.MessageID)
			fullCount[e.ItemID]++
		case events.AgentMessage:
			full[e.ItemID] = observedMessage(t, e, p.Content, p.MessageID)
			fullCount[e.ItemID]++
		case events.ToolCall:
			tools[e.ItemID] = append(tools[e.ItemID], e.EventID)
		case events.ToolResult:
			tools[e.ItemID] = append(tools[e.ItemID], e.EventID)
		case events.StateUpdate:
			if p.State != nil && *p.State == events.Idle {
				goto observed
			}
		}
	}

observed:
	require.Equal(t, []messageObservation{{
		content:  []events.ContentBlock{events.Text("think ")},
		message:  "msg-block:0",
		response: "msg-block",
		index:    0,
		parent:   nil,
	}}, chunks["msg-block:0"])
	require.Equal(t, []messageObservation{{
		content:  []events.ContentBlock{events.Text("hello ")},
		message:  "msg-block:3",
		response: "msg-block",
		index:    3,
		parent:   nil,
	}}, chunks["msg-block:3"])
	require.Equal(t, []messageObservation{{
		content:  []events.ContentBlock{events.Text("\n\n")},
		message:  "msg-block:6",
		response: "msg-block",
		index:    6,
		parent:   nil,
	}}, chunks["msg-block:6"])
	require.Equal(t, []messageObservation{
		{
			content:  []events.ContentBlock{events.Text("open ")},
			message:  "msg-block:8",
			response: "msg-block",
			index:    8,
			parent:   nil,
		},
		{
			content:  []events.ContentBlock{events.Text("still-open")},
			message:  "msg-block:8",
			response: "msg-block",
			index:    8,
			parent:   nil,
		},
	}, chunks["msg-block:8"])
	require.Equal(t, []messageObservation{{
		content:  []events.ContentBlock{events.Text("child thought preview")},
		message:  "parent-tool:msg-block:0",
		response: "msg-block",
		index:    0,
		parent:   "parent-tool",
	}}, chunks["parent-tool:msg-block:0"])

	require.Equal(t, map[string]messageObservation{
		"msg-block:0": {
			content:  []events.ContentBlock{events.Text("think complete")},
			message:  "msg-block:0",
			response: "msg-block",
			index:    0,
			parent:   nil,
		},
		"msg-block:3": {
			content:  []events.ContentBlock{events.Text("hello complete")},
			message:  "msg-block:3",
			response: "msg-block",
			index:    3,
			parent:   nil,
		},
		"msg-block:6": {
			content:  []events.ContentBlock{events.Text("\n\n")},
			message:  "msg-block:6",
			response: "msg-block",
			index:    6,
			parent:   nil,
		},
		"parent-tool:msg-block:0": {
			content:  []events.ContentBlock{events.Text("child complete")},
			message:  "parent-tool:msg-block:0",
			response: "msg-block",
			index:    0,
			parent:   "parent-tool",
		},
	}, full)
	for item, count := range fullCount {
		require.Equal(t, 1, count, "duplicate full for %s", item)
	}
	for _, item := range []string{"msg-block:1", "msg-block:4", "msg-block:5", "msg-block:7", "msg-block:9", "msg-block:10", "msg-block:11", "msg-invalid:0"} {
		require.NotContains(t, full, item)
	}
	for _, item := range []string{"msg-block:2", "msg-block:5", "msg-block:7", "msg-block:9", "msg-block:10", "msg-block:11", "msg-invalid:0", "unstarted-parent:msg-block:0"} {
		require.NotContains(t, chunks, item)
	}
	require.NotContains(t, tools, "parent-tool")
	require.Len(t, tools["tool-shared"], 2)
	require.NotEqual(t, tools["tool-shared"][0], tools["tool-shared"][1])

	history, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	persisted := map[string]events.MessagePayload{}
	persistedCount := map[string]int{}
	persistedTools := map[string]int{}
	for _, e := range history.Events {
		switch p := e.Payload().(type) {
		case events.AgentThinking:
			persisted[e.ItemID] = events.MessagePayload(p)
			persistedCount[e.ItemID]++
		case events.AgentMessage:
			persisted[e.ItemID] = events.MessagePayload(p)
			persistedCount[e.ItemID]++
		case events.ToolCall:
			persistedTools[e.ItemID]++
		case events.ToolResult:
			persistedTools[e.ItemID]++
		default:
			t.Fatalf("unexpected persisted event %s for item %q", e.Type(), e.ItemID)
		}
	}
	require.Len(t, persisted, len(full))
	for item, observation := range full {
		require.Equal(t, 1, persistedCount[item], "duplicate persisted full for %s", item)
		require.Equal(t, observation.content, persisted[item].Content)
		require.Equal(t, item, persisted[item].MessageID)
	}
	require.Equal(t, 2, persistedTools["tool-shared"])
	for _, forbidden := range []string{"aborted answer", "ambiguous", "guess one", "guess two", "visible after signature", "invalid coordinate", "invalid null coordinate", "invalid envelope coordinate", "missing child response", "conflicting preview", "conflicting complete"} {
		for _, payload := range persisted {
			for _, block := range payload.Content {
				require.NotEqual(t, forbidden, block.Text)
			}
		}
	}
	require.NoError(t, svc.Shutdown(ctx(t)))
	t.Log("PASS block item identity, lane separation, closure suppression, empty filtering, ambiguous fail-close and tool identity through SSE and history")
}

const claudeLiveBlockStateCapacity = 4096

func TestClaudeLiveBlockStateCapacityFailsClosed(t *testing.T) {
	const sessionID = "capacity-session"
	frames := capacityFrames(sessionID)
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-capacity", "role": "assistant", "content": []any{}}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "preview"}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "assistant", "session_id": sessionID, "uuid": "capacity-complete",
		"message": map[string]any{"id": "msg-capacity", "role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "completed", "apiBlockIndex": 0},
		}},
	})
	for index := 1; index < claudeLiveBlockStateCapacity; index++ {
		frames = appendServerFrame(frames, map[string]any{
			"type": "stream_event", "session_id": sessionID,
			"event": map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}},
		})
	}
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "reopened"}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_start", "index": claudeLiveBlockStateCapacity, "content_block": map[string]any{"type": "text", "text": ""}},
	})
	sub := runObservationFailureScenario(t, sessionID, frames)
	defer sub.Close()

	full := 0
	reopened := false
	requireClaudeObservationFailure(t, sub, func(event events.SessionEvent) {
		switch payload := event.Payload().(type) {
		case events.AgentMessage:
			if event.ItemID == "msg-capacity:0" {
				full++
			}
		case events.MessageChunk:
			if event.ItemID == "msg-capacity:0" {
				for _, block := range payload.Content {
					reopened = reopened || block.Text == "reopened"
				}
			}
		}
	})
	require.Equal(t, 1, full)
	require.False(t, reopened, "an evicted completed item must never be reopened by a late chunk")
}

func TestClaudeLiveLaneStateCapacityFailsClosed(t *testing.T) {
	const sessionID = "lane-capacity-session"
	frames := capacityFrames(sessionID)
	for index := 0; index <= claudeLiveBlockStateCapacity; index++ {
		frames = appendServerFrame(frames, map[string]any{
			"type": "stream_event", "session_id": sessionID, "parent_tool_use_id": "child-" + strconv.Itoa(index),
			"event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-lane-" + strconv.Itoa(index), "role": "assistant", "content": []any{}}},
		})
	}
	sub := runObservationFailureScenario(t, sessionID, frames)
	defer sub.Close()

	requireClaudeObservationFailure(t, sub)
}

func TestClaudeLiveAssistantFrameCapacityFailsClosed(t *testing.T) {
	const sessionID = "frame-capacity-session"
	frames := capacityFrames(sessionID)
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-frame", "role": "assistant", "content": []any{}}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "assistant", "session_id": sessionID, "uuid": "frame-0",
		"message": map[string]any{"id": "msg-frame", "role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "first-full", "apiBlockIndex": 0},
		}},
	})
	for index := 1; index < claudeLiveBlockStateCapacity; index++ {
		frames = appendServerFrame(frames, map[string]any{
			"type": "assistant", "session_id": sessionID, "uuid": "frame-" + strconv.Itoa(index),
			"message": map[string]any{"id": "msg-frame", "role": "assistant", "content": []any{}},
		})
	}
	frames = appendServerFrame(frames, map[string]any{
		"type": "assistant", "session_id": sessionID, "uuid": "frame-0",
		"message": map[string]any{"id": "msg-frame", "role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "duplicate-full", "apiBlockIndex": 0},
		}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "after-duplicate"}},
	})
	frames = appendServerFrame(frames, map[string]any{
		"type": "assistant", "session_id": sessionID, "uuid": "frame-overflow",
		"message": map[string]any{"id": "msg-frame", "role": "assistant", "content": []any{}},
	})
	sub := runObservationFailureScenario(t, sessionID, frames)
	defer sub.Close()

	duplicateSurvived := false
	fullCount := 0
	duplicateFull := false
	requireClaudeObservationFailure(t, sub, func(event events.SessionEvent) {
		switch payload := event.Payload().(type) {
		case events.AgentMessage:
			if event.ItemID != "msg-frame:0" {
				return
			}
			fullCount++
			for _, block := range payload.Content {
				duplicateFull = duplicateFull || block.Text == "duplicate-full"
			}
		case events.MessageChunk:
			if event.ItemID != "msg-frame:1" {
				return
			}
			for _, block := range payload.Content {
				duplicateSurvived = duplicateSurvived || block.Text == "after-duplicate"
			}
		}
	})
	require.True(t, duplicateSurvived, "a duplicate frame must stay suppressed at capacity without terminating observation")
	require.Equal(t, 1, fullCount, "a duplicate frame must not emit a second full")
	require.False(t, duplicateFull, "the duplicate full content must be ignored")
}

func TestClaudeLiveToolBlockIdentityConflictFailsClosed(t *testing.T) {
	const sessionID = "tool-identity-session"
	frames := capacityFrames(sessionID)
	frames = appendServerFrame(frames, map[string]any{
		"type": "stream_event", "session_id": sessionID,
		"event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-tool", "role": "assistant", "content": []any{}}},
	})
	for _, id := range []string{"tool-first", "tool-second"} {
		frames = appendServerFrame(frames, map[string]any{
			"type": "stream_event", "session_id": sessionID,
			"event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{}}},
		})
	}
	sub := runObservationFailureScenario(t, sessionID, frames)
	defer sub.Close()

	requireClaudeObservationFailure(t, sub)
}

func capacityFrames(sessionID string) []any {
	return []any{
		scenarioFrame(1, "client", map[string]any{
			"type":       "control_request",
			"request_id": "init",
			"request":    map[string]any{"subtype": "initialize", "hooks": nil, "forwardSubagentText": true},
		}),
		scenarioFrame(2, "server", map[string]any{
			"type":     "control_response",
			"response": map[string]any{"subtype": "success", "request_id": "init", "response": map[string]any{}},
		}),
		scenarioFrame(3, "server", map[string]any{
			"type": "system", "subtype": "init", "session_id": sessionID, "cwd": "/synthetic", "model": "synthetic", "tools": []any{"Bash"},
		}),
		scenarioFrame(4, "client", map[string]any{
			"type": "user", "message": map[string]any{"role": "user", "content": "hello"}, "parent_tool_use_id": nil, "session_id": "",
		}),
	}
}

func appendServerFrame(frames []any, data map[string]any) []any {
	return append(frames, scenarioFrame(len(frames)+1, "server", data))
}

func runObservationFailureScenario(t *testing.T, name string, frames []any) *events.Subscription {
	t.Helper()
	path, work := writeNativeScenario(t, map[string]any{"version": 1, "provenance": "synthetic", "frames": frames})
	svc := setupExpectedObservationFailure(t)
	scenarioCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(scenarioCtx, name)
	require.NoError(t, err)
	session, err := svc.CreateSession(scenarioCtx, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: name, SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: session.Status.SessionID}
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	_, err = svc.SendPrompt(scenarioCtx, ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	return sub
}

func setupExpectedObservationFailure(t *testing.T) *service.Service {
	t.Helper()
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	reg := runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}})
	svc := service.NewWithPolicy(st, st, reg, prototype.AutoApprovePermissions)
	t.Cleanup(func() {
		require.ErrorContains(t, svc.Shutdown(ctx(t)), "observation terminated")
		require.NoError(t, st.Close())
	})
	return svc
}

func requireClaudeObservationFailure(t *testing.T, sub *events.Subscription, observe ...func(events.SessionEvent)) {
	t.Helper()
	for {
		event, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		for _, observe := range observe {
			observe(event)
		}
		switch payload := event.Payload().(type) {
		case events.SessionError:
			require.Equal(t, "native_observation_failed", payload.Reason)
			return
		case events.StateUpdate:
			if payload.State != nil && *payload.State == events.Idle {
				t.Fatal("capacity exhaustion reached idle without terminating observation")
			}
		}
	}
}

func scenarioFrame(ordinal int, direction string, data any) map[string]any {
	return map[string]any{"ordinal": ordinal, "channel": "protocol", "direction": direction, "data": data}
}

func observedMessage(t *testing.T, e events.SessionEvent, content []events.ContentBlock, message string) messageObservation {
	t.Helper()
	var observation messageObservation
	observation.content = content
	observation.message = message
	meta := decodeClaudeMeta(t, e)
	observation.response = meta.ResponseID
	observation.index = meta.BlockIndex
	if meta.ParentToolUseID != nil {
		observation.parent = *meta.ParentToolUseID
	}
	return observation
}

type decodedClaudeMeta struct {
	ResponseID      string  `json:"responseId"`
	BlockIndex      int     `json:"blockIndex"`
	ParentToolUseID *string `json:"parentToolUseId"`
	Submission      string  `json:"submission"`
	Attribution     string  `json:"attribution"`
}

func decodeClaudeMeta(t *testing.T, event events.SessionEvent) decodedClaudeMeta {
	t.Helper()
	raw, err := json.Marshal(event.Meta["claude"])
	require.NoError(t, err)
	var meta decodedClaudeMeta
	require.NoError(t, json.Unmarshal(raw, &meta))
	return meta
}

func TestClaudeSuite(t *testing.T) { suite.Run(t, new(ClaudeSuite)) }
