package codex_test

import (
	"context"
	"encoding/json"
	"github.com/zoumo/mass/mass-go/internal/testutil"
	"strings"
	"sync"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *runtimeSuite) TestRuntimeResponseAuthority() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"id":"private-request","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`, `c2s {"id":"private-request","result":{"decision":"accept"}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	wire, err := json.Marshal(e)
	s.Require().NoError(err)
	s.NotContains(string(wire), "private-request")
	var replay rt.Event
	s.Require().NoError(json.Unmarshal(wire, &replay))
	ref := replay.Interaction.Ref
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = session.Respond(cancelled, ref, rt.Response{Decision: "allow_once"})
	s.ErrorIs(err, context.Canceled)
	r, err := session.Respond(ctx, ref, rt.Response{Decision: "allow_always"})
	s.Error(err)
	s.Equal(rt.Known(false), r.HandedToSDK)
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := session.Respond(ctx, ref, rt.Response{Decision: "allow_once"}); outcomes <- err })
	}
	wg.Wait()
	close(outcomes)
	winners := 0
	for err := range outcomes {
		if err == nil {
			winners++
		}
	}
	s.Equal(1, winners)
}

func (s *runtimeSuite) TestEventMutationCannotBroadenAuthority() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"id":77,"method":"item/tool/requestUserInput","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"question","isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Pick?","isOther":false,"isSecret":true,"options":[{"label":"A","description":"first"}]}]}}`, `c2s {"id":77,"result":{"answers":{"q":{"answers":["A"]}}}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	s.Equal("first", e.Interaction.Questions[0].Options[0].Description)
	s.Require().NotNil(e.Interaction.Questions[0].Secret)
	s.True(*e.Interaction.Questions[0].Secret)
	*e.Interaction.Questions[0].FreeText = true
	r, err := session.Respond(ctx, e.Interaction.Ref, rt.Response{Answers: map[string]rt.Answer{"q": {Text: strptr("arbitrary")}}})
	s.Error(err)
	s.Equal(rt.Known(false), r.HandedToSDK)
	_, err = session.Respond(ctx, e.Interaction.Ref, rt.Response{Answers: map[string]rt.Answer{"q": {Selections: []string{"A"}}}})
	s.Require().NoError(err)
}

func (s *runtimeSuite) TestTaggedNativeIDsRemainDistinctAcrossSessions() {
	second := strings.ReplaceAll(startup()[4], `"id":2`, `"id":3`)
	second = strings.ReplaceAll(second, "native-thread", "other-thread")
	frames := append(startup(), `c2s {"id":3,"method":"thread/start","params":{"ephemeral":true}}`, second)
	frames = append(frames, strings.ReplaceAll(turnStart()[0], `"id":3`, `"id":4`), strings.ReplaceAll(turnStart()[1], `"id":3`, `"id":4`),
		`s2c {"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`,
		`c2s {"id":5,"method":"turn/start","params":{"threadId":"other-thread","input":[{"type":"text","text":"hello","text_elements":[]}]}}`,
		`s2c {"id":5,"result":{"turn":{"id":"other-turn","status":"inProgress","items":[],"error":null}}}`,
		`s2c {"id":"7","method":"item/commandExecution/requestApproval","params":{"threadId":"other-thread","turnId":"other-turn","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`,
		`c2s {"id":"7","result":{"decision":"accept"}}`, `c2s {"id":7,"result":{"decision":"decline"}}`)
	worker, ctx := s.connect(frames)
	a := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	b := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	for _, session := range []rt.Session{a, b} {
		_, err := session.Submit(ctx, rt.TextInput("hello"))
		s.Require().NoError(err)
		_, err = session.Next(ctx)
		s.Require().NoError(err)
	}
	ea := s.nextMapped(a, ctx)
	eb := s.nextMapped(b, ctx)
	s.NotEqual(ea.Interaction.Ref, eb.Interaction.Ref)
	for _, tc := range []struct {
		session rt.Session
		ref     string
	}{{a, eb.Interaction.Ref}, {b, ea.Interaction.Ref}} {
		r, err := tc.session.Respond(ctx, tc.ref, rt.Response{Decision: "allow_once"})
		s.Error(err)
		s.Equal(rt.Known(false), r.HandedToSDK)
	}
	_, err := b.Respond(ctx, eb.Interaction.Ref, rt.Response{Decision: "allow_once"})
	s.Require().NoError(err)
	_, err = a.Respond(ctx, ea.Interaction.Ref, rt.Response{Decision: "deny_once"})
	s.Require().NoError(err)
}

func (s *runtimeSuite) TestNativeDecisionRestrictionsCannotBroadenPermission() {
	frames := append(startup(), turnStart()...)
	frames = append(frames, `s2c {"id":"restricted","method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool","startedAtMs":1,"command":"synthetic","availableDecisions":["decline","acceptForSession"]}}`, `c2s {"id":"restricted","result":{"decision":"decline"}}`)
	_, session, ctx := s.open(frames)
	_, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	e := s.nextMapped(session, ctx)
	s.Equal([]string{"deny_once"}, e.Interaction.Decisions)
	r, err := session.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
	s.ErrorIs(err, rt.ErrUnsupported)
	s.Equal(rt.Known(false), r.HandedToSDK)
	_, err = session.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "deny_once"})
	s.Require().NoError(err)
}
