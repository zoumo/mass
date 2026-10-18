package claude_test

import (
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestMessageBlocksAndFrameIdentity() {
	frames := append(startup(), send(),
		`server protocol {"type":"stream_event","uuid":"frame-start","event":{"type":"message_start","message":{"id":"native-message","role":"assistant","content":[]}}}`,
		`server protocol {"type":"stream_event","uuid":"frame-a","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}}`,
		`server protocol {"type":"stream_event","uuid":"frame-b","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"second"}}}`,
		`server protocol {"type":"assistant","uuid":"frame-full","isReplay":true,"origin":{"kind":"history"},"message":{"id":"native-message","role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)
	_, session, ctx := s.open(frames)
	for _, tc := range []struct {
		stage, frame string
		index        int
	}{{"started", "frame-start", -1}, {"delta", "frame-a", 0}, {"delta", "frame-b", 1}, {"completed", "frame-full", -1}} {
		e := s.next(session, ctx)
		if e.Kind == rt.SessionReady {
			e = s.next(session, ctx)
		}
		s.Require().NotNil(e.Message)
		s.Equal("native-message", e.Message.MessageRef)
		s.Equal(tc.frame, e.FrameRef)
		s.Equal(tc.stage, e.Message.Stage)
		s.Equal("assistant", e.Message.Role)
		if tc.index >= 0 {
			s.Require().Len(e.Message.Content.Blocks, 1)
			s.Equal(tc.index, *e.Message.Content.Blocks[0].Index)
			s.Empty(e.Message.Content.Blocks[0].BlockRef)
		}
		if tc.stage == "completed" {
			s.Len(e.Message.Content.Blocks, 2)
			s.Require().NotNil(e.Origin.Replay)
			s.True(*e.Origin.Replay)
			s.Equal("history", e.Origin.SourceKind)
		}
		data, err := json.Marshal(e)
		s.Require().NoError(err)
		var decoded rt.Event
		s.Require().NoError(json.Unmarshal(data, &decoded))
		s.Equal(e, decoded)
	}
}

func (s *runtimeSuite) TestEqualUnidentifiedDeltasAreBothDelivered() {
	frame := `server protocol {"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"same"}}}`
	_, session, ctx := s.open(append(startup(), send(), frame, frame))
	var previous uint64
	for range 2 {
		e := s.next(session, ctx)
		if e.Kind == rt.SessionReady {
			e = s.next(session, ctx)
		}
		s.Require().NotNil(e.Message)
		s.Empty(e.Message.MessageRef)
		s.Nil(e.Message.Content.Blocks[0].Index)
		s.Equal("same", *e.Message.Content.Blocks[0].Text)
		s.Greater(e.Seq, previous)
		previous = e.Seq
	}
}

func (s *runtimeSuite) TestContentAvailabilityAndReasoning() {
	for _, tc := range []struct {
		name, raw, availability, completeness string
		reasoning                             bool
	}{
		{"empty text", `""`, "provided_empty", "complete", false},
		{"empty blocks", `[]`, "provided_empty", "complete", false},
		{"media", `[{"type":"image","source":{"type":"base64","data":"synthetic-image"}}]`, "unsupported", "partial", false},
		{"thinking", `[{"type":"thinking","thinking":"observed thought","signature":"synthetic"}]`, "provided", "complete", true},
		{"redacted", `[{"type":"redacted_thinking","data":"synthetic-hidden-marker"}]`, "redacted", "partial", true},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"assistant","uuid":"frame","message":{"id":"m","role":"assistant","content":`+tc.raw+`}}`))
			e := s.next(session, ctx)
			if e.Kind == rt.SessionReady {
				e = s.next(session, ctx)
			}
			s.Require().NotNil(e.Message)
			s.Equal(tc.availability, e.Message.Content.Availability)
			s.Equal(tc.completeness, e.Message.Content.Completeness)
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(data), "synthetic-hidden-marker")
			s.NotContains(string(data), "synthetic-image")
			if tc.reasoning {
				e = s.next(session, ctx)
				s.Require().NotNil(e.Reasoning)
				s.Equal("completed", e.Reasoning.Stage)
				s.Equal("m", e.Reasoning.MessageRef)
				if tc.name == "redacted" {
					s.Equal("redacted", e.Reasoning.Content.Availability)
					s.Nil(e.Reasoning.Content.Blocks[0].Text)
				}
			}
		})
	}
}

func (s *runtimeSuite) TestMetadataPresenceAndCollectionClearing() {
	_, session, ctx := s.open(append(startup(), send(),
		`server protocol {"type":"system","subtype":"init","session_id":"native-session","capabilities":["synthetic-feature"],"slash_commands":["help"],"tools":["Read"],"model":"synthetic-model"}`,
		`server protocol {"type":"system","subtype":"init","session_id":"native-session","model":"next-model"}`,
		`server protocol {"type":"system","subtype":"init","session_id":"native-session","capabilities":[],"slash_commands":[],"tools":[]}`))
	for i := range 3 {
		e := s.next(session, ctx)
		for e.Metadata == nil {
			e = s.next(session, ctx)
		}
		if i == 1 {
			s.Nil(e.Metadata.Capabilities)
			s.Nil(e.Metadata.Commands)
			s.Nil(e.Metadata.Tools)
		}
		snapshot := session.Snapshot().Metadata
		s.Require().NotNil(snapshot.Commands)
		s.Require().NotNil(snapshot.Capabilities)
		if i < 2 {
			s.Equal([]string{"help"}, *snapshot.Commands)
			s.Equal([]string{"synthetic-feature"}, *snapshot.Capabilities)
		} else {
			s.Empty(*snapshot.Commands)
			s.Empty(*snapshot.Capabilities)
			s.Empty(*snapshot.Tools)
		}
		if i > 0 {
			s.Equal("next-model", *snapshot.Model)
		}
		// Get a copy: callers cannot mutate the next snapshot through prior data.
		*snapshot.Model = "caller edit"
	}
}

func (s *runtimeSuite) TestStreamMissingVersusNullContent() {
	for _, tc := range []struct{ name, field, availability string }{{"absent", "", "missing"}, {"null", `,"content":null`, "null"}} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"stream_event","event":{"type":"message_start","message":{"id":"m","role":"assistant"`+tc.field+`}}}`))
			e := s.next(session, ctx)
			if e.Kind == rt.SessionReady {
				e = s.next(session, ctx)
			}
			s.Require().NotNil(e.Message)
			s.Equal(tc.availability, e.Message.Content.Availability)
			s.Empty(e.Message.Content.Blocks)
		})
	}
}

func (s *runtimeSuite) TestStreamBoundariesRetainAssociationWithoutInventedText() {
	_, session, ctx := s.open(append(startup(), send(),
		`server protocol {"type":"stream_event","event":{"type":"message_start","message":{"id":"m","role":"assistant","content":[]}}}`,
		`server protocol {"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`server protocol {"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`server protocol {"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		`server protocol {"type":"stream_event","event":{"type":"message_stop"}}`))
	for _, stage := range []string{"started", "started", "delta", "delta", "completed"} {
		e := s.next(session, ctx)
		if e.Kind == rt.SessionReady {
			e = s.next(session, ctx)
		}
		s.Require().NotNil(e.Message)
		s.Equal("m", e.Message.MessageRef)
		s.Equal(stage, e.Message.Stage)
		if e.Message.BlockStage == "completed" {
			s.Require().NotNil(e.Message.BlockIndex)
			s.Zero(*e.Message.BlockIndex)
			s.Equal("missing", e.Message.Content.Availability)
		}
	}
}

func (s *runtimeSuite) TestMalformedKnownStreamFactsFailExplicitly() {
	for _, tc := range []struct{ name, event string }{
		{"missing block", `{"type":"content_block_start","index":0}`},
		{"missing text", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`},
		{"negative index", `{"type":"content_block_delta","index":-1,"delta":{"type":"text_delta","text":"invalid"}}`},
	} {
		s.Run(tc.name, func() {
			_, session, ctx := s.open(append(startup(), send(), `server protocol {"type":"stream_event","event":`+tc.event+`}`))
			e := s.next(session, ctx)
			if e.Kind == rt.SessionReady {
				e = s.next(session, ctx)
			}
			s.Require().Equal(rt.Failure, e.Kind)
			s.Equal("decode_error", e.Error.Category)
			s.Equal("session", e.Error.Scope)
		})
	}
}
