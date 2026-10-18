package codex_test

import (
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"strings"
)

func (s *runtimeSuite) TestStructuredToolLifecycle() {
	for _, tc := range []struct{ name, call, result, category string }{
		{"command", `{"type":"commandExecution","id":"call","command":"echo synthetic","cwd":"/synthetic","commandActions":[],"status":"inProgress"}`, `{"type":"commandExecution","id":"call","command":"echo synthetic","cwd":"/synthetic","commandActions":[],"status":"completed","aggregatedOutput":"bad","exitCode":7,"durationMs":42}`, "execute"},
		{"mcp", `{"type":"mcpToolCall","id":"call","server":"synthetic","tool":"lookup","arguments":{"n":9007199254740993},"status":"inProgress"}`, `{"type":"mcpToolCall","id":"call","server":"synthetic","tool":"lookup","arguments":{"n":9007199254740993},"status":"failed","result":{"content":[{"type":"text","text":"bad"}],"structuredContent":{"value":null}},"error":{"message":"synthetic"}}`, "unknown"},
		{"dynamic", `{"type":"dynamicToolCall","id":"call","tool":"lookup","arguments":{"n":9007199254740993},"status":"inProgress"}`, `{"type":"dynamicToolCall","id":"call","tool":"lookup","arguments":{"n":9007199254740993},"status":"completed","success":false,"contentItems":[{"type":"inputText","text":"bad"}]}`, "unknown"},
		{"file", `{"type":"fileChange","id":"call","status":"inProgress","changes":[{"path":"/synthetic/file","kind":{"type":"add"},"diff":"+value"}]}`, `{"type":"fileChange","id":"call","status":"failed","changes":[{"path":"/synthetic/file","kind":{"type":"add"},"diff":"+value"}]}`, "write"},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"item":`+tc.call+`}}`,
				`s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":`+tc.result+`}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			for _, kind := range []rt.EventKind{rt.ToolAnnounced, rt.ToolCompleted} {
				e := s.nextMapped(session, ctx)
				s.Require().Equal(kind, e.Kind, "%+v", e)
				s.Require().NotNil(e.Tool)
				s.Equal("call", e.Tool.CallRef)
				s.Equal(tc.category, e.Tool.Category)
				if kind == rt.ToolAnnounced {
					s.Equal("announced", e.Tool.State)
				} else {
					s.Equal("failed", e.Tool.State)
					if tc.name == "command" {
						s.Require().NotNil(e.Tool.ExitCode)
						s.Equal(int32(7), *e.Tool.ExitCode)
						s.Equal(int64(42), *e.Tool.DurationMS)
					}
					if tc.name == "mcp" {
						s.Equal("text", e.Tool.Output.Blocks[0].Kind)
						s.Require().NotNil(e.Tool.Content)
						s.JSONEq(`{"value":null}`, string(e.Tool.Content.Blocks[0].JSON))
					}
					if tc.name == "dynamic" {
						s.Equal("text", e.Tool.Output.Blocks[0].Kind)
					}
				}
				data, err := json.Marshal(e)
				s.Require().NoError(err)
				var decoded rt.Event
				s.Require().NoError(json.Unmarshal(data, &decoded))
				s.Equal(e, decoded)
			}
		})
	}
}

func (s *runtimeSuite) TestCommandOutputAvailability() {
	for _, tc := range []struct{ name, field, availability string }{
		{"missing", "", "missing"}, {"null", `,"aggregatedOutput":null`, "null"}, {"empty", `,"aggregatedOutput":""`, "provided_empty"}, {"long", `,"aggregatedOutput":"` + strings.Repeat("x", 20000) + `"`, "provided"},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":{"type":"commandExecution","id":"call","command":"synthetic","cwd":"/synthetic","commandActions":[],"status":"completed"`+tc.field+`}}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			e := s.nextMapped(session, ctx)
			s.Require().NotNil(e.Tool)
			s.Equal(tc.availability, e.Tool.Output.Availability)
			s.Equal("complete", e.Tool.Output.Completeness)
		})
	}
}

func (s *runtimeSuite) TestToolOutputUpdateDoesNotInventSuccess() {
	frames := append(startup(), turnStart()...)
	frames = append(frames,
		`s2c {"method":"item/commandExecution/outputDelta","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"call","delta":"observed"}}`,
		`s2c {"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed","items":[],"error":null}}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	s.Require().Equal(rt.ToolUpdated, e.Kind)
	s.Require().NotNil(e.Tool)
	s.Equal("call", e.Tool.CallRef)
	s.Equal("running", e.Tool.State)
	s.Equal("partial", e.Tool.Output.Completeness)
	s.Equal("observed", *e.Tool.Output.Blocks[0].Text)
	e = s.nextMapped(session, ctx)
	s.Equal(rt.TurnCompleted, e.Kind)
	s.Nil(e.Tool)
}

func (s *runtimeSuite) TestUnknownAndMalformedToolsStayDistinct() {
	for _, tc := range []struct {
		name, item string
		kind       rt.EventKind
	}{
		{"unknown", `{"type":"futureTool","id":"call","private":"synthetic-sensitive-tool-marker"}`, rt.Unknown},
		{"missing command", `{"type":"commandExecution","id":"call","cwd":"/synthetic","commandActions":[],"status":"inProgress"}`, rt.Failure},
		{"missing call", `{"type":"dynamicToolCall","id":"","tool":"lookup","arguments":{},"status":"inProgress"}`, rt.Failure},
		{"invalid arguments", `{"type":"mcpToolCall","id":"call","server":"synthetic","tool":"lookup","status":"inProgress"}`, rt.Failure},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"item":`+tc.item+`}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			var e rt.Event
			for {
				e, err = session.Next(ctx)
				s.Require().NoError(err)
				if e.Origin.SourceKind == "item/started" || e.Diagnostic.NativeKind == "item/started" {
					break
				}
			}
			s.Equal(tc.kind, e.Kind)
			s.Nil(e.Tool)
			s.Equal("native-thread", e.SessionID)
			if tc.kind == rt.Failure {
				s.Equal("decode_error", e.Error.Category)
				s.Equal("session", e.Error.Scope)
			}
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(data), "synthetic-sensitive-tool-marker")
		})
	}
}

func (s *runtimeSuite) TestMCPContentPresenceAndUnsupportedBlocks() {
	for _, tc := range []struct {
		name, field, availability string
		blocks                    int
	}{
		{"missing", "", "missing", 0}, {"null", `,"result":null`, "null", 0},
		{"empty", `,"result":{"content":[]}`, "provided_empty", 0},
		{"json null", `,"result":{"content":[],"structuredContent":null}`, "provided_empty", 0},
		{"mixed", `,"result":{"content":[{"type":"text","text":"prefix"},{"type":"image","data":"hidden-media"}]}`, "provided", 2},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":{"type":"mcpToolCall","id":"call","server":"synthetic","tool":"lookup","arguments":{},"status":"completed"`+tc.field+`}}}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			e := s.nextMapped(session, ctx)
			s.Require().NotNil(e.Tool)
			s.Equal(tc.availability, e.Tool.Output.Availability)
			s.Len(e.Tool.Output.Blocks, tc.blocks)
			if tc.name == "json null" {
				s.Require().NotNil(e.Tool.Content)
				s.Equal("provided", e.Tool.Content.Availability)
				s.Equal("null", string(e.Tool.Content.Blocks[0].JSON))
			}
			if tc.name == "mixed" {
				s.Equal("partial", e.Tool.Output.Completeness)
				s.Equal("unsupported", e.Tool.Output.Blocks[1].Kind)
			}
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(data), "hidden-media")
		})
	}
}

func (s *runtimeSuite) TestExplicitFailureInStartedItemKeepsItsEvidence() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"item":{"type":"dynamicToolCall","id":"call","tool":"lookup","arguments":{},"status":"failed","success":false}}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	s.Equal(rt.ToolCompleted, e.Kind)
	s.Require().NotNil(e.Tool)
	s.Equal("failed", e.Tool.State)
	_, err = json.Marshal(e)
	s.NoError(err)
}
