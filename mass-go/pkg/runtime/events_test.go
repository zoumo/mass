package runtime_test

import (
	"encoding/json"
	"github.com/stretchr/testify/suite"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"strings"
	"testing"
	"time"
)

type eventSuite struct{ suite.Suite }

func TestEvents(t *testing.T) { suite.Run(t, new(eventSuite)) }
func eventWithMessage() rt.Event {
	return rt.Event{SchemaVersion: 1, Kind: rt.MessageCompleted, Scope: "session", RuntimeName: "named", SessionID: "native", ConnectionEpoch: "attachment", Seq: 1, ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Origin: rt.EventOrigin{Kind: "native", Attribution: "root", Completeness: "complete"}, Message: &rt.MessagePayload{MessageRef: "m", Role: "assistant", Stage: "completed", Content: rt.TextContent("", "completed", "complete", nil)}}
}
func (s *eventSuite) TestDataUnionAndPresence() {
	for _, availability := range []string{"missing", "null", "provided_empty", "provided", "redacted", "unsupported"} {
		s.Run(availability, func() {
			e := eventWithMessage()
			e.Message.Content = rt.ContentSet{Availability: availability, Completeness: "unknown"}
			if availability == "provided" {
				e.Message.Content.Blocks = []rt.ContentBlock{{Kind: "json", JSON: json.RawMessage(`{"number":9007199254740993,"null":null}`), Stage: "completed", Completeness: "complete"}}
			}
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			var decoded rt.Event
			s.Require().NoError(json.Unmarshal(data, &decoded))
			s.Equal(e, decoded)
			s.Contains(string(data), `"payload"`)
			s.NotContains(string(data), `"Message"`)
			if availability == "provided" {
				s.Contains(string(data), "9007199254740993")
			}
		})
	}
}
func (s *eventSuite) TestInvalidUnionsAndContentAreRejected() {
	valid, err := json.Marshal(eventWithMessage())
	s.Require().NoError(err)
	for _, tc := range []struct{ name, from, to string }{
		{"wrong type", `"type":"message.completed"`, `"type":"reasoning.completed"`},
		{"unknown schema", `"schemaVersion":1`, `"schemaVersion":2`},
		{"invalid role", `"role":"assistant"`, `"role":"invalid"`},
		{"type-stage mismatch", `"stage":"completed"`, `"stage":"delta"`},
		{"invalid origin attribution", `"attribution":"root"`, `"attribution":"invented"`},
		{"invalid stage", `"stage":"completed"`, `"stage":"invalid"`},
		{"invalid content", `"availability":"provided_empty"`, `"availability":"invented"`},
		{"wrong content kind", `"kind":"text"`, `"kind":"json"`},
		{"unsafe cursor", `"seq":1`, `"seq":9007199254740992`},
		{"missing session", `"sessionId":"native"`, `"sessionId":""`},
	} {
		s.Run(tc.name, func() {
			var decoded rt.Event
			s.Error(json.Unmarshal([]byte(strings.Replace(string(valid), tc.from, tc.to, 1)), &decoded))
		})
	}
	e := eventWithMessage()
	e.Plan = &rt.PlanPayload{UpdateMode: "replace", Completeness: "complete"}
	_, err = json.Marshal(e)
	s.Error(err)
	e = eventWithMessage()
	e.Diagnostic = rt.Diagnostic{NativeKind: "extra"}
	_, err = json.Marshal(e)
	s.Error(err)
}

func (s *eventSuite) TestMetadataNullIsNotOmission() {
	e := eventWithMessage()
	e.Message = nil
	e.Kind = rt.Metadata
	e.Metadata = &rt.MetadataPayload{}
	data, err := json.Marshal(e)
	s.Require().NoError(err)
	for _, payload := range []string{`{"commands":null}`, `{"capabilities":null}`, `{"tools":null}`, `{"model":null}`} {
		var decoded rt.Event
		s.Error(json.Unmarshal([]byte(strings.Replace(string(data), `"payload":{}`, `"payload":`+payload, 1)), &decoded))
	}
}
