package integration_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
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
	s.codexUnsupportedNativeContent("native-image", []string{"userMessage"})
}

func (s *failureSuite) TestCodexUnsupportedNativeFutureContent() {
	s.codexUnsupportedNativeContent("native-unknown", []string{"userMessage", "futureNativeItem"})
}

func (s *failureSuite) codexUnsupportedNativeContent(scenario string, itemTypes []string) {
	recorder := tracetest.NewSpanRecorder()
	traceProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	s.T().Cleanup(func() { s.NoError(traceProvider.Shutdown(context.Background())) })
	s.reg = runtime.NewRegistry(map[string]runtime.Factory{
		"codex": codex.Factory{RecoveryDir: s.T().TempDir(), TracerProvider: traceProvider},
	})
	s.start("codex", scenario, s.st)
	ref := s.open("unsupported")
	handler, err := transport.New(s.svc, withTestWriteTimeout(transport.Options{}))
	s.Require().NoError(err)
	server := httptest.NewServer(handler)
	defer server.Close()
	c := newHTTPClient(s.T(), server.URL, "")
	observer, err := c.Subscribe(ctx(s.T()), ref)
	s.Require().NoError(err)
	defer observer.Close()
	s.Require().NoError(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, nil))
	observation := s.sessionObservation(ref)
	diagnostics := 0
	var answered bool
	for {
		e, err := observer.Next(ctx(s.T()))
		s.Require().NoError(err)
		s.Require().NotEqual("user.message", e.Type(), "a mixed supported/unsupported message must not be partially emitted")
		switch problem := e.Payload().(type) {
		case events.SessionError:
			s.Equal("unsupported_native_item", problem.Reason)
			diagnostics++
		case events.AgentMessage:
			s.Equal([]events.ContentBlock{events.Text("complete answer")}, problem.Content)
			answered = true
		case events.StateUpdate:
			meta, _ := e.Meta["codex"].(map[string]any)
			if meta["method"] == "turn/completed" {
				s.Require().NotNil(problem.State)
				s.Equal(events.Idle, *problem.State)
				goto settled
			}
		}
	}
settled:
	s.Equal(len(itemTypes), diagnostics)
	s.True(answered)
	select {
	case <-observation.Done():
		s.FailNow("unsupported item ended the original observation", "%v", observation.Err())
	default:
	}
	s.Require().NoError(observation.Err())
	s.Require().Eventually(func() bool {
		var current resource.Session
		return c.GetSession(ctx(s.T()), ref, &current) == nil && current.Status.State == events.Idle
	}, time.Second, time.Millisecond)

	var tracedTypes []string
	for _, span := range recorder.Ended() {
		if span.Name() != "codex.unsupported_native_item" {
			continue
		}
		var itemType string
		for _, attr := range span.Attributes() {
			if attr.Key == "codex.item.type" {
				itemType = attr.Value.AsString()
			}
		}
		tracedTypes = append(tracedTypes, itemType)
		s.ElementsMatch([]attribute.KeyValue{
			attribute.String("reason", "unsupported_native_item"),
			attribute.String("codex.method", "item/completed"),
			attribute.String("codex.item.type", itemType),
		}, span.Attributes())
		s.Empty(span.Events())
	}
	s.Equal(itemTypes, tracedTypes)

	var history store.HistoryPage
	s.Require().NoError(c.History(ctx(s.T()), ref, store.HistoryQuery{}, &history))
	for _, e := range history.Events {
		s.NotEqual("user.message", e.Type(), "no lossy partial text in public history")
	}
	_, err = s.svc.CloseSession(ctx(s.T()), ref)
	s.Require().NoError(err)
	_, err = s.svc.StopRuntime(ctx(s.T()), "agent")
	s.Require().NoError(err)
}
