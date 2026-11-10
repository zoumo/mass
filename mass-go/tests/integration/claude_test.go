package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
				defer observation.releaseFacts()
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
				before, queryErr := svc.ListHistoryEvents(ref, store.HistoryQuery{})
				require.NoError(t, queryErr)
				require.Empty(t, before.Events)
				observation.releaseFacts()
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
	normalizeCodexResumeInput(scenario)
	normalizeCodexTurnInput(scenario)
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
	expandCodexIdentityReads(scenario)
	data, err := json.Marshal(scenario)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path, work
}

// normalizeCodexResumeInput reflects the SDK provider's explicit workspace on
// resume. The placeholder is replaced with the test workspace immediately
// afterwards, while the strict peer still verifies the complete request.
func normalizeCodexResumeInput(scenario map[string]any) {
	frames, _ := scenario["frames"].([]any)
	for _, value := range frames {
		frame, _ := value.(map[string]any)
		if direction, _ := frame["direction"].(string); direction != "c2s" {
			continue
		}
		wire, _ := frame["wire"].(map[string]any)
		if method, _ := wire["method"].(string); method != "thread/resume" {
			continue
		}
		params, _ := wire["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
			wire["params"] = params
		}
		if _, exists := params["cwd"]; !exists {
			params["cwd"] = "/synthetic"
		}
	}
}

// normalizeCodexTurnInput removes the empty annotation array emitted by the
// pre-SDK adapter. Non-empty annotations remain part of the strict contract.
func normalizeCodexTurnInput(scenario map[string]any) {
	frames, _ := scenario["frames"].([]any)
	for _, value := range frames {
		frame, _ := value.(map[string]any)
		if direction, _ := frame["direction"].(string); direction != "c2s" {
			continue
		}
		wire, _ := frame["wire"].(map[string]any)
		if method, _ := wire["method"].(string); method != "turn/start" {
			continue
		}
		params, _ := wire["params"].(map[string]any)
		input, _ := params["input"].([]any)
		for _, value := range input {
			block, _ := value.(map[string]any)
			if elements, ok := block["text_elements"].([]any); ok && len(elements) == 0 {
				delete(block, "text_elements")
			}
		}
	}
}

// expandCodexIdentityReads adds the provider's metadata-only identity probe
// after every authored Codex open. The read response is derived from the
// preceding start or resume response, but remains explicit traffic consumed by
// the strict scenario peer.
func expandCodexIdentityReads(scenario map[string]any) {
	frames, ok := scenario["frames"].([]any)
	if !ok {
		return
	}
	expanded := make([]any, 0, len(frames)+4)
	openRequests := map[string]struct{}{}
	for index := range frames {
		frame, ok := frames[index].(map[string]any)
		if !ok {
			expanded = append(expanded, frames[index])
			continue
		}
		expanded = append(expanded, frame)
		if requestID, open := codexOpenRequestID(frame); open {
			if requestID != "" {
				openRequests[requestID] = struct{}{}
			}
			continue
		}
		responseID, _ := frameWireID(frame)
		if _, open := openRequests[responseID]; !open {
			continue
		}
		delete(openRequests, responseID)
		thread, ok := codexThread(frame)
		if !ok {
			continue
		}
		if next, ok := frameWireMethodAt(frames, index+1); ok && next == "thread/read" {
			continue
		}
		id := "sdk-thread-read-" + strconv.Itoa(index)
		expanded = append(expanded,
			map[string]any{
				"channel":   "protocol",
				"direction": "c2s",
				"epoch":     1,
				"wire": map[string]any{
					"id":     id,
					"method": "thread/read",
					"params": map[string]any{
						"threadId":     thread["id"],
						"includeTurns": false,
					},
				},
			},
			map[string]any{
				"channel":   "protocol",
				"direction": "s2c",
				"epoch":     1,
				"wire": map[string]any{
					"id":     id,
					"result": map[string]any{"thread": thread},
				},
			},
		)
	}
	for index, value := range expanded {
		if frame, ok := value.(map[string]any); ok {
			frame["ordinal"] = index + 1
		}
	}
	scenario["frames"] = expanded
}

func codexOpenRequestID(frame map[string]any) (string, bool) {
	direction, _ := frame["direction"].(string)
	if direction != "c2s" {
		return "", false
	}
	wire, _ := frame["wire"].(map[string]any)
	method, _ := wire["method"].(string)
	if method != "thread/start" && method != "thread/resume" {
		return "", false
	}
	return fmt.Sprint(wire["id"]), true
}

func frameWireID(frame map[string]any) (string, bool) {
	direction, _ := frame["direction"].(string)
	if direction != "s2c" {
		return "", false
	}
	wire, _ := frame["wire"].(map[string]any)
	if wire == nil || wire["id"] == nil {
		return "", false
	}
	return fmt.Sprint(wire["id"]), true
}

func codexThread(frame map[string]any) (map[string]any, bool) {
	direction, _ := frame["direction"].(string)
	if direction != "s2c" {
		return nil, false
	}
	wire, _ := frame["wire"].(map[string]any)
	result, _ := wire["result"].(map[string]any)
	thread, ok := result["thread"].(map[string]any)
	if !ok {
		return nil, false
	}
	id, _ := thread["id"].(string)
	return thread, id != ""
}

func frameWireMethodAt(frames []any, index int) (string, bool) {
	if index < 0 || index >= len(frames) {
		return "", false
	}
	frame, ok := frames[index].(map[string]any)
	if !ok {
		return "", false
	}
	wire, _ := frame["wire"].(map[string]any)
	method, ok := wire["method"].(string)
	return method, ok
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
						interaction = string(p.Interaction)
						require.False(t, e.Persistent())
						if mode == "question" {
							require.Equal(t, events.InteractionForm, p.Request.Kind)
							_, handled := prototype.AutoApprovePermissions(p)
							require.False(t, handled, "question must not be auto-approved")
							var content map[string]any
							if kind == "claude" {
								content = map[string]any{"answers": map[string]any{"Which format?": "json"}}
							} else {
								content = map[string]any{"answers": map[string]any{"format": map[string]any{"answers": []any{"json"}}}}
							}
							accepted, err := svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": content})
							require.NoError(t, err)
							require.Equal(t, runtime.Yes(), accepted.Evidence.Sent)
							require.Nil(t, accepted.Evidence.NativeAccepted)
							_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": content})
							require.Error(t, err, "no double answer")
						} else {
							require.Equal(t, events.InteractionPermission, p.Request.Kind)
							if mode == "manual" {
								page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
								require.NoError(t, err)
								for _, event := range page.Events {
									require.NotEqual(t, "agent.tool_result", event.Type(), "no automatic permission policy")
								}
								acceptance, err := svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "allow-once"})
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
								require.Nil(t, p.RawOutput, "SDK JSON codec maps explicit null to absent raw output")
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
	missingStore, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	missing := service.NewWithPolicy(missingStore, missingStore, runtime.NewRegistry(map[string]runtime.Factory{
		"claude": claude.Factory{RecoveryDir: t.TempDir()},
	}), prototype.AutoApprovePermissions)
	t.Cleanup(func() {
		if shutdownErr := missing.Shutdown(ctx(t)); shutdownErr != nil {
			require.ErrorContains(t, shutdownErr, "original opening or stream responsibility remains")
		}
		require.NoError(t, missingStore.Close())
	})
	_, err = missing.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "missing"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": claudeMock, "scenario": path}}})
	require.NoError(t, err)
	_, err = missing.StartRuntime(ctx(t), "missing")
	require.NoError(t, err)
	wait, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = missing.CreateSession(wait, resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "missing"}, Spec: resource.SessionSpec{RuntimeRef: "missing", SessionSpec: runtime.SessionSpec{Cwd: work}}})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = missing.GetSession(resource.SessionRef{Namespace: "default", Name: "missing"})
	require.ErrorIs(t, err, store.ErrNotFound)
	d, err := reg.Get("healthy")
	require.NoError(t, err)
	// Unsupported SDK options reject without starting a replacement process.
	_, err = d.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: "bad"}, runtime.SessionSpec{Cwd: work, SystemPrompt: "unsupported"})
	var rejected *runtime.OperationError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, "unsupported", rejected.Code)
	require.Equal(t, "system_prompt_or_additional_dirs_unsupported", rejected.Reason)
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
			_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"Which format?": "json"}}})
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
	gaps := map[string]int{}
	for {
		e, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		switch p := e.Payload().(type) {
		case events.SessionError:
			require.False(t, e.Persistent())
			gaps[p.Reason]++

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
	require.Equal(t, map[string]int{
		"assistant_aborted":                   1,
		"assistant_block_content_invalid":     3,
		"assistant_block_coordinate_missing":  5,
		"assistant_block_type_conflict":       1,
		"assistant_response_identity_missing": 1,
		"stream_block_type_conflict":          1,
	}, gaps)
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
		"msg-block:12": {
			content: []events.ContentBlock{events.Text("visible after signature")},
			message: "msg-block:12", response: "msg-block", index: 12,
		},
		"msg-invalid:0": {
			content: []events.ContentBlock{events.Text("invalid envelope coordinate")},
			message: "msg-invalid:0", response: "msg-invalid", index: 0,
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
	for _, item := range []string{"msg-block:1", "msg-block:4", "msg-block:5", "msg-block:7", "msg-block:9", "msg-block:10", "msg-block:11"} {
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
	for _, forbidden := range []string{"aborted answer", "ambiguous", "guess one", "guess two", "invalid coordinate", "invalid null coordinate", "missing child response", "conflicting preview", "conflicting complete"} {
		for _, payload := range persisted {
			for _, block := range payload.Content {
				require.NotEqual(t, forbidden, events.TextOf(block))
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
	sub := runClaudeDriverScenario(t, sessionID, frames)
	full := 0
	reopened := false
	requireClaudeObservationFailure(t, sub, "live_block_capacity", func(event events.SessionEvent) {
		switch payload := event.Payload().(type) {
		case events.AgentMessage:
			if event.ItemID == "msg-capacity:0" {
				full++
			}
		case events.MessageChunk:
			if event.ItemID == "msg-capacity:0" {
				for _, block := range payload.Content {
					reopened = reopened || events.TextOf(block) == "reopened"
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
	sub := runClaudeDriverScenario(t, sessionID, frames)
	requireClaudeObservationFailure(t, sub, "live_block_lane_capacity")
}

func TestClaudeLiveAssistantFramesSuppressConflictingCompletedContent(t *testing.T) {
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
	frames = appendServerFrame(frames, map[string]any{
		"type": "system", "subtype": "session_state_changed", "session_id": sessionID, "state": "idle",
	})
	sub := runClaudeDriverScenario(t, sessionID, frames)
	duplicateSurvived := false
	fullCount := 0
	duplicateFull := false
	observe := func(event events.SessionEvent) {
		switch payload := event.Payload().(type) {
		case events.AgentMessage:
			if event.ItemID != "msg-frame:0" {
				return
			}
			fullCount++
			for _, block := range payload.Content {
				duplicateFull = duplicateFull || events.TextOf(block) == "duplicate-full"
			}
		case events.MessageChunk:
			if event.ItemID != "msg-frame:1" {
				return
			}
			for _, block := range payload.Content {
				duplicateSurvived = duplicateSurvived || events.TextOf(block) == "after-duplicate"
			}
		}
	}
	diagnostics := 0
	read, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		event, err := sub.Next(read)
		require.NoError(t, err)
		observe(event)
		if problem, ok := event.Payload().(events.SessionError); ok {
			require.Equal(t, "assistant_block_reopened", problem.Reason)
			diagnostics++
		}
		if state, ok := event.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
			break
		}
	}
	require.Equal(t, 1, diagnostics)
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
	sub := runClaudeDriverScenario(t, sessionID, frames)
	requireClaudeObservationFailure(t, sub, "conflicting_tool_block_identity")
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
			"type": "system", "subtype": "init", "claude_code_version": "2.1.258", "session_id": sessionID, "cwd": "/synthetic", "model": "synthetic", "tools": []any{"Bash"},
		}),
		scenarioFrame(4, "client", map[string]any{
			"type": "user", "message": map[string]any{"role": "user", "content": "hello"}, "parent_tool_use_id": nil, "session_id": "", "priority": "next", "shouldQuery": true,
		}),
	}
}

func appendServerFrame(frames []any, data map[string]any) []any {
	return append(frames, scenarioFrame(len(frames)+1, "server", data))
}

func runClaudeDriverScenario(t *testing.T, name string, frames []any) events.Stream {
	t.Helper()
	path, work := writeNativeScenario(t, map[string]any{"version": 1, "provenance": "synthetic", "frames": frames})
	driver, err := (claude.Factory{RecoveryDir: t.TempDir()}).New(name, runtime.Config{"executable": claudeMock, "scenario": path})
	require.NoError(t, err)
	stream, err := driver.Events()
	require.NoError(t, err)
	_, err = driver.Start(ctx(t))
	require.NoError(t, err)
	opened, err := driver.OpenSession(ctx(t), runtime.Address{Namespace: "default", SessionName: name}, runtime.SessionSpec{Cwd: work})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for {
				if _, receiveErr := stream.Next(cleanup); receiveErr != nil {
					return
				}
			}
		}()
		closeErr := driver.CloseSession(cleanup, opened.SessionID)
		var stopErr error
		if closeErr == nil {
			stopErr = driver.Stop(cleanup)
		}
		cancel()
		<-drained
		require.NotErrorIs(t, closeErr, context.DeadlineExceeded)
		require.NotErrorIs(t, closeErr, context.Canceled)
		require.NoError(t, stopErr)
	})
	_, err = driver.SendPrompt(ctx(t), opened.SessionID, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	return stream
}

func requireClaudeObservationFailure(t *testing.T, stream events.Stream, reason string, observe ...func(events.SessionEvent)) {
	t.Helper()
	read, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		event, err := stream.Next(read)
		if err != nil {
			require.NotErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, reason)
			return
		}
		for _, observe := range observe {
			observe(event)
		}
		if payload, ok := event.Payload().(events.StateUpdate); ok && payload.State != nil && *payload.State == events.Idle {
			t.Fatal("native observation failure reached idle")
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
