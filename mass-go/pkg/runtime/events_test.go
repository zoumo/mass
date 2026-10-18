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

func (s *eventSuite) TestStructuredToolSchemaAndTruncationEvidence() {
	e := eventWithMessage()
	e.Kind = rt.ToolCompleted
	e.Message = nil
	e.Tool = &rt.ToolPayload{CallRef: "call", Name: "lookup", Category: "unknown", State: "completed", Input: rt.JSONValueContent(json.RawMessage(`null`), "completed", "complete"), Output: rt.TextContent("observed prefix", "completed", "truncated", nil), OutcomeEvidence: "native_result"}
	_, err := json.Marshal(e)
	s.Error(err, "truncation requires source evidence")
	e.Tool.Output.TruncationEvidence = "native_explicit_truncation"
	data, err := json.Marshal(e)
	s.Require().NoError(err)
	var decoded rt.Event
	s.Require().NoError(json.Unmarshal(data, &decoded))
	s.Equal(e, decoded)
	s.Equal("provided", decoded.Tool.Input.Availability)
	s.Equal("null", string(decoded.Tool.Input.Blocks[0].JSON))
	for _, tc := range []struct{ name, from, to string }{
		{"category", `"category":"unknown"`, `"category":"invented"`},
		{"state", `"state":"completed"`, `"state":"running"`},
		{"missing call", `"callRef":"call"`, `"callRef":""`},
		{"updated terminal", `"type":"tool.completed"`, `"type":"tool.updated"`},
		{"delta stage", `"output":`, `"inputJsonDelta":"{","output":`},
	} {
		s.Run(tc.name, func() {
			var invalid rt.Event
			s.Error(json.Unmarshal([]byte(strings.Replace(string(data), tc.from, tc.to, 1)), &invalid))
		})
	}
}

func (s *eventSuite) TestAgentTaskAndUsageUnion() {
	subject := &rt.EventSubject{AgentID: "agent", RunID: "old-run", TaskID: "task", ParentToolRef: "tool", AssociationRef: "sdk-key", AssociationScope: "sdk_attachment"}
	task := rt.TaskPayload{TaskRef: "task", SourceScope: "source", Subject: *subject, Background: rt.Known(true), State: "running", Completeness: "partial"}
	for _, tc := range []struct {
		name string
		set  func(*rt.Event)
	}{
		{"associated", func(e *rt.Event) {
			e.Kind = rt.AgentAssociated
			e.Association = &rt.AgentAssociationPayload{State: "enriched", Evidence: "native_identity", Provenance: "live", Completeness: "partial"}
		}},
		{"created", func(e *rt.Event) {
			e.Kind = rt.AgentCreated
			e.Creation = &rt.AgentCreatedPayload{Evidence: "sdk_creation", Provenance: "live"}
		}},
		{"run terminal", func(e *rt.Event) {
			e.Kind = rt.AgentStateChanged
			e.AgentState = &rt.AgentStatePayload{State: "terminal", Terminal: true, Outcome: "completed", Completeness: "partial"}
		}},
		{"task", func(e *rt.Event) { e.Kind = rt.TaskChanged; e.Task = &task }},
		{"snapshot", func(e *rt.Event) {
			e.Kind = rt.TaskSnapshot
			e.TaskSnapshot = &rt.TaskSnapshotPayload{Mode: "replace", SourceScope: "source", Tasks: []rt.TaskPayload{task}, Completeness: "complete"}
		}},
		{"empty snapshot", func(e *rt.Event) {
			e.Kind = rt.TaskSnapshot
			e.TaskSnapshot = &rt.TaskSnapshotPayload{Mode: "replace", SourceScope: "source", Tasks: []rt.TaskPayload{}, Completeness: "complete"}
		}},
		{"usage cumulative", func(e *rt.Event) {
			e.Kind = rt.UsageUpdated
			e.Usage = &rt.UsagePayload{Counters: map[string]json.RawMessage{"tokens": json.RawMessage(`9007199254740993`)}, Mode: "cumulative", AccountingScope: "root", ChildInclusion: "yes"}
		}},
		{"usage delta", func(e *rt.Event) {
			e.Kind = rt.UsageUpdated
			e.Usage = &rt.UsagePayload{Counters: map[string]json.RawMessage{"tokens": json.RawMessage(`3`)}, Mode: "delta", AccountingScope: "child", ChildInclusion: "no"}
		}},
		{"observation", func(e *rt.Event) {
			e.Kind = rt.ObservationChanged
			e.Observation = &rt.ObservationPayload{State: "incomplete", Completeness: "partial"}
		}},
	} {
		s.Run(tc.name, func() {
			e := eventWithMessage()
			e.Message = nil
			e.Subject = subject
			tc.set(&e)
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			var decoded rt.Event
			s.Require().NoError(json.Unmarshal(data, &decoded))
			s.Equal(e, decoded)
			e.Work = &rt.WorkPayload{Status: "completed"}
			_, err = json.Marshal(e)
			s.Error(err)
		})
	}
	for _, bad := range []rt.Event{
		{Kind: rt.AgentAssociated, Association: &rt.AgentAssociationPayload{State: "created", Evidence: "label", Completeness: "partial"}},
		{Kind: rt.TaskSnapshot, TaskSnapshot: &rt.TaskSnapshotPayload{Mode: "replace", SourceScope: "source", Tasks: nil, Completeness: "complete"}},
		{Kind: rt.TaskChanged, Task: &rt.TaskPayload{TaskRef: "task", State: "deleted"}},
		{Kind: rt.UsageUpdated, Usage: &rt.UsagePayload{Counters: map[string]json.RawMessage{}, Mode: "sum", AccountingScope: "root", ChildInclusion: "yes"}},
	} {
		e := eventWithMessage()
		e.Message = nil
		e.Subject = subject
		e.Kind = bad.Kind
		e.Association = bad.Association
		e.Task = bad.Task
		e.TaskSnapshot = bad.TaskSnapshot
		e.Usage = bad.Usage
		_, err := json.Marshal(e)
		s.Error(err)
	}
}
