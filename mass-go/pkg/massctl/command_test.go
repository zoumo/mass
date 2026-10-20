package massctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/massctl"
)

type CommandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(CommandSuite)) }
func (s *CommandSuite) TestRuntimeCreateUsesResourceHTTP() {
	s.T().Setenv("MASS_TOKEN", "test-token")
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal("POST", r.Method)
		s.Equal("/apis/v1/runtimes", r.URL.EscapedPath())
		s.Equal("Bearer test-token", r.Header.Get("Authorization"))
		var v any
		s.NoError(json.NewDecoder(r.Body).Decode(&v))
		b, _ := json.Marshal(v)
		s.JSONEq(`{"metadata":{"name":"agent","namespace":""},"spec":{"type":"codex","config":{}}}`, string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}},"status":{"state":"stopped"}}`))
	}))
	defer h.Close()
	var out, stderr bytes.Buffer
	s.Equal(0, massctl.Run(s.T().Context(), []string{"--url", h.URL, "-o", "json", "runtime", "create", "agent", "--type", "codex"}, strings.NewReader(""), &out, &stderr), stderr.String())
	s.Contains(out.String(), `"stopped"`)
	s.Empty(stderr.String())
}

func (s *CommandSuite) TestUpdatePreservesTypeAndConfigFlagsOrReplacesCompleteConfig() {
	for _, input := range [][]string{{"--executable", "new"}, {"--config-json", `{"executable":"new","large":9007199254740993}`}} {
		s.Run(strings.Join(input, " "), func() {
			methods := []string{}
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				s.Equal("/apis/v1/runtimes/agent", r.URL.EscapedPath())
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"spec":{"type":"claude","config":{"executable":"old","retained":true}}}`))
					return
				}
				var body json.RawMessage
				s.NoError(json.NewDecoder(r.Body).Decode(&body))
				expected := `{"type":"claude","config":{"executable":"new","retained":true}}`
				if input[0] == "--config-json" {
					expected = `{"type":"claude","config":{"executable":"new","large":9007199254740993}}`
				}
				s.JSONEq(expected, string(body))
				_, _ = w.Write([]byte(`{"status":{"state":"running"}}`))
			}))
			defer h.Close()
			var out, stderr bytes.Buffer
			args := append([]string{"--url", h.URL, "runtime", "update", "agent"}, input...)
			s.Equal(0, massctl.Run(s.T().Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			s.Equal([]string{"GET", "PUT"}, methods)
		})
	}
}

func (s *CommandSuite) TestInvalidInputNeverDispatches() {
	cases := [][]string{
		{"runtime", "create", "a", "--type", "codex", "--config-json", "{\"executable\":\"\xff\"}"},
		{"--op", "runtime.list"},
		{"runtime", "create", "a"},
		{"runtime", "update", "a"},
		{"runtime", "create", "a", "--type", "codex", "--executable", "x", "--config-json", "{}"},
		{"runtime", "create", "a", "--type", "codex", "--config-json", "{} {}"},
		{"session", "list", "--limit", "0"},
		{"session", "list", "-n", "demo"},
		{"session", "get", "a", "-n", ""},
		{"session", "get", ".."},
		{"session", "prompt", "a"},
		{"session", "create", "a", "-n", "demo", "--runtime", "a"},
		{"session", "get", "a", "-n", "demo", "--namespace", "other"},
		{"session", "list", "--limit", "1", "--limit", "2"},
		{"runtime", "list", "extra"},
		{"session", "answer", "a"},
		{"completion"},
		{"runtime", "create", "a", "--type", "codex", "--scenario", "x"},
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Fail("invalid input dispatched", r.URL.String()) }))
	defer h.Close()
	for _, args := range cases {
		s.Run(strings.Join(args, " "), func() {
			var out, stderr bytes.Buffer
			s.Equal(2, massctl.Run(s.T().Context(), append([]string{"--url", h.URL, "-o", "json"}, args...), strings.NewReader(""), &out, &stderr), stderr.String())
			s.Empty(out.String())
			var e map[string]string
			s.Require().NoError(json.Unmarshal(stderr.Bytes(), &e))
			s.Equal("invalid_argument", e["code"])
		})
	}
}

func (s *CommandSuite) TestAddressingAndPagination() {
	for _, test := range []struct {
		args        []string
		path, query string
	}{
		{[]string{"session", "get", "a/b%2F ?#"}, "/apis/v1/sessions/a%2Fb%252F%20%3F%23", ""},
		{[]string{"session", "history", "task", "-n", "demo", "--after", "a+/="}, "/apis/v1/namespaces/demo/sessions/task/events", "after=a%2B%2F%3D&limit=100"},
		{[]string{"session", "list"}, "/apis/v1/sessions", "limit=100"},
	} {
		s.Run(strings.Join(test.args, " "), func() {
			calls := 0
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				s.Equal(test.path, r.URL.EscapedPath())
				s.Equal(test.query, r.URL.RawQuery)
				_, _ = w.Write([]byte(`{"items":[],"next":"opaque"}`))
			}))
			defer h.Close()
			s.T().Setenv("MASS_URL", h.URL)
			var out, stderr bytes.Buffer
			s.Equal(0, massctl.Run(s.T().Context(), test.args, strings.NewReader(""), &out, &stderr), stderr.String())
			s.Equal(1, calls)
			s.Contains(out.String(), "opaque")
		})
	}
}

func (s *CommandSuite) TestWatchReadyTimeoutEndsAtReadyAndInterruptOnlyClosesSubscription() {
	for _, mode := range []string{"closed", "eof", "error", "interrupt", "no-ready"} {
		s.Run(mode, func() {
			ready := make(chan struct{})
			release := make(chan struct{})
			requests := make(chan string, 4)
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method + " " + r.URL.EscapedPath()
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "no-ready" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				_, _ = w.Write([]byte("event: subscription.ready\ndata: {}\n\n: heartbeat\n\n"))
				w.(http.Flusher).Flush()
				close(ready)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				switch mode {
				case "closed":
					_, _ = w.Write([]byte("event: subscription.closed\ndata: {}\n\n"))
				case "error":
					_, _ = w.Write([]byte("event: subscription.error\ndata: {\"code\":\"resource_exhausted\",\"reason\":\"slow_consumer\",\"message\":\"overflow\"}\n\n"))
				case "interrupt":
					<-r.Context().Done()
				}
			}))
			defer h.Close()
			ctx, cancel := context.WithCancel(s.T().Context())
			defer cancel()
			var out bytes.Buffer
			stderr := new(readyWriter)
			done := make(chan int, 1)
			go func() {
				done <- massctl.Run(ctx, []string{"--url", h.URL, "-o", "json", "--timeout", "100ms", "session", "watch", "a/b"}, strings.NewReader(""), &out, stderr)
			}()
			if mode != "no-ready" {
				<-ready
				s.Eventually(stderr.isReady, time.Second, time.Millisecond)
				// The subscription must survive well past its establishment timeout.
				select {
				case code := <-done:
					s.FailNow("watch ended after ready", strconv.Itoa(code))
				case <-time.After(150 * time.Millisecond):
				}
				if mode == "interrupt" {
					cancel()
				}
				close(release)
			}
			code := <-done
			expected := 1
			if mode == "closed" {
				expected = 0
			}
			if mode == "interrupt" {
				expected = 130
			}
			s.Equal(expected, code)
			s.Empty(out.String())
			s.Equal("GET /apis/v1/sessions/a%2Fb/events/stream", <-requests)
			s.Empty(requests)
			if mode == "error" {
				s.Contains(stderr.String(), "slow_consumer")
			}
		})
	}
}

type readyWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *readyWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(b)
}
func (w *readyWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }
func (w *readyWriter) isReady() bool  { return strings.Contains(w.String(), "subscription.ready") }

func (s *CommandSuite) TestOutputFailureIsNotAUsageError() {
	var stderr bytes.Buffer
	s.Equal(1, massctl.Run(s.T().Context(), []string{"version"}, strings.NewReader(""), brokenWriter{}, &stderr))
	s.Contains(stderr.String(), "output_failed")
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (s *CommandSuite) TestSessionCreationAndPromptDoNotReadImplicitInput() {
	requests := []string{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.EscapedPath())
		var b json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			s.NoError(err)
			return
		}
		switch r.URL.EscapedPath() {
		case "/apis/v1/sessions":
			s.JSONEq(`{"metadata":{"namespace":"demo","name":"review"},"spec":{"runtimeRef":"agent","cwd":"/remote/work","systemPrompt":"rules\n","additionalDirs":["/remote/a","/remote/b"]}}`, string(b))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":{"sessionId":"original"}}`))
		case "/apis/v1/namespaces/demo/sessions/review/inputs":
			s.JSONEq(`{"content":[{"type":"text","text":"{raw text}"}]}`, string(b))
			_, _ = w.Write([]byte(`{"submission":"receipt","acceptance":{"sent":true,"nativeAccepted":false}}`))
		}
	}))
	defer h.Close()
	var out, stderr bytes.Buffer
	args := []string{"--url", h.URL, "-o", "json", "session", "create", "review", "-n", "demo", "--runtime", "agent", "--cwd", "/remote/work", "--system-prompt-file", "-", "--additional-dir", "/remote/a", "--additional-dir", "/remote/b"}
	s.Equal(0, massctl.Run(s.T().Context(), args, strings.NewReader("rules\n"), &out, &stderr), stderr.String())
	s.Len(requests, 1, "creation must not send prompt")
	out.Reset()
	args = []string{"--url", h.URL, "-o", "json", "session", "prompt", "review", "-n", "demo", "--text", "{raw text}"}
	s.Equal(0, massctl.Run(s.T().Context(), args, unreadableInput{}, &out, &stderr), stderr.String())
	s.JSONEq(`{"submission":"receipt","acceptance":{"sent":true,"nativeAccepted":false}}`, out.String())
}

type unreadableInput struct{}

func (unreadableInput) Read([]byte) (int, error) { panic("command unexpectedly read stdin") }

func (s *CommandSuite) TestDeleteHasEmptyJSONResultAndNeverStops() {
	methods := []string{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer h.Close()
	var out, stderr bytes.Buffer
	s.Equal(0, massctl.Run(s.T().Context(), []string{"--url", h.URL, "-o", "json", "runtime", "delete", "agent"}, unreadableInput{}, &out, &stderr), stderr.String())
	s.Empty(out.String())
	s.Empty(stderr.String())
	s.Equal([]string{"DELETE /apis/v1/runtimes/agent"}, methods)
}

func (s *CommandSuite) TestGetFailurePreventsUpdateAndPreservesPublicError() {
	calls := 0
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		s.Equal(http.MethodGet, r.Method)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","reason":"missing_runtime","message":"Runtime is absent"}`))
	}))
	defer h.Close()
	var out, stderr bytes.Buffer
	s.Equal(1, massctl.Run(s.T().Context(), []string{"--url", h.URL, "-o", "json", "runtime", "update", "agent", "--executable", "new"}, unreadableInput{}, &out, &stderr))
	s.Equal(1, calls)
	s.Empty(out.String())
	s.JSONEq(`{"code":"not_found","reason":"missing_runtime","message":"Runtime is absent"}`, stderr.String())
}

func (s *CommandSuite) TestMutationTimeoutHasUnknownOutcomeAndNoRetry() {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer h.Close()
	var out, stderr bytes.Buffer
	s.Equal(1, massctl.Run(s.T().Context(), []string{"--url", h.URL, "-o", "json", "--timeout", "20ms", "runtime", "start", "agent"}, unreadableInput{}, &out, &stderr))
	s.Empty(out.String())
	s.Contains(stderr.String(), `"code":"outcome_unknown"`)
	s.Equal(int32(1), calls.Load())
}

func (s *CommandSuite) TestHelpExplainsRequiredInputsAndExclusiveModes() {
	for _, args := range [][]string{{"--help"}, {"runtime", "--help"}, {"runtime", "create", "--help"}, {"session", "create", "--help"}} {
		var out, stderr bytes.Buffer
		s.Equal(0, massctl.Run(s.T().Context(), args, unreadableInput{}, &out, &stderr))
		s.Contains(out.String(), "Examples:")
		if len(args) > 2 {
			s.Contains(out.String(), "mutually exclusive")
		}
	}
}

func (s *CommandSuite) TestInterruptedMutationRetainsUnknownOutcome() {
	dispatched := make(chan struct{})
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(dispatched)
		<-r.Context().Done()
	}))
	defer h.Close()
	ctx, cancel := context.WithCancel(s.T().Context())
	defer cancel()
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- massctl.Run(ctx, []string{"--url", h.URL, "-o", "json", "session", "prompt", "id", "--text", "hello"}, unreadableInput{}, &out, &stderr)
	}()
	<-dispatched
	cancel()
	s.Equal(130, <-done)
	s.Empty(out.String())
	s.Contains(stderr.String(), `"code":"outcome_unknown"`)
}
