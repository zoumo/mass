package claude_test

import (
	"encoding/json"
	"fmt"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestAgentsRunsAndBackground() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"root","claude_code_version":"2.1.258","tools":["Agent","Task"]}`,
		`server protocol {"type":"assistant","session_id":"root","message":{"content":[{"type":"tool_use","id":"spawn","name":"Agent","input":{}}]}}`,
		`server protocol {"type":"system","subtype":"task_started","session_id":"root","task_id":"task-A","tool_use_id":"spawn","task_type":"local_agent","run_id":"001","is_backgrounded":true}`,
		`server protocol {"type":"system","subtype":"task_started","session_id":"root","task_id":"task-A","task_type":"local_agent","run_id":"002","is_backgrounded":true}`,
		`server protocol {"type":"system","subtype":"task_notification","session_id":"root","task_id":"task-A","run_id":"001","status":"completed"}`,
		`server protocol {"type":"system","subtype":"task_progress","session_id":"root","task_id":"task-A","usage":{"output_tokens":9007199254740993}}`,
		`server protocol {"type":"result","session_id":"root","agent_id":"agent-X","parent_tool_use_id":"spawn","subtype":"success","usage":{"output_tokens":3}}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root","tasks":[{"task_id":"task-B","run_id":"B","status":"running"}]}`,
		`server protocol {"type":"result","session_id":"root","subtype":"success","usage":{"output_tokens":7}}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root"}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root","tasks":[]}`,
		`server protocol {"type":"sentinel","session_id":"root"}`)
	_, session, ctx := s.open(frames)
	associated, oldRun, unknownRun, childResult, rootResult, snapshots, usages := false, false, false, false, false, 0, 0
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "sentinel/" {
			break
		}
		data, err := json.Marshal(e)
		s.Require().NoError(err)
		var decoded rt.Event
		s.Require().NoError(json.Unmarshal(data, &decoded))
		s.Equal(e, decoded)
		s.Equal("root", e.SessionID)
		s.NotEqual(rt.AgentCreated, e.Kind)
		if e.Association != nil {
			associated = true
			s.NotEmpty(e.Subject.AssociationRef)
			s.Equal("sdk_attachment", e.Subject.AssociationScope)
		}
		if e.AgentState != nil && e.Subject.RunID == "001" && e.Subject.Stale != nil && *e.Subject.Stale {
			oldRun = true
			s.True(e.AgentState.Terminal)
			s.Empty(e.OperationRef)
		}
		if e.Task != nil && e.Task.TaskRef == "task-A" && e.Subject.RunID == "" {
			unknownRun = true
			s.Empty(e.OperationRef)
		}
		if e.AgentState != nil && e.Subject.AgentID == "agent-X" {
			childResult = true
			s.Empty(e.OperationRef)
		}
		if e.Kind == rt.TurnCompleted {
			rootResult = true
		}
		if e.TaskSnapshot != nil {
			snapshots++
			if snapshots == 1 {
				s.Len(e.TaskSnapshot.Tasks, 1)
			} else {
				s.Empty(e.TaskSnapshot.Tasks)
			}
		}
		if e.Usage != nil {
			if e.Usage.AccountingScope == "task" {
				s.Equal("9007199254740993", string(e.Usage.Counters["output_tokens"]))
			}
			usages++
			s.Equal("unknown", e.Usage.ChildInclusion)
			s.Equal("unknown", e.Usage.Mode)
			if e.Subject != nil {
				s.Empty(e.OperationRef)
			}
		}
	}
	s.True(associated)
	s.True(oldRun)
	s.True(unknownRun)
	s.True(childResult)
	s.True(rootResult)
	s.Equal(2, snapshots)
	s.GreaterOrEqual(usages, 3)
	s.Empty(session.Snapshot().Background.Tasks)
	s.Equal("root", session.Key().SessionID)
}

func (s *runtimeSuite) TestConservativeCreation() {
	for _, tc := range []struct {
		name, input, replay string
		created             int
	}{
		{"live", `{"description":"inspect","prompt":"inspect"}`, "", 1},
		{"resumed", `{"description":"inspect","prompt":"inspect","resume":"prior"}`, "", 0},
		{"replayed", `{"description":"inspect","prompt":"inspect"}`, `,"isReplay":true`, 0},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), send(),
				`server protocol {"type":"system","subtype":"init","session_id":"root","claude_code_version":"2.1.258","tools":["Agent","Task"]}`,
				`server protocol {"type":"assistant","session_id":"root","message":{"content":[{"type":"tool_use","id":"call","name":"Agent","input":`+tc.input+`}]}}`,
				`server protocol {"type":"system","subtype":"task_started","session_id":"root","task_id":"child","task_type":"local_agent","tool_use_id":"call","is_backgrounded":true`+tc.replay+`}`,
				`server protocol {"type":"user","session_id":"root","tool_use_result":{"status":"async_launched","agentId":"child","description":"inspect","prompt":"inspect","outputFile":"/synthetic/child.output"},"message":{"content":[{"type":"tool_result","tool_use_id":"call","content":"done"}]}}`,
				`server protocol {"type":"sentinel","session_id":"root"}`)
			_, session, ctx := s.open(frames)
			created, associated := 0, 0
			for {
				e, err := session.Next(ctx)
				s.Require().NoError(err)
				if e.Diagnostic.NativeKind == "sentinel/" {
					break
				}
				if e.Kind == rt.AgentCreated {
					created++
					s.NotEmpty(e.Creation.Evidence)
				}
				if e.Association != nil {
					associated++
				}
			}
			s.Equal(tc.created, created)
			s.Positive(associated)
		})
	}
}

func (s *runtimeSuite) TestBackgroundSurvivesForegroundAndScopedReplacement() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"root"}`,
		`server protocol {"type":"assistant","session_id":"root","message":{"content":[{"type":"tool_use","id":"spawn-child","name":"Agent","input":{}}]}}`,
		`server protocol {"type":"system","subtype":"task_started","session_id":"root","task_id":"agent-X","task_type":"local_agent","tool_use_id":"spawn-child"}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root","tasks":[{"task_id":"root-task","run_id":"001","status":"running"}]}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root","agent_id":"agent-X","parent_tool_use_id":"spawn-child","tasks":[{"task_id":"child-task","status":"running"}]}`,
		`server protocol {"type":"result","session_id":"root","subtype":"success"}`,
		send(),
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root"}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"root","tasks":[]}`,
		`server protocol {"type":"result","session_id":"root","subtype":"success"}`,
		send())
	_, session, ctx := s.open(frames)
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Kind == rt.TurnCompleted {
			break
		}
	}
	snapshot := session.Snapshot()
	s.Equal("idle", snapshot.Foreground.State)
	s.Equal("attached", snapshot.Attachment)
	s.Require().Len(snapshot.Background.Tasks, 2)
	s.Equal("running", snapshot.Background.Tasks[0].State)
	snapshot.Background.Tasks[0].State = "caller mutation"
	s.NotEqual("caller mutation", session.Snapshot().Background.Tasks[0].State)
	receipt, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Kind == rt.TurnCompleted {
			s.Equal(receipt.OperationRef, e.OperationRef)
			break
		}
	}
	snapshot = session.Snapshot()
	s.Require().Len(snapshot.Background.Tasks, 1)
	s.Equal("child-task", snapshot.Background.Tasks[0].TaskRef)
	s.Equal("idle", snapshot.Foreground.State)
}

func (s *runtimeSuite) TestDistinctNativeIdentityNamespacesAndChildMessageIsolation() {
	frames := append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"root","model":"root-model"}`,
		`server protocol {"type":"assistant","session_id":"root","message":{"content":[{"type":"tool_use","id":"outer","name":"Agent","input":{"resume":"prior"}}]}}`,
		`server protocol {"type":"stream_event","session_id":"root","parent_tool_use_id":"outer","run_id":"old","event":{"type":"message_start","message":{"id":"child-message","role":"assistant","content":[]}}}`,
		`server protocol {"type":"stream_event","session_id":"root","event":{"type":"message_start","message":{"id":"root-message","role":"assistant","content":[]}}}`,
		`server protocol {"type":"assistant","session_id":"root","agent_id":"agent-native","parent_tool_use_id":"outer","message":{"content":"identity enrichment"}}`,
		`server protocol {"type":"system","subtype":"init","session_id":"root","agent_id":"agent-native","model":"child-model"}`,
		`server protocol {"type":"system","subtype":"task_started","session_id":"root","task_id":"task-native","run_id":"run-native","tool_use_id":"tool-native","task_type":"local_bash"}`,
		`server protocol {"type":"stream_event","session_id":"root","parent_tool_use_id":"outer","run_id":"old","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"late child"}}}`,
		`server protocol {"type":"stream_event","session_id":"root","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"root text"}}}`,
		`server protocol {"type":"sentinel","session_id":"root"}`)
	_, session, ctx := s.open(frames)
	enriched, distinct, childDelta, rootDelta := false, false, false, false
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "sentinel/" {
			break
		}
		s.NotEqual(rt.AgentCreated, e.Kind)
		if e.Association != nil && e.Association.State == "enriched" {
			enriched = true
			s.Equal("agent-native", e.Subject.AgentID)
			s.NotEqual(e.Subject.AgentID, e.Subject.ParentToolRef)
			s.Equal("outer", e.Subject.ParentToolRef)
		}
		if e.Task != nil && e.Task.TaskRef == "task-native" {
			distinct = true
			s.Equal("run-native", e.Subject.RunID)
			s.Equal("tool-native", e.Subject.ParentToolRef)
			s.Empty(e.Subject.AgentID)
		}
		if e.Message != nil && e.Message.Stage == "delta" {
			if e.Subject != nil {
				childDelta = true
				s.Equal("child-message", e.Message.MessageRef)
				s.Equal("old", e.Subject.RunID)
				s.Empty(e.OperationRef)
			} else {
				rootDelta = true
				s.Equal("root-message", e.Message.MessageRef)
			}
		}
	}
	s.True(enriched)
	s.True(distinct)
	s.True(childDelta)
	s.True(rootDelta)
	s.Equal("root-model", *session.Snapshot().Metadata.Model)
}

func (s *runtimeSuite) TestUnknownTaskVariantStaysDiagnostic() {
	_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"system","subtype":"task_future","session_id":"native-session","task_id":"unproven","status":"running"}`))
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "system/task_future" {
			s.Equal(rt.Unknown, e.Kind)
			s.Nil(e.Task)
			break
		}
	}
	s.Empty(session.Snapshot().Background.Tasks)
}

func (s *runtimeSuite) TestSnapshotPresenceAndMalformedFacts() {
	for _, tc := range []struct {
		name, field string
		kind        rt.EventKind
	}{
		{"absent", "", rt.Unknown}, {"null", `,"tasks":null`, rt.Failure}, {"empty", `,"tasks":[]`, rt.TaskSnapshot},
		{"missing identity", `,"tasks":[{}]`, rt.Failure},
		{"duplicate identity", `,"tasks":[{"task_id":"same"},{"task_id":"same"}]`, rt.Failure},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"native-session"`+tc.field+`}`))
			for {
				e, err := session.Next(ctx)
				s.Require().NoError(err)
				if e.Origin.SourceKind != "system/background_tasks_changed" && e.Diagnostic.NativeKind != "system/background_tasks_changed" {
					continue
				}
				s.Equal(tc.kind, e.Kind)
				if tc.kind == rt.Failure {
					s.Equal("decode_error", e.Error.Category)
				}
				break
			}
		})
	}
}

func (s *runtimeSuite) TestBackgroundRetentionRejectsExcessWithoutClearingPrior() {
	tasks := make([]map[string]string, 257)
	for i := range tasks {
		tasks[i] = map[string]string{"task_id": fmt.Sprintf("task-%d", i), "status": "running"}
	}
	data, err := json.Marshal(tasks)
	s.Require().NoError(err)
	_, session, ctx := s.open(append(startup(), send(),
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"native-session","tasks":[{"task_id":"retained","status":"running"}]}`,
		`server protocol {"type":"system","subtype":"background_tasks_changed","session_id":"native-session","tasks":`+string(data)+`}`))
	observed := false
	for {
		e, err := session.Next(ctx)
		if err != nil {
			s.ErrorIs(err, rt.ErrBackpressure)
			break
		}
		if e.TaskSnapshot != nil {
			observed = true
			s.Len(e.TaskSnapshot.Tasks, 1)
		}
	}
	s.True(observed)
	s.Require().Len(session.Snapshot().Background.Tasks, 1)
	s.Equal("retained", session.Snapshot().Background.Tasks[0].TaskRef)
}

func (s *runtimeSuite) TestTaskStatusPresence() {
	for _, tc := range []struct{ name, patch, want string }{
		{"omitted", `{"description":"still observed"}`, "running"},
		{"unknown", `{"status":"future-native-status"}`, "unknown"},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(),
				`server protocol {"type":"system","subtype":"task_started","session_id":"native-session","task_id":"task","task_type":"local_bash","run_id":"original-run"}`,
				`server protocol {"type":"system","subtype":"task_updated","session_id":"native-session","task_id":"task","run_id":"original-run","patch":`+tc.patch+`}`,
				`server protocol {"type":"result","session_id":"native-session","subtype":"success"}`, send()))
			for {
				e, err := session.Next(ctx)
				s.Require().NoError(err)
				if e.Task != nil && e.Origin.SourceKind == "system/task_updated" {
					break
				}
			}
			snapshot := session.Snapshot()
			s.Require().Len(snapshot.Background.Tasks, 1)
			s.Equal(tc.want, snapshot.Background.Tasks[0].State)
			s.True(snapshot.Background.Tasks[0].StateObserved)
		})
	}
}
