package integration_test

import (
	"encoding/json"
	"net/http/httptest"

	"github.com/zoumo/mass/mass-go/pkg/events"
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
	codexMeta, ok := user.Meta["codex"].(map[string]any)
	s.Require().True(ok)
	native, err := json.Marshal(codexMeta["raw"])
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
	handler, err := transport.New(s.svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	s.Require().NoError(err)
	server := httptest.NewServer(handler)
	defer server.Close()
	c := newHTTPClient(s.T(), server.URL, "")
	observer, err := c.Subscribe(ctx(s.T()), ref)
	s.Require().NoError(err)
	defer observer.Close()
	s.Require().NoError(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, nil))
	observation := s.sessionObservation(ref)
	select {
	case <-observation.Done():
	case <-ctx(s.T()).Done():
		s.FailNow("unsupported native content did not fail its original observation")
	}
	var operation *runtime.OperationError
	s.Require().ErrorAs(observation.Err(), &operation)
	s.Equal("unavailable", operation.Code)
	s.Equal("unsupported_native_item", operation.Reason)
	for {
		e, err := observer.Next(ctx(s.T()))
		s.Require().NoError(err)
		s.Require().NotEqual("user.message", e.Type(), "a mixed supported/unsupported message must not be partially emitted")
		if problem, ok := e.Payload().(events.SessionError); ok {
			s.Equal("unsupported_native_item", problem.Reason)
			break
		}
	}
	s.Require().Error(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must not resend")}}, nil))
	var history store.HistoryPage
	s.Require().NoError(c.History(ctx(s.T()), ref, store.HistoryQuery{}, &history))
	for _, e := range history.Events {
		s.NotEqual("user.message", e.Type(), "no lossy partial text in public history")
	}
}
