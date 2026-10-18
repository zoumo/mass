package codex_test

import (
	"context"
	"encoding/json"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"time"
)

func (s *runtimeSuite) TestTypedMessageCorrelation() {
	frames := append(startup(), turnStart()...)
	frames = append(frames,
		`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"m","delta":"same"}}`,
		`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"m","delta":"same"}}`,
		`s2c {"method":"item/completed","params":{"threadId":"native-thread","turnId":"native-turn","startedAtMs":1,"completedAtMs":2,"item":{"type":"agentMessage","id":"m","text":"samesame"}}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	for _, stage := range []string{"delta", "delta", "completed"} {
		event := s.nextMapped(session, ctx)
		s.Require().NotNil(event.Message, "stage=%s event=%+v error=%+v", stage, event, event.Error)
		s.Equal("m", event.Message.MessageRef)
		s.Equal("assistant", event.Message.Role)
		s.Equal(stage, event.Message.Stage)
		s.Equal(1, event.SchemaVersion)
		s.Equal("session", event.Scope)
		s.NotEmpty(event.ConnectionEpoch)
		s.Positive(event.Seq)
		data, err := json.Marshal(event)
		s.Require().NoError(err)
		var decoded rt.Event
		s.Require().NoError(json.Unmarshal(data, &decoded))
		s.Equal(event, decoded)
	}
}

func (s *runtimeSuite) TestReasoningPlansAndMetadata() {
	for _, tc := range []struct {
		name, method, params string
		kind                 rt.EventKind
		check                func(rt.Event)
	}{
		{"reasoning delta", "item/reasoning/textDelta", `{"threadId":"native-thread","turnId":"native-turn","itemId":"r","contentIndex":2,"delta":"observed"}`, rt.ReasoningDelta, func(e rt.Event) {
			s.Equal("r", e.Reasoning.MessageRef)
			s.Equal("content", e.Reasoning.Channel)
			s.Equal(2, *e.Reasoning.Content.Blocks[0].Index)
			s.Equal("observed", *e.Reasoning.Content.Blocks[0].Text)
		}},
		{"summary delta", "item/reasoning/summaryTextDelta", `{"threadId":"native-thread","turnId":"native-turn","itemId":"r","summaryIndex":1,"delta":"summary"}`, rt.ReasoningDelta, func(e rt.Event) {
			s.Equal("summary", e.Reasoning.Channel)
			s.Equal(1, *e.Reasoning.Content.Blocks[0].Index)
		}},
		{"summary boundary", "item/reasoning/summaryPartAdded", `{"threadId":"native-thread","turnId":"native-turn","itemId":"r","summaryIndex":3}`, rt.ReasoningDelta, func(e rt.Event) {
			s.Equal(3, *e.Reasoning.BlockIndex)
			s.Equal("missing", e.Reasoning.Content.Availability)
		}},
		{"reasoning complete", "item/completed", `{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":{"type":"reasoning","id":"r","summary":["summary"],"content":["observed"]}}`, rt.ReasoningCompleted, func(e rt.Event) {
			s.Equal("completed", e.Reasoning.Stage)
			s.Len(e.Reasoning.Content.Blocks, 2)
			s.Equal("content", e.Reasoning.Content.Blocks[0].Namespace)
			s.Equal("summary", e.Reasoning.Content.Blocks[1].Namespace)
		}},
		{"reasoning absent", "item/completed", `{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":{"type":"reasoning","id":"r"}}`, rt.ReasoningCompleted, func(e rt.Event) {
			s.Equal("missing", e.Reasoning.Content.Availability)
			s.Empty(e.Reasoning.Content.Blocks)
		}},
		{"plan replace", "turn/plan/updated", `{"threadId":"native-thread","turnId":"native-turn","explanation":null,"plan":[{"step":"inspect","status":"completed"},{"step":"implement","status":"inProgress"},{"step":"verify","status":"pending"}]}`, rt.Plan, func(e rt.Event) {
			s.Equal("replace", e.Plan.UpdateMode)
			s.Equal([]rt.PlanEntry{{Text: "inspect", Status: "completed"}, {Text: "implement", Status: "in_progress"}, {Text: "verify", Status: "pending"}}, e.Plan.Entries)
		}},
		{"plan clear", "turn/plan/updated", `{"threadId":"native-thread","turnId":"native-turn","explanation":null,"plan":[]}`, rt.Plan, func(e rt.Event) { s.Empty(e.Plan.Entries); s.Equal("replace", e.Plan.UpdateMode) }},
		{"user echo", "item/completed", `{"threadId":"native-thread","turnId":"native-turn","completedAtMs":2,"item":{"type":"userMessage","id":"u","content":[{"type":"text","text":"hello","text_elements":[]}]}}`, rt.MessageCompleted, func(e rt.Event) {
			s.Equal("user", e.Message.Role)
			s.Equal("u", e.Message.MessageRef)
			s.Equal("hello", *e.Message.Content.Blocks[0].Text)
		}},
		{"metadata title", "thread/name/updated", `{"threadId":"native-thread","name":"Observed title"}`, rt.Metadata, func(e rt.Event) { s.Equal("Observed title", *e.Metadata.Title) }},
		{"malformed known", "item/reasoning/textDelta", `{"threadId":"native-thread","turnId":"native-turn","itemId":"r","delta":null}`, rt.Failure, func(e rt.Event) { s.Equal("decode_error", e.Error.Category); s.Equal("session", e.Error.Scope) }},
	} {
		s.Run(tc.name, func() {
			frames := append(startup(), turnStart()...)
			frames = append(frames, `s2c {"method":"`+tc.method+`","params":`+tc.params+`}`)
			_, session, ctx := s.open(frames)
			_, err := session.Submit(ctx, "hello")
			s.Require().NoError(err)
			e := s.nextMapped(session, ctx)
			s.Require().Equal(tc.kind, e.Kind)
			tc.check(e)
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			var decoded rt.Event
			s.Require().NoError(json.Unmarshal(data, &decoded))
			s.Equal(e, decoded)
		})
	}
}

// Resolving live authority must remain possible while the sole event reader is
// waiting for a native fact that itself depends on the permission answer.
func (s *runtimeSuite) TestAuthorityLookupWhileNextWaits() {
	frames := append(startup(), turnStart()...)
	frames = append(frames,
		`s2c {"id":"permission","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`,
		`c2s {"id":"permission","result":{"decision":"accept"}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, "hello")
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	s.Require().NotNil(e.Interaction)
	waiting, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = session.Next(waiting) }()
	// Allow the receive loop to enter its blocking read. This deadline is only
	// a test coordination window; the lookup itself has no native I/O.
	time.Sleep(20 * time.Millisecond)
	authority := make(chan rt.ResponseHandle, 1)
	go func() { authority <- session.ResponseHandle(e.Interaction.Ref) }()
	select {
	case handle := <-authority:
		s.Require().NotNil(handle)
		s.NoError(handle.Respond(ctx, rt.Response{Allow: true}))
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-finished
		s.FailNow("authority lookup blocked behind event read")
	}
	cancel()
	<-finished
}
