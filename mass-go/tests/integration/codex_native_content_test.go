package integration_test

import (
	"encoding/json"
	"net/http/httptest"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func (s *entrypointSuite) TestCodexNativeUserContent() {
	ref, _, _ := s.open("codex", "native-content")
	_, scan := s.observer(ref)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}}, nil)
	var user events.SessionEvent
	completed := false
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		s.Require().NotEqual("session.error", e.Type(), "native text must not terminate observation")
		switch p := e.Payload().(type) {
		case events.UserMessage:
			s.Require().Empty(user.EventID, "started is not a second complete user fact")
			s.Equal("user-1", p.MessageID)
			s.Equal([]events.ContentBlock{events.Text("hello")}, p.Content)
			user = e
		case events.AgentMessage:
			s.Equal([]events.ContentBlock{events.Text("complete answer")}, p.Content)
			completed = true
		case events.StateUpdate:
			if p.State != nil && *p.State == events.Idle {
				s.Require().True(completed, "observation must reach native output and completion")
				goto settled
			}
		}
	}
	s.Require().NoError(scan.Err())
	s.FailNow("missing native completion")
settled:
	s.Require().NotEmpty(user.EventID)
	native, err := json.Marshal(user.Meta["native"])
	s.Require().NoError(err)
	s.Contains(string(native), `"text_elements":[{"byteRange":{"end":5,"start":0},"placeholder":"hello"}]`)
	s.Contains(string(native), `"futureNative":{"big":9007199254740993,"nullable":null}`)
	history := s.history(ref)
	found := false
	for _, e := range history.Events {
		if e.EventID == user.EventID {
			found = true
			s.Equal(user.Payload(), e.Payload())
			s.Equal(user.Meta, e.Meta, "native provenance must survive persistence and public history")
		}
	}
	s.True(found, "live user fact must be available to late history readers")
	s.call("session.close", workflowParams{Session: ref}, nil)
	s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
}

func (s *failureSuite) TestCodexUnsupportedNativeImage() {
	s.codexUnsupportedNativeContent("native-image")
}

func (s *failureSuite) TestCodexUnsupportedNativeFutureContent() {
	s.codexUnsupportedNativeContent("native-unknown")
}

func (s *failureSuite) codexUnsupportedNativeContent(scenario string) {
	s.start("codex", scenario, s.st)
	s.unknown = true
	ref := s.open("unsupported")
	handler, err := transport.New(s.svc, transport.Options{LocalOnly: true})
	s.Require().NoError(err)
	server := httptest.NewServer(handler)
	defer server.Close()
	c := newHTTPClient(s.T(), server.URL, "")
	observer, err := c.Observe(ctx(s.T()), ref)
	s.Require().NoError(err)
	defer observer.Close()
	s.Require().NoError(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, nil))
	failed := false
	for {
		e, err := observer.Next()
		s.Require().NoError(err)
		s.Require().NotEqual("user.message", e.Type(), "a mixed supported/unsupported message must not be partially emitted")
		if _, ok := e.Payload().(events.SessionError); ok {
			failed = true
		}
		if p, ok := e.Payload().(events.StateUpdate); ok && p.State == nil {
			s.Require().True(failed, "unsupported native content must fail visibly, not disappear")
			break
		}
	}
	var current resource.Session
	s.Require().Eventually(func() bool {
		return c.GetSession(ctx(s.T()), ref, &current) == nil && current.Status.State == events.Unknown
	}, 2*time.Second, time.Millisecond, "asynchronous state handler must settle the failed observation")
	s.Require().Error(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not resend")}}, nil))
	var history store.HistoryPage
	s.Require().NoError(c.History(ctx(s.T()), ref, store.HistoryQuery{}, &history))
	for _, e := range history.Events {
		s.NotEqual("user.message", e.Type(), "no lossy partial text in public history")
	}
}
