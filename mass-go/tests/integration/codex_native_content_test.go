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
	s.codexUnsupportedNativeContent("native-image", []codexDiagnostic{
		{itemID: "user-1", nativeType: "userMessage", nativeSubtype: "localImage"},
	})
}

func (s *failureSuite) TestCodexUnsupportedNativeFutureContent() {
	s.codexUnsupportedNativeContent("native-unknown", []codexDiagnostic{
		{itemID: "user-1", nativeType: "userMessage", nativeSubtype: "futureUserContent"},
		{itemID: "future-1", nativeType: "futureNativeItem"},
	})
}

type codexDiagnostic struct {
	itemID        string
	nativeType    string
	nativeSubtype string
}

func (s *failureSuite) codexUnsupportedNativeContent(scenario string, wantDiagnostics []codexDiagnostic) {
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
	observation := s.sessionObservation(ref)
	s.Require().NoError(c.SendPrompt(ctx(s.T()), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, nil))
	var diagnostics []events.SessionEvent
	var answered bool
	for {
		e, err := observer.Next(ctx(s.T()))
		s.Require().NoError(err)
		s.Equal(ref.SessionID, e.SessionID)
		s.Require().NotEqual("user.message", e.Type(), "a mixed supported/unsupported message must not be partially emitted")
		switch problem := e.Payload().(type) {
		case events.SessionError:
			s.Equal("unsupported_native_event", problem.Reason)
			s.NotEmpty(problem.Message)
			diagnostics = append(diagnostics, e)
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
	s.Require().Len(diagnostics, len(wantDiagnostics))
	s.True(answered)
	select {
	case <-observation.Done():
		s.FailNow("unsupported item ended the original observation", "%v", observation.Err())
	default:
	}
	s.Require().NoError(observation.Err())
	s.Require().Eventually(func() bool {
		var current resource.Session
		return c.GetSession(ctx(s.T()), ref, &current) == nil &&
			current.Status.SessionID == ref.SessionID && current.Status.State == events.Idle
	}, time.Second, time.Millisecond)

	var diagnosticSpans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		s.NotEqual("codex.unsupported_native_item", span.Name())
		if span.Name() == "agent.unsupported_native_event" {
			diagnosticSpans = append(diagnosticSpans, span)
		}
	}
	s.Require().Len(diagnosticSpans, len(wantDiagnostics))
	for i, want := range wantDiagnostics {
		event := diagnostics[i]
		s.Equal(want.itemID, event.ItemID)
		meta := event.Meta["codex"].(map[string]any)
		s.Equal("item/completed", meta["method"])
		item := meta["raw"].(map[string]any)["params"].(map[string]any)["item"].(map[string]any)
		s.Equal(want.nativeType, item["type"])

		wantAttrs := []attribute.KeyValue{
			attribute.String("provider", "codex"),
			attribute.String("session.id", ref.SessionID),
			attribute.String("native.method", "item/completed"),
			attribute.String("native.type", want.nativeType),
		}
		if want.nativeSubtype != "" {
			wantAttrs = append(wantAttrs, attribute.String("native.subtype", want.nativeSubtype))
		}
		s.ElementsMatch(wantAttrs, diagnosticSpans[i].Attributes())
		s.Empty(diagnosticSpans[i].Events())
	}

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
