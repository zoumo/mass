package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallEnvelopeAndErrors(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"jsonrpc":"2.0","id":"massctl","result":{"ok":true}}`, ""},
		{`{"jsonrpc":"2.0","id":"massctl","error":{"code":-32603,"message":"acceptance_unknown","data":{"operationId":"op-1"}}}`, "op-1"},
		{`{"jsonrpc":"2.0","id":"other","result":{}}`, "envelope"},
		{`{"jsonrpc":"2.0","id":"massctl"}`, "envelope"},
		{`{"jsonrpc":"2.0","id":"massctl","result":{},"error":{"code":1}}`, "envelope"},
		{`not-json`, "decode"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/agentd/jsonrpc" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request["method"] != "agentd/status" || request["id"] != "massctl" {
					t.Errorf("bad envelope: %v", request)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, err := New(srv.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Call(context.Background(), "agentd/status", map[string]any{})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
			if calls != 1 {
				t.Fatalf("unexpected retry: %d", calls)
			}
		})
	}
}

func TestWatchSSE(t *testing.T) {
	large := strings.Repeat("x", 100000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Method != "agentd/session/watch_event" || len(req.Params) != 1 || req.Params["sessionName"] != "demo" {
			t.Errorf("unexpected watch: %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprintf(w, ": keepalive\r\n\r\ndata: {\"type\":\"agent.message\",\r\ndata: \"payload\":\"%s\"}\r\n\r\n", large)
	}))
	defer srv.Close()
	c, err := New(srv.URL+"/api/v1/agentd/jsonrpc", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	count := 0
	err = c.Watch(context.Background(), "demo", func(event json.RawMessage) error {
		count++
		if !strings.Contains(string(event), large) {
			t.Error("large event lost")
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("events=%d err=%v", count, err)
	}
}

func TestWatchFailureAndCancellation(t *testing.T) {
	for _, body := range []string{"data: {bad}\n\n", "data: {}\n"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, body)
		}))
		c, _ := New(srv.URL, time.Second)
		if err := c.Watch(context.Background(), "s", func(json.RawMessage) error { return nil }); err == nil {
			t.Error("expected invalid stream error")
		}
		c.Close()
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := New(srv.URL, time.Second)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Watch(ctx, "s", func(json.RawMessage) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestEndpointAndHTTPFailures(t *testing.T) {
	for _, endpoint := range []string{"ftp://host", "http://", "http://user:password@host", "http://host/#fragment"} {
		if _, err := New(endpoint, time.Second); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(status)
		}))
		c, _ := New(srv.URL, time.Second)
		if _, err := c.Call(context.Background(), "agentd/status", nil); err == nil {
			t.Error("HTTP failure hidden")
		}
		c.Close()
		srv.Close()
	}
}
