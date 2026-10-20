package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type ClientSuite struct{ suite.Suite }

type framingSuite struct{ suite.Suite }

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
			o, e := (client.Client{URL: h.URL}).Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
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
	o, e := (client.Client{URL: h.URL}).Observe(context.Background(), resource.SessionRef{SessionID: "synthetic"})
	s.Require().NoError(e)
	done := make(chan error, 1)
	go func() { _, e := o.Next(); done <- e }()
	s.Require().NoError(o.Close())
	s.Require().ErrorIs(<-done, io.EOF)
}

func (clientSuite *ClientSuite) TestAuthenticatedRESTClientAndNoReplay() {
	t := clientSuite.T()
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
	custom := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	err := (client.Client{URL: h.URL, Token: "test-token", HTTP: custom}).Call(context.Background(), "runtime.list", client.Params{}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("redirect was not rejected: %v, %d", err, calls.Load())
	}
}

func (clientSuite *ClientSuite) TestObserverIgnoresHeartbeatAndPreservesError() {
	t := clientSuite.T()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.EscapedPath(), "/a%2Fb/events/stream") || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("incorrect observation request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n: heartbeat\n\nevent: subscription.error\ndata: {\"code\":\"unavailable\",\"reason\":\"shutdown\",\"message\":\"Observation ended\"}\n\n")
	}))
	defer h.Close()
	o, err := (client.Client{URL: h.URL, Token: "test-token"}).Observe(context.Background(), resource.SessionRef{SessionID: "a/b"})
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

func (clientSuite *ClientSuite) TestRESTCallPreservesNumbersAndExplicitNull() {
	t := clientSuite.T()
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
	err := (client.Client{URL: h.URL}).CallJSON(context.Background(), "session.answer", json.RawMessage(`{"session":{"sessionId":"a/b"},"interaction":"..","answer":null}`), &out)
	if err != nil {
		t.Fatal(err)
	}
	if out["large"] != json.Number("9007199254740993") {
		t.Fatal("lost number precision")
	}
}

func TestClientSuite(t *testing.T) { suite.Run(t, new(ClientSuite)) }
