package client_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"resty.dev/v3"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/client/generated"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type ClientSuite struct{ suite.Suite }

type framingSuite struct{ suite.Suite }

func (s *ClientSuite) TestJSONResponseKeepsDecompressionAndTimeoutCleanup() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		fmt.Fprint(writer, `{"large":9007199254740993}`)
		writer.Close()
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL, "")
	var observedResponse *resty.Response
	c.HTTP.SetTimeout(time.Minute).SetHeader("Accept-Encoding", "gzip").
		AddResponseMiddleware(func(_ *resty.Client, response *resty.Response) error {
			observedResponse = response
			return nil
		})
	var result map[string]any
	s.Require().NoError(c.GetRuntime(s.T().Context(), "agent", &result))
	s.Equal(json.Number("9007199254740993"), result["large"])
	s.Require().NotNil(observedResponse)
	requestContext := observedResponse.RawResponse.Request.Context()
	s.ErrorIs(requestContext.Err(), context.Canceled, "closing the wrapped body must release the request timer")
}

func (s *ClientSuite) TestObservationRetainsTimeoutUntilBodyClose() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL, "")
	var observedResponse *resty.Response
	c.HTTP.SetTimeout(time.Minute).AddResponseMiddleware(func(_ *resty.Client, response *resty.Response) error {
		observedResponse = response
		return nil
	})
	observer, err := c.Observe(s.T().Context(), resource.SessionRef{SessionID: "synthetic"})
	s.Require().NoError(err)
	defer observer.Close()
	s.Require().NotNil(observedResponse)
	requestContext := observedResponse.RawResponse.Request.Context()
	s.NoError(requestContext.Err(), "receiving headers must not cancel a live SSE stream")
	s.Require().NoError(observer.Close())
	s.ErrorIs(requestContext.Err(), context.Canceled)
}

func (s *ClientSuite) TestCloseRunsRestyCleanupOnce() {
	c := newHTTPClient(s.T(), "http://localhost", "")
	var closed atomic.Int32
	c.HTTP.OnClose(func() { closed.Add(1) })
	s.Require().NoError(c.Close())
	s.Require().NoError(c.Close())
	s.Equal(int32(1), closed.Load())
}

func TestFramingSuite(t *testing.T) { suite.Run(t, new(framingSuite)) }
func (s *framingSuite) TestTermination() {
	for _, kind := range []string{"disconnect", "end", "error"} {
		s.Run(kind, func() {
			// Protocol-only frames; these are not fabricated Agent facts.
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
				if kind == "end" {
					fmt.Fprint(w, "event: subscription.closed\ndata: {}\n\n")
				}
				if kind == "error" {
					fmt.Fprint(w, `event: subscription.error
data: {"code":"outcome_unknown","reason":"operation_unconfirmed","message":"Operation failed"}

`)
				}
			}))
			defer h.Close()
			o, e := newHTTPClient(s.T(), h.URL, "").Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
			s.Require().NoError(e)
			_, first := o.Next()
			s.Require().Error(first)
			switch kind {
			case "disconnect":
				s.Require().ErrorIs(first, io.ErrUnexpectedEOF)
				s.Require().NotErrorIs(first, io.EOF)
			case "end":
				s.Require().ErrorIs(first, io.EOF)
			case "error":
				var v *transport.Error
				s.Require().ErrorAs(first, &v)
			}
			s.Require().NoError(o.Close())
			_, again := o.Next()
			s.Equal(first, again, "abnormal result survives close and further next")
		})
	}
}

func (s *framingSuite) TestCloseReleasesBlockedNext() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer h.Close()
	o, e := newHTTPClient(s.T(), h.URL, "").Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
	s.Require().NoError(e)
	done := make(chan error, 1)
	go func() { _, e := o.Next(); done <- e }()
	s.Require().NoError(o.Close())
	s.Require().ErrorIs(<-done, io.EOF)
}

func (s *ClientSuite) TestAuthenticatedRESTClientAndNoReplay() {
	t := s.T()
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		if r.Method != http.MethodGet || r.URL.Path != "/apis/v1/runtimes" {
			t.Error("not a REST request")
		}
		w.Header().Set("Location", "/replayed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL, "test-token")
	err := c.ListRuntimes(context.Background(), resource.RuntimeQuery{}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("redirect was not rejected: %v, %d", err, calls.Load())
	}
}

func (s *ClientSuite) TestMutationDoesNotRetryOrFollowRedirects() {
	for _, status := range []int{0, http.StatusServiceUnavailable, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		s.Run(strconv.Itoa(status), func() {
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				s.Equal(http.MethodPost, r.Method)
				s.Zero(r.ContentLength, "bodyless actions must stay bodyless")
				s.Empty(r.TransferEncoding)
				if status == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if s.NoError(err) {
						conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "/replayed")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"code":"unavailable","reason":"synthetic","message":"Unavailable"}`)
			}))
			defer h.Close()
			err := newHTTPClient(s.T(), h.URL, "").StartRuntime(s.T().Context(), "agent", nil)
			s.Require().Error(err)
			if status != 0 {
				var public *runtime.OperationError
				s.Require().ErrorAs(err, &public)
				s.Equal("unavailable", public.Code)
				s.Equal("synthetic", public.Reason)
			}
			s.Equal(int32(1), calls.Load())
		})
	}
}

func (s *ClientSuite) TestCustomHTTPTransportAndTimeoutArePreserved() {
	h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/v1/runtimes/slow" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL, "")
	c.HTTP.SetTransport(h.Client().Transport).SetTimeout(time.Second)
	s.Require().NoError(c.ListRuntimes(s.T().Context(), resource.RuntimeQuery{}, nil))
	s.Require().ErrorIs(c.GetRuntime(s.T().Context(), "slow", nil), context.DeadlineExceeded)
	s.Equal(time.Second, c.HTTP.Timeout())
}

func (s *ClientSuite) TestSharedRestyClientServesJSONWhileObserving() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("Bearer test-token", r.Header.Get("Authorization"))
		s.Equal("configured", r.Header.Get("X-Client"))
		if r.URL.Path == "/prefix/apis/v1/runtimes" {
			s.Equal("application/json", r.Header.Get("Accept"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[]`)
			return
		}
		s.Equal("/prefix/apis/v1/sessions/a%2Fb/events/stream", r.URL.EscapedPath())
		s.Equal("text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL+"/prefix", "test-token")
	var calls atomic.Int32
	c.HTTP.SetHeader("X-Client", "configured").AddRequestMiddleware(func(rc *resty.Client, _ *resty.Request) error {
		s.Same(c.HTTP, rc)
		calls.Add(1)
		return nil
	})
	observer, err := c.Observe(s.T().Context(), resource.SessionRef{SessionID: "a/b"})
	s.Require().NoError(err)
	defer observer.Close()
	results := make(chan error, 4)
	for range 4 {
		go func() { results <- c.ListRuntimes(s.T().Context(), resource.RuntimeQuery{}, nil) }()
	}
	for range 4 {
		s.Require().NoError(<-results)
	}
	s.Equal(int32(5), calls.Load())
	s.Require().NoError(observer.Close())
}

func (s *ClientSuite) TestObserverIgnoresHeartbeatAndPreservesError() {
	t := s.T()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.EscapedPath(), "/a%2Fb/events/stream") || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("incorrect observation request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n: heartbeat\n\nevent: subscription.error\ndata: {\"code\":\"unavailable\",\"reason\":\"shutdown\",\"message\":\"Observation ended\"}\n\n")
	}))
	defer h.Close()
	o, err := newHTTPClient(s.T(), h.URL, "test-token").Observe(context.Background(), resource.SessionRef{SessionID: "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	_, err = o.Next()
	var public *transport.Error
	if !errors.As(err, &public) || public.Reason != "shutdown" {
		t.Fatalf("heartbeat/error handling: %v", err)
	}
}

func (s *ClientSuite) TestRESTCallPreservesNumbersAndExplicitNull() {
	t := s.T()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.EscapedPath() != "/apis/v1/sessions/a%2Fb/interactions/Li4/answer" || r.Method != http.MethodPost {
			t.Error("incorrect answer route")
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "null" {
			t.Error("lost null answer")
		}
		fmt.Fprint(w, `{"interaction":"..","evidence":{"sent":true},"large":9007199254740993}`)
	}))
	defer h.Close()
	var out map[string]any
	err := newHTTPClient(s.T(), h.URL, "").Answer(context.Background(), resource.SessionRef{SessionID: "a/b"}, "..", nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out["large"] != json.Number("9007199254740993") {
		t.Fatal("lost number precision")
	}
}

func TestClientSuite(t *testing.T) { suite.Run(t, new(ClientSuite)) }

func (s *ClientSuite) TestGeneratedRuntimeRoundTrip() {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("/apis/v1/runtimes", r.URL.Path)
		s.Equal("POST", r.Method)
		b, err := io.ReadAll(r.Body)
		if !s.NoError(err) {
			return
		}
		s.JSONEq(`{"metadata":{"name":"test","namespace":""},"spec":{"type":"codex","config":{"large":9007199254740993,"nested":[null,true]}}}`, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"spec":{"config":{"large":9007199254740993,"nested":[null,true]}}}`)
	}))
	defer h.Close()
	var out map[string]any
	err := newHTTPClient(s.T(), h.URL, "").CreateRuntime(context.Background(), resource.Runtime{Metadata: resource.Metadata{Name: "test", UID: "must-not-send"}, Spec: resource.RuntimeSpec{Type: "codex", Config: map[string]any{"large": json.Number("9007199254740993"), "nested": []any{nil, true}}}}, &out)
	s.Require().NoError(err)
	s.Equal(json.Number("9007199254740993"), out["spec"].(map[string]any)["config"].(map[string]any)["large"])
}

func (s *ClientSuite) TestGeneratedResourceWorkflow() {
	var paths []string
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/apis/v1/sessions" {
			w.WriteHeader(http.StatusCreated)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer h.Close()
	c := newHTTPClient(s.T(), h.URL, "")
	ctx := context.Background()
	ref := resource.SessionRef{SessionID: "a/b% 空"}
	s.Require().NoError(c.GetRuntime(ctx, "agent", nil))
	s.Require().NoError(c.ListRuntimes(ctx, resource.RuntimeQuery{}, nil))
	s.Require().NoError(c.UpdateRuntime(ctx, "agent", resource.RuntimeSpec{Type: "codex", Config: runtime.Config{}}, nil))
	s.Require().NoError(c.StartRuntime(ctx, "agent", nil))
	s.Require().NoError(c.StopRuntime(ctx, "agent", nil))
	s.Require().NoError(c.DeleteRuntime(ctx, "agent"))
	s.Require().NoError(c.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "demo", Name: "test"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}}, nil))
	s.Require().NoError(c.GetSession(ctx, ref, nil))
	s.Require().NoError(c.ListSessions(ctx, resource.SessionQuery{Limit: 2}, nil))
	s.Require().NoError(c.SendPrompt(ctx, ref, runtime.Input{}, nil))
	s.Require().NoError(c.Cancel(ctx, ref, nil))
	s.Require().NoError(c.ArchiveSession(ctx, ref, nil))
	s.Require().NoError(c.ResumeSession(ctx, ref, nil))
	s.Require().NoError(c.History(ctx, ref, store.HistoryQuery{Limit: 2, Types: []string{"agent.message", "user.message"}}, nil))
	s.Contains(paths, "GET /apis/v1/sessions/a%2Fb%25%20%E7%A9%BA")
	s.Contains(paths, "GET /apis/v1/sessions/a%2Fb%25%20%E7%A9%BA/events?limit=2&types=agent.message&types=user.message")
}

func (s *framingSuite) TestLargeFrameHasNoInventedLimit() {
	text := strings.Repeat("x", 17<<20)
	raw := `{"eventId":"10000000-0000-4000-8000-000000000001","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:00Z","itemId":"message","type":"agent.message","payload":{"messageId":"message","content":[{"type":"text","text":"` + text + `"}]}}`
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "event: session.event\ndata: %s\n\nevent: subscription.closed\ndata: {}\n\n", raw)
	}))
	defer h.Close()
	observer, err := newHTTPClient(s.T(), h.URL, "").Observe(context.Background(), resource.SessionRef{SessionID: "test"})
	s.Require().NoError(err)
	defer observer.Close()
	event, err := observer.Next()
	s.Require().NoError(err)
	s.Equal(text, event.Payload().(events.AgentMessage).Content[0].Text)
	_, err = observer.Next()
	s.ErrorIs(err, io.EOF)
}

func (s *ClientSuite) TestGeneratedModelsPreserveOptionalNullAndLargeOpenNumbers() {
	var call generated.ToolCall
	raw := `{"id":"tool","kind":"execute","rawInput":{"n":9007199254740993},"rawOutput":null}`
	s.Require().NoError(json.Unmarshal([]byte(raw), &call))
	encoded, err := json.Marshal(call)
	s.Require().NoError(err)
	s.JSONEq(raw, string(encoded))
}

func (s *ClientSuite) TestAnswersRejectValuesThatJSONWouldSilentlyRewrite() {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) }))
	defer h.Close()
	for _, v := range []any{string([]byte{0xff}), []byte{1, 2}, struct {
		Hidden string `json:"-"`
	}{Hidden: "lost"}} {
		err := newHTTPClient(s.T(), h.URL, "").Answer(context.Background(), resource.SessionRef{SessionID: "id"}, "interaction", v, nil)
		s.Require().Error(err)
	}
	s.Zero(calls.Load())
}

func (s *ClientSuite) TestPromptRejectsInvalidUTF8BeforeSending() {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) }))
	defer h.Close()
	err := newHTTPClient(s.T(), h.URL, "").SendPrompt(s.T().Context(), resource.SessionRef{SessionID: "test"}, runtime.Input{Content: []events.ContentBlock{events.Text(string([]byte{0xff}))}}, nil)
	var public *transport.Error
	s.Require().ErrorAs(err, &public)
	s.Equal("invalid_argument", public.Code)
	s.Zero(calls.Load())
}

// newHTTPClient binds client resource cleanup to the current test.
func newHTTPClient(t *testing.T, serverURL, token string) *client.Client {
	t.Helper()
	c := client.New(serverURL, token)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close HTTP client: %v", err)
		}
	})
	return c
}
