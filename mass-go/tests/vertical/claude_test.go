package vertical_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func nativeScenario(t *testing.T, kind, mode string) (string, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + kind + "-" + mode + ".json")
	require.NoError(t, err)
	var scenario map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&scenario))
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
	data, err = json.Marshal(scenario)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path, work
}

func setup(t *testing.T, policy service.InteractionPolicy) (*service.Service, *runtime.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	reg := runtime.NewRegistry(map[string]runtime.Factory{"claude": claude.Factory{RecoveryDir: filepath.Join(dir, "private")}, "codex": codex.Factory{RecoveryDir: filepath.Join(dir, "private")}})
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
					e, err := sub.Next(ctx(t))
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
								content = map[string]any{"answers": map[string]any{"format": "json"}}
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
					for _, e := range page.Events {
						require.NotEqual(t, "delta-only", e.ItemID, "delta-only content must not enter history")
						switch p := e.Payload().(type) {
						case events.AgentMessage:
							require.Equal(t, []events.ContentBlock{events.Text("complete answer"), events.Text(""), events.Text("second block")}, p.Content)
							require.Equal(t, "msg-8", e.ItemID)
						case events.AgentThinking:
							require.Equal(t, []events.ContentBlock{events.Text("reason one"), events.Text("")}, p.Content)
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
								require.Equal(t, json.Number("9007199254740993"), (*p.RawOutput).([]any)[0].(map[string]any)["value"].(map[string]any)["large"])
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
		e, err := sub.Next(ctx(t))
		require.NoError(t, err)
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			_, err = svc.AnswerInteraction(ctx(t), ref, p.Interaction, map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"format": "json"}}})
			require.NoError(t, err)
		}
		if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle {
			break
		}
	}
	require.NoError(t, svc.Shutdown(ctx(t)))
	t.Log("PASS duplicate and missing init cleanup leave healthy Session usable; no hidden prompt or fabricated identity")
}

func TestClaudeSuite(t *testing.T) { suite.Run(t, new(ClaudeSuite)) }
