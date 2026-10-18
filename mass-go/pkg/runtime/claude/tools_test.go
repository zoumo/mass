package claude_test

import (
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestToolAnnouncementDoesNotClaimExecution() {
	_, session, ctx := s.open(append(startup(), send(),
		`server protocol {"type":"assistant","message":{"id":"m","role":"assistant","content":[{"type":"tool_use","id":"call","name":"Read","input":{"path":"synthetic"}}]}}`,
		`server control {"type":"control_request","request_id":"permission","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"call","input":{"path":"synthetic"}}}`,
		`client control {"type":"control_response","response":{"subtype":"success","request_id":"permission","response":{"behavior":"allow","updatedInput":{"path":"synthetic"}}}}`,
		`server protocol {"type":"result","subtype":"success","session_id":"native-session","is_error":false}`))
	var tool rt.Event
	var interaction *rt.Interaction
	for tool.Tool == nil || interaction == nil {
		e := s.next(session, ctx)
		if e.Tool != nil {
			tool = e
		}
		if e.Interaction != nil {
			interaction = e.Interaction
		}
	}
	s.Require().NoError(session.ResponseHandle(interaction.Ref).Respond(ctx, rt.Response{Allow: true}))
	s.Equal("tool.announced", string(tool.Kind))
	s.Equal("announced", tool.Tool.State)
	data, err := json.Marshal(tool)
	s.Require().NoError(err)
	s.Contains(string(data), `"input":{"availability":"provided"`)
	s.Contains(string(data), `"json":{"path":"synthetic"}`)
	e := s.next(session, ctx)
	s.Equal(rt.TurnCompleted, e.Kind)
	s.Nil(e.Tool)
}

func (s *runtimeSuite) TestToolResultsPreserveAvailabilityAndFailure() {
	for _, tc := range []struct {
		name, field, availability string
		count                     int
	}{
		{"missing", "", "missing", 0}, {"null", `,"content":null`, "null", 0},
		{"empty", `,"content":""`, "provided_empty", 1}, {"empty blocks", `,"content":[]`, "provided_empty", 0},
		{"text blocks", `,"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]`, "provided", 2},
		{"json", `,"content":[{"type":"json","json":{"n":9007199254740993,"v":null}}]`, "provided", 1},
		{"mixed", `,"content":[{"type":"text","text":"one"},{"type":"image","source":{"data":"hidden-media"}}]`, "provided", 2},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(),
				`server protocol {"type":"user","message":{"id":"result-message","role":"user","content":[{"type":"tool_result","tool_use_id":"call","is_error":true`+tc.field+`}]}}`,
				`server protocol {"type":"result","subtype":"success","session_id":"native-session","is_error":false}`))
			var e rt.Event
			for e.Tool == nil {
				e = s.next(session, ctx)
			}
			s.Equal(rt.ToolCompleted, e.Kind)
			s.Equal("call", e.Tool.CallRef)
			s.Equal("failed", e.Tool.State)
			s.Equal("native_is_error", e.Tool.OutcomeEvidence)
			s.Equal(tc.availability, e.Tool.Output.Availability)
			s.Len(e.Tool.Output.Blocks, tc.count)
			if tc.name == "json" {
				s.JSONEq(`{"n":9007199254740993,"v":null}`, string(e.Tool.Output.Blocks[0].JSON))
			}
			if tc.name == "mixed" {
				s.Equal("partial", e.Tool.Output.Completeness)
				s.Equal("unsupported", e.Tool.Output.Blocks[1].Kind)
			}
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(data), "hidden-media")
			var decoded rt.Event
			s.Require().NoError(json.Unmarshal(data, &decoded))
			s.Equal(e, decoded)
			s.Equal(rt.TurnCompleted, s.next(session, ctx).Kind)
		})
	}
}

func (s *runtimeSuite) TestStreamedToolInputKeepsCallAndBlockAssociation() {
	_, session, ctx := s.open(append(startup(), send(),
		`server protocol {"type":"stream_event","event":{"type":"message_start","message":{"id":"m","role":"assistant","content":[]}}}`,
		`server protocol {"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call","name":"Read","input":{}}}}`,
		`server protocol {"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}}`,
		`server protocol {"type":"stream_event","event":{"type":"content_block_stop","index":2}}`,
		`server protocol {"type":"assistant","message":{"id":"m","role":"assistant","content":[{"type":"tool_use","id":"call","name":"Read","input":{"path":"synthetic"}}]}}`,
		`server protocol {"type":"user","message":{"id":"r","role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"observed"}]}}`))
	for step, kind := range []rt.EventKind{rt.ToolAnnounced, rt.ToolUpdated, rt.ToolUpdated, rt.ToolAnnounced, rt.ToolCompleted} {
		var e rt.Event
		for e.Tool == nil {
			e = s.next(session, ctx)
		}
		s.Equal(kind, e.Kind)
		s.Equal("call", e.Tool.CallRef)
		if step < 4 {
			s.Equal("announced", e.Tool.State)
			s.Equal("m", e.Tool.MessageRef)
		} else {
			s.Equal("completed", e.Tool.State)
			s.Equal("r", e.Tool.MessageRef)
			s.Equal("observed", *e.Tool.Output.Blocks[0].Text)
		}
		if step < 3 {
			s.Equal(2, *e.Tool.BlockIndex)
		} else {
			s.Zero(*e.Tool.BlockIndex)
		}
		data, err := json.Marshal(e)
		s.Require().NoError(err)
		if kind == rt.ToolUpdated && e.Tool.Input.Completeness == "partial" {
			s.Contains(string(data), `"inputJsonDelta":"{\"path\":"`)
		}
		if kind == rt.ToolAnnounced {
			e.Tool.CallRef = "caller-edit"
			*e.Tool.BlockIndex = 99
		}
	}
}

func (s *runtimeSuite) TestUnknownAndMalformedToolBlocksStayDistinct() {
	for _, tc := range []struct {
		name, block string
		kind        rt.EventKind
	}{
		{"unknown", `{"type":"future_tool","id":"call","private":"synthetic-sensitive-tool-marker"}`, rt.MessageStarted},
		{"missing call", `{"type":"tool_use","name":"Read","input":{}}`, rt.Failure},
		{"missing name", `{"type":"tool_use","id":"call","input":{}}`, rt.Failure},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":`+tc.block+`}}`))
			e := s.next(session, ctx)
			if e.Kind == rt.SessionReady {
				e = s.next(session, ctx)
			}
			s.Equal(tc.kind, e.Kind)
			s.Nil(e.Tool)
			if tc.kind == rt.Failure {
				s.Equal("decode_error", e.Error.Category)
				s.Equal("session", e.Error.Scope)
			} else {
				s.Equal("unsupported", e.Message.Content.Availability)
			}
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(data), "synthetic-sensitive-tool-marker")
		})
	}
}
