package codex_test

import (
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestNativeAssociationsAndChildRuns() {
	for _, tc := range []struct {
		name, tool, status string
		created            int
	}{
		{"spawn", "spawnAgent", "completed", 1}, {"resume", "resumeAgent", "completed", 0},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames,
				`s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","completedAtMs":8,"item":{"type":"collabAgentToolCall","id":"parent-tool","agentsStates":{"child-1":{"status":"running","message":null}},"receiverThreadIds":["child-1"],"senderThreadId":"native-thread","status":"`+tc.status+`","tool":"`+tc.tool+`","prompt":null,"model":null,"reasoningEffort":null}}}`,
				`s2c {"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-run-old","status":"completed","items":[],"error":null}}}`,
				`s2c {"method":"item/agentMessage/delta","params":{"threadId":"child-1","turnId":"child-run-new","itemId":"child-message","delta":"child text"}}`,
				`s2c {"method":"item/agentMessage/delta","params":{"threadId":"child-1","turnId":"child-run-old","itemId":"late-message","delta":"late"}}`,
				`s2c {"method":"thread/tokenUsage/updated","params":{"threadId":"native-thread","turnId":"native-turn","tokenUsage":{"total":{"totalTokens":10,"inputTokens":7,"outputTokens":3,"cachedInputTokens":0,"reasoningOutputTokens":0},"last":{"totalTokens":4,"inputTokens":2,"outputTokens":2,"cachedInputTokens":0,"reasoningOutputTokens":0},"modelContextWindow":null}}}`,
				`s2c {"method":"sentinel","params":{"threadId":"native-thread"}}`)
			_, session, ctx := s.open(frames)
			receipt, err := session.Submit(ctx, rt.TextInput("hello"))
			s.Require().NoError(err)
			associated, created, child, late, usage := 0, 0, false, false, 0
			for {
				e, err := session.Next(ctx)
				s.Require().NoError(err)
				if e.Diagnostic.NativeKind == "sentinel" {
					break
				}
				data, err := json.Marshal(e)
				s.Require().NoError(err)
				var decoded rt.Event
				s.Require().NoError(json.Unmarshal(data, &decoded))
				s.Equal(e, decoded)
				s.Equal("native-thread", e.SessionID)
				if e.Association != nil {
					associated++
					s.Equal("child-1", e.Subject.AgentID)
					s.Equal("parent-tool", e.Subject.ParentToolRef)
					s.Empty(e.Subject.TaskID)
					s.Empty(e.Subject.RunID)
				}
				if e.Creation != nil {
					created++
					s.Equal("sdk_derived", e.Origin.Kind)
				}
				if e.AgentState != nil && e.Subject.RunID == "child-run-old" {
					child = true
					s.Equal("terminal", e.AgentState.State)
					s.Empty(e.OperationRef)
				}
				if e.Message != nil && e.Message.MessageRef == "late-message" {
					late = true
					s.Equal("child-run-old", e.Subject.RunID)
					s.Empty(e.OperationRef)
				}
				if e.Usage != nil {
					usage++
					s.Equal("unknown", e.Usage.ChildInclusion)
					if e.Usage.Mode == "cumulative" {
						s.Equal("10", string(e.Usage.Counters["totalTokens"]))
					} else {
						s.Equal("4", string(e.Usage.Counters["totalTokens"]))
					}
				}
			}
			s.Equal(tc.created, created)
			if tc.created == 0 {
				s.Positive(associated)
			}
			s.True(child)
			s.True(late)
			s.Equal(2, usage)
			state, err := session.GetSubmission(receipt.OperationRef)
			s.Require().NoError(err)
			s.Equal("unknown", state.Completed.State)
			s.Equal("running", session.Snapshot().Foreground.State)
		})
	}
}

func (s *runtimeSuite) TestNativeHistorySeedNeverCreates() {
	frames := startup()
	var ack map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal([]byte(frames[4][4:]), &ack))
	var result map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(ack["result"], &result))
	var thread map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(result["thread"], &thread))
	thread["turns"] = json.RawMessage(`[{"id":"historical-turn","status":"completed","error":null,"items":[{"type":"collabAgentToolCall","id":"historical-tool","senderThreadId":"native-thread","receiverThreadIds":["historical-child"],"tool":"spawnAgent","status":"completed","agentsStates":{},"prompt":null,"model":null,"reasoningEffort":null}]}]`)
	result["thread"], _ = json.Marshal(thread)
	ack["result"], _ = json.Marshal(result)
	wire, _ := json.Marshal(ack)
	frames[4] = "s2c " + string(wire)
	frames = append(frames, turnStart()...)
	frames = append(frames, `s2c {"method":"item/agentMessage/delta","params":{"threadId":"historical-child","turnId":"original-run","itemId":"child-message","delta":"historical child continues"}}`, `s2c {"method":"sentinel","params":{"threadId":"native-thread"}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	associated, child := false, false
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "sentinel" {
			break
		}
		s.NotEqual(rt.AgentCreated, e.Kind)
		if e.Association != nil {
			associated = true
			s.Equal("historical-child", e.Subject.AgentID)
			s.Equal("history", e.Association.Provenance)
		}
		if e.Message != nil {
			child = true
			s.Equal("historical-child", e.Subject.AgentID)
			s.Equal("original-run", e.Subject.RunID)
			s.Empty(e.OperationRef)
		}
	}
	s.True(associated)
	s.True(child)
}
