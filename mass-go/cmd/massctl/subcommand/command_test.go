package subcommand_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/cmd/massctl/subcommand"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type CommandSuite struct{ suite.Suite }

func TestCommandSuite(t *testing.T) { suite.Run(t, new(CommandSuite)) }

func (s *CommandSuite) TestRuntimeCreateUsesResourceRPC() {
	s.T().Setenv("MASS_TOKEN", "test-token")
	endpoint := newRPCService(s.T(), "test-token", func(call *rpcTestCall) {
		s.Equal("runtime.create", call.Method)
		s.JSONEq(`{"input":{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}}}}`, string(call.Params))
		call.respond(json.RawMessage(`{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}},"status":{"state":"stopped"}}`))
	})
	var out, stderr bytes.Buffer
	s.Equal(0, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-o", "json", "runtime", "create", "agent", "--type", "codex"}, strings.NewReader(""), &out, &stderr), stderr.String())
	s.Contains(out.String(), `"stopped"`)
	s.Empty(stderr.String())
}

func (s *CommandSuite) TestRuntimeHumanOutputUsesRuntimeColumnsWithoutGenerationFooter() {
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		switch call.Method {
		case "runtime.list":
			call.respond(json.RawMessage(`{"items":[]}`))
		case "runtime.start":
			call.respond(json.RawMessage(`{"metadata":{"namespace":"","name":"codex-local","generation":1},"spec":{"type":"codex"},"status":{"state":"running","appliedGeneration":1}}`))
		default:
			s.Fail("unexpected method", call.Method)
		}
	})

	for _, test := range []struct {
		args []string
		rows [][]string
	}{
		{[]string{"runtime", "start", "codex-local"}, [][]string{{"NAME", "TYPE", "STATE"}, {"codex-local", "codex", "running"}}},
		{[]string{"runtime", "list"}, [][]string{{"NAME", "TYPE", "STATE"}}},
	} {
		s.Run(strings.Join(test.args, " "), func() {
			var out, stderr bytes.Buffer
			s.Equal(0, subcommand.Run(s.T().Context(), append([]string{"--url", endpoint}, test.args...), strings.NewReader(""), &out, &stderr), stderr.String())
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			s.Len(lines, len(test.rows))
			for i, row := range test.rows {
				s.Equal(row, strings.Fields(lines[i]))
			}
			s.Empty(stderr.String())
		})
	}
}

func (s *CommandSuite) TestResourceJSONOutputIsIndented() {
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		s.Equal("runtime.get", call.Method)
		call.respond(json.RawMessage(`{"metadata":{"name":"codex-local"},"spec":{"type":"codex"},"status":{"state":"running"}}`))
	})
	var out, stderr bytes.Buffer
	s.Equal(0, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-ojson", "runtime", "get", "codex-local"}, strings.NewReader(""), &out, &stderr), stderr.String())
	s.JSONEq(`{"metadata":{"name":"codex-local"},"spec":{"type":"codex"},"status":{"state":"running"}}`, out.String())
	s.Equal([]string{
		"{",
		`  "metadata": {`,
		`    "name": "codex-local"`,
		"  },",
		`  "spec": {`,
		`    "type": "codex"`,
		"  },",
		`  "status": {`,
		`    "state": "running"`,
		"  }",
		"}",
	}, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"))
	s.Empty(stderr.String())
}

func (s *CommandSuite) TestWatchJSONOutputRemainsOneEventPerLine() {
	event := `{"eventId":"10000000-0000-4000-8000-000000000001","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:00Z","itemId":"message","type":"agent.message","payload":{"messageId":"message","content":[{"type":"text","text":"hello"}]}}`
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		s.Equal("session.subscribe", call.Method)
		call.respond(map[string]string{"subscriptionId": "1"})
		call.notify("session.event", map[string]any{"subscriptionId": "1", "event": json.RawMessage(event)})
		call.notify("subscription.closed", map[string]any{"subscriptionId": "1"})
	})
	var out, stderr bytes.Buffer
	s.Equal(0, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-ojson", "session", "watch", "test"}, strings.NewReader(""), &out, &stderr), stderr.String())
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	s.Len(lines, 1)
	s.True(json.Valid([]byte(lines[0])))
	s.JSONEq(event, lines[0])
	s.Contains(stderr.String(), "subscription.ready")
}

func (s *CommandSuite) TestWatchJSONPreservesToolIdentityAcrossFrames() {
	events := []string{
		`{"eventId":"10000000-0000-4000-8000-000000000001","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:00Z","itemId":"tool_1","type":"agent.tool_call","payload":{"id":"tool_1","kind":"read","toolName":"Read","provider":"agent","status":"in_progress","rawInput":{"path":"README.md"}}}`,
		`{"eventId":"10000000-0000-4000-8000-000000000002","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:01Z","itemId":"tool_1","type":"agent.tool_result","payload":{"id":"tool_1","kind":"read","toolName":"Read","provider":"agent","status":"completed","rawInput":{"path":"README.md"},"content":[{"type":"text","text":"hello"}]}}`,
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: subscription.ready\ndata: {}\n\n")
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "event: session.event\ndata: %s\n\n", event)
		}
		_, _ = fmt.Fprint(w, "event: subscription.closed\ndata: {}\n\n")
	}))
	defer h.Close()

	var out, stderr bytes.Buffer
	s.Equal(0, subcommand.Run(s.T().Context(), []string{"--url", h.URL, "-ojson", "session", "watch", "test"}, strings.NewReader(""), &out, &stderr), stderr.String())
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	s.Require().Len(lines, len(events))
	for i, line := range lines {
		s.JSONEq(events[i], line)
		var decoded struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		s.Require().NoError(json.Unmarshal([]byte(line), &decoded))
		s.Equal("Read", decoded.Payload["toolName"])
		s.Equal("agent", decoded.Payload["provider"])
		s.NotContains(decoded.Payload, "name")
	}
	s.Contains(stderr.String(), "subscription.ready")
}

func (s *CommandSuite) TestWatchRejectsLegacyEventSchemaDistinctly() {
	for name, event := range map[string]string{
		"plan":          `{"eventId":"10000000-0000-4000-8000-000000000001","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:00Z","type":"agent.plan","payload":{"title":"legacy"}}`,
		"tool identity": `{"eventId":"10000000-0000-4000-8000-000000000002","namespace":"demo","sessionName":"test","sessionId":"test","time":"2026-10-20T00:00:00Z","itemId":"tool_1","type":"agent.tool_call","payload":{"id":"tool_1","kind":"read","name":"Read","rawInput":{"path":"README.md"}}}`,
	} {
		s.Run(name, func() {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: subscription.ready\ndata: {}\n\nevent: session.event\ndata: %s\n\n", event)
			}))
			defer h.Close()

			var out, stderr bytes.Buffer
			s.Equal(1, subcommand.Run(s.T().Context(), []string{"--url", h.URL, "-ojson", "session", "watch", "test"}, strings.NewReader(""), &out, &stderr), stderr.String())
			s.Empty(out.String())
			var public map[string]string
			for line := range strings.SplitSeq(stderr.String(), "\n") {
				var candidate map[string]string
				if json.Unmarshal([]byte(line), &candidate) == nil && candidate["reason"] == "legacy_event_schema" {
					public = candidate
					break
				}
			}
			s.Require().NotNil(public, stderr.String())
			s.Equal("unsupported", public["code"])
			s.Equal("legacy_event_schema", public["reason"])
		})
	}
}

func (s *CommandSuite) TestUpdatePreservesTypeAndConfigFlagsOrReplacesCompleteConfig() {
	for _, input := range [][]string{{"--executable", "new"}, {"--config-json", `{"executable":"new","large":9007199254740993}`}} {
		s.Run(strings.Join(input, " "), func() {
			methods := []string{}
			endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
				methods = append(methods, call.Method)
				switch call.Method {
				case "runtime.get":
					s.JSONEq(`{"runtimeRef":"agent"}`, string(call.Params))
					call.respond(json.RawMessage(`{"spec":{"type":"claude","config":{"executable":"old","retained":true},"features":{"agentQuestions":false},"mcpServers":[{"name":"default","type":"http","url":"https://example.invalid/default"}]}}`))
				case "runtime.update":
					expected := `{"runtimeRef":"agent","spec":{"type":"claude","config":{"executable":"new","retained":true},"features":{"agentQuestions":false},"mcpServers":[{"name":"default","type":"http","url":"https://example.invalid/default"}]}}`
					if input[0] == "--config-json" {
						expected = `{"runtimeRef":"agent","spec":{"type":"claude","config":{"executable":"new","large":9007199254740993},"features":{"agentQuestions":false},"mcpServers":[{"name":"default","type":"http","url":"https://example.invalid/default"}]}}`
					}
					s.JSONEq(expected, string(call.Params))
					call.respond(json.RawMessage(`{"status":{"state":"running"}}`))
				default:
					s.Fail("unexpected method", call.Method)
				}
			})
			var out, stderr bytes.Buffer
			args := append([]string{"--url", endpoint, "runtime", "update", "agent"}, input...)
			s.Equal(0, subcommand.Run(s.T().Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
			s.Equal([]string{"runtime.get", "runtime.update"}, methods)
		})
	}
}

func (s *CommandSuite) TestRuntimeAndSessionCLIForwardSessionOptions() {
	params := make(map[string]json.RawMessage)
	var mu sync.Mutex
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		mu.Lock()
		params[call.Method] = append(json.RawMessage(nil), call.Params...)
		mu.Unlock()
		switch call.Method {
		case "runtime.create":
			call.respond(json.RawMessage(`{"status":{"state":"stopped"}}`))
		case "session.create":
			call.respond(json.RawMessage(`{"status":{"state":"unknown"}}`))
		case "session.load":
			call.respond(json.RawMessage(`{"loadId":"load","session":{"status":{"sessionId":"native"}}}`))
		default:
			s.Fail("unexpected method", call.Method)
		}
	})
	for _, args := range [][]string{
		{"--url", endpoint, "-o", "json", "runtime", "create", "agent", "--type", "codex", "--features-json", `{"agentQuestions":false}`, "--mcp-servers-json", `[{"name":"remote","type":"http","url":"https://example.invalid"}]`},
		{"--url", endpoint, "-o", "json", "session", "create", "task", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--features-json", `{"agentQuestions":true}`, "--mcp-servers-json", `[{"name":"remote","type":"stdio","command":"/mcp"}]`},
		{"--url", endpoint, "-o", "json", "session", "load", "task", "-n", "demo", "--runtime", "agent", "--cwd", "/remote", "--session-id", "native", "--features-json", `{"agentQuestions":false}`, "--mcp-servers-json", `[{"name":"remote","type":"http","url":"https://example.invalid/load"}]`},
	} {
		var out, stderr bytes.Buffer
		s.Equal(0, subcommand.Run(s.T().Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	s.Require().Len(params, 3)
	s.JSONEq(`{"input":{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{},"features":{"agentQuestions":false},"mcpServers":[{"name":"remote","type":"http","url":"https://example.invalid"}]}}}`, string(params["runtime.create"]))
	s.JSONEq(`{"input":{"metadata":{"namespace":"demo","name":"task"},"spec":{"runtimeRef":"agent","cwd":"/remote","features":{"agentQuestions":true},"mcpServers":[{"name":"remote","type":"stdio","command":"/mcp"}]}}}`, string(params["session.create"]))
	s.JSONEq(`{"input":{"metadata":{"namespace":"demo","name":"task"},"runtimeRef":"agent","cwd":"/remote","sessionId":"native","features":{"agentQuestions":false},"mcpServers":[{"name":"remote","type":"http","url":"https://example.invalid/load"}]}}`, string(params["session.load"]))
}

func (s *CommandSuite) TestRuntimeDefaultsOnlyUpdatePreservesConfiguration() {
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		switch call.Method {
		case "runtime.get":
			call.respond(json.RawMessage(`{"spec":{"type":"codex","config":{"executable":"/agent","retained":true},"features":{"agentQuestions":false},"mcpServers":[{"name":"old","type":"stdio","command":"/old"}]}}`))
		case "runtime.update":
			s.JSONEq(`{"runtimeRef":"agent","spec":{"type":"codex","config":{"executable":"/agent","retained":true},"features":{"agentQuestions":true},"mcpServers":[{"name":"old","type":"stdio","command":"/new"}]}}`, string(call.Params))
			call.respond(json.RawMessage(`{"status":{"state":"stopped"}}`))
		default:
			s.Fail("unexpected method", call.Method)
		}
	})
	var out, stderr bytes.Buffer
	args := []string{"--url", endpoint, "runtime", "update", "agent", "--features-json", `{"agentQuestions":true}`, "--mcp-servers-json", `[{"name":"old","type":"stdio","command":"/new"}]`}
	s.Equal(0, subcommand.Run(s.T().Context(), args, strings.NewReader(""), &out, &stderr), stderr.String())
}

func (s *CommandSuite) TestInvalidInputNeverDispatches() {
	var dispatched atomic.Int32
	endpoint := newRPCService(s.T(), "", func(*rpcTestCall) { dispatched.Add(1) })
	cases := [][]string{
		{"runtime", "create", "a", "--type", "codex", "--config-json", "{\"executable\":\"\xff\"}"},
		{"--op", "runtime.list"},
		{"runtime", "create", "a"},
		{"runtime", "update", "a"},
		{"runtime", "create", "a", "--type", "codex", "--executable", "x", "--config-json", "{}"},
		{"runtime", "create", "a", "--type", "codex", "--config-json", "{} {}"},
		{"runtime", "create", "a", "--type", "codex", "--features-json", "[]"},
		{"runtime", "create", "a", "--type", "codex", "--mcp-servers-json", `[{"name":"bad","type":"stdio"}]`},
		{"session", "list", "--limit", "0"},
		{"session", "list", "-n", "demo"},
		{"session", "get", "a", "-n", ""},
		{"session", "get", ".."},
		{"session", "prompt", "a"},
		{"session", "create", "a", "-n", "demo", "--runtime", "a"},
		{"session", "create", "a", "-n", "demo", "--runtime", "a", "--cwd", "/work", "--mcp-servers-json", `{}`},
		{"session", "get", "a", "-n", "demo", "--namespace", "other"},
		{"session", "list", "--limit", "1", "--limit", "2"},
		{"runtime", "list", "extra"},
		{"session", "answer", "a"},
		{"completion"},
		{"runtime", "create", "a", "--type", "codex", "--scenario", "x"},
		{"--write-timeout", "0s", "version"},
		{"--subscription-queue", "0", "version"},
	}
	for _, args := range cases {
		s.Run(strings.Join(args, " "), func() {
			var out, stderr bytes.Buffer
			s.Equal(2, subcommand.Run(s.T().Context(), append([]string{"--url", endpoint, "-o", "json"}, args...), strings.NewReader(""), &out, &stderr), stderr.String())
			s.Empty(out.String())
			var e map[string]string
			s.Require().NoError(json.Unmarshal(stderr.Bytes(), &e))
			s.Equal("invalid_argument", e["code"])
		})
	}
	s.Zero(dispatched.Load())
}

func (s *CommandSuite) TestAddressingAndPagination() {
	for _, test := range []struct {
		args     []string
		method   string
		expected string
	}{
		{[]string{"session", "get", "a/b%2F ?#"}, "session.get", `{"session":{"sessionId":"a/b%2F ?#"}}`},
		{[]string{"session", "history", "task", "-n", "demo", "--after", "a+/="}, "session.history", `{"session":{"namespace":"demo","name":"task"},"query":{"after":"a+/=","limit":100}}`},
		{[]string{"session", "list"}, "session.list", `{"query":{"page":{"limit":100}}}`},
	} {
		s.Run(strings.Join(test.args, " "), func() {
			calls := 0
			endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
				calls++
				s.Equal(test.method, call.Method)
				s.JSONEq(test.expected, string(call.Params))
				call.respond(json.RawMessage(`{"items":[],"next":"opaque"}`))
			})
			s.T().Setenv("MASS_URL", endpoint)
			var out, stderr bytes.Buffer
			s.Equal(0, subcommand.Run(s.T().Context(), test.args, strings.NewReader(""), &out, &stderr), stderr.String())
			s.Equal(1, calls)
			s.Contains(out.String(), "opaque")
		})
	}
}

func (s *CommandSuite) TestSessionCloseUsesBothAddressingFormsAndReturnsOffline() {
	for _, test := range []struct {
		args     []string
		expected string
	}{
		{[]string{"session", "close", "a/b%2F ?#"}, `{"session":{"sessionId":"a/b%2F ?#"}}`},
		{[]string{"session", "close", "task", "-n", "demo"}, `{"session":{"namespace":"demo","name":"task"}}`},
	} {
		s.Run(strings.Join(test.args, " "), func() {
			endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
				s.Equal("session.close", call.Method)
				s.JSONEq(test.expected, string(call.Params))
				call.respond(json.RawMessage(`{"metadata":{"namespace":"demo","name":"task"},"spec":{"runtimeRef":"agent","cwd":"/remote"},"status":{"sessionId":"native","state":"offline"}}`))
			})
			var out, stderr bytes.Buffer
			s.Equal(0, subcommand.Run(s.T().Context(), append([]string{"--url", endpoint, "-o", "json"}, test.args...), unreadableInput{}, &out, &stderr), stderr.String())
			s.Contains(out.String(), `"state": "offline"`)
			s.NotContains(out.String(), "reason")
			s.Empty(stderr.String())
		})
	}
}

func (s *CommandSuite) TestWatchReadyTimeoutEndsAtReadyAndInterruptOnlyClosesSubscription() {
	for _, mode := range []string{"closed", "eof", "error", "interrupt", "no-ready"} {
		s.Run(mode, func() {
			ready := make(chan struct{})
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			calls := 0
			endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
				calls++
				s.Equal("session.subscribe", call.Method)
				if mode == "no-ready" {
					return
				}
				call.respond(map[string]string{"subscriptionId": "1"})
				close(ready)
				if mode == "interrupt" {
					return
				}
				<-release
				switch mode {
				case "closed":
					call.notify("subscription.closed", map[string]any{"subscriptionId": "1"})
				case "eof":
					_ = call.conn.Close()
				case "error":
					call.notify("subscription.closed", map[string]any{
						"subscriptionId": "1",
						"error":          runtime.OperationError{Code: "resource_exhausted", Reason: "slow_consumer", Message: "overflow"},
					})
				}
			})
			ctx, cancel := context.WithCancel(s.T().Context())
			defer cancel()
			var out bytes.Buffer
			stderr := new(readyWriter)
			done := make(chan int, 1)
			go func() {
				done <- subcommand.Run(ctx, []string{"--url", endpoint, "-o", "json", "--timeout", "100ms", "session", "watch", "a/b"}, strings.NewReader(""), &out, stderr)
			}()
			if mode != "no-ready" {
				<-ready
				s.Eventually(stderr.isReady, time.Second, time.Millisecond)
				select {
				case code := <-done:
					s.FailNow("watch ended after ready", "exit", code)
				case <-time.After(150 * time.Millisecond):
				}
				if mode == "interrupt" {
					cancel()
				}
				if mode != "interrupt" {
					close(release)
				}
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
			s.Equal(1, calls)
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
	s.Equal(1, subcommand.Run(s.T().Context(), []string{"version"}, strings.NewReader(""), brokenWriter{}, &stderr))
	s.Contains(stderr.String(), "output_failed")
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (s *CommandSuite) TestSessionCreationAndPromptDoNotReadImplicitInput() {
	calls := []string{}
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		calls = append(calls, call.Method)
		switch call.Method {
		case "session.create":
			s.JSONEq(`{"input":{"metadata":{"namespace":"demo","name":"review"},"spec":{"runtimeRef":"agent","cwd":"/remote/work","systemPrompt":"rules\n","additionalDirs":["/remote/a","/remote/b"]}}}`, string(call.Params))
			call.respond(json.RawMessage(`{"status":{"sessionId":"original"}}`))
		case "session.prompt":
			s.JSONEq(`{"session":{"namespace":"demo","name":"review"},"input":{"content":[{"type":"text","text":"{raw text}"}]}}`, string(call.Params))
			call.respond(json.RawMessage(`{"submission":"receipt","acceptance":{"sent":true,"nativeAccepted":false}}`))
		default:
			s.Fail("unexpected method", call.Method)
		}
	})
	var out, stderr bytes.Buffer
	args := []string{"--url", endpoint, "-o", "json", "session", "create", "review", "-n", "demo", "--runtime", "agent", "--cwd", "/remote/work", "--system-prompt-file", "-", "--additional-dir", "/remote/a", "--additional-dir", "/remote/b"}
	s.Equal(0, subcommand.Run(s.T().Context(), args, strings.NewReader("rules\n"), &out, &stderr), stderr.String())
	s.Equal([]string{"session.create"}, calls, "creation must not send prompt")
	out.Reset()
	args = []string{"--url", endpoint, "-o", "json", "session", "prompt", "review", "-n", "demo", "--text", "{raw text}"}
	s.Equal(0, subcommand.Run(s.T().Context(), args, unreadableInput{}, &out, &stderr), stderr.String())
	s.JSONEq(`{"submission":"receipt","acceptance":{"sent":true,"nativeAccepted":false}}`, out.String())
}

type unreadableInput struct{}

func (unreadableInput) Read([]byte) (int, error) { panic("command unexpectedly read stdin") }

func (s *CommandSuite) TestDeleteHasEmptyJSONResultAndNeverStops() {
	methods := []string{}
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		methods = append(methods, call.Method)
		call.respond(nil)
	})
	var out, stderr bytes.Buffer
	s.Equal(0, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-o", "json", "runtime", "delete", "agent"}, unreadableInput{}, &out, &stderr), stderr.String())
	s.Empty(out.String())
	s.Empty(stderr.String())
	s.Equal([]string{"runtime.delete"}, methods)
}

func (s *CommandSuite) TestGetFailurePreventsUpdateAndPreservesPublicError() {
	calls := 0
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		calls++
		s.Equal("runtime.get", call.Method)
		call.fail(runtime.OperationError{Code: "not_found", Reason: "missing_runtime", Message: "Runtime is absent"})
	})
	var out, stderr bytes.Buffer
	s.Equal(1, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-o", "json", "runtime", "update", "agent", "--executable", "new"}, unreadableInput{}, &out, &stderr))
	s.Equal(1, calls)
	s.Empty(out.String())
	s.JSONEq(`{"code":"not_found","reason":"missing_runtime","message":"Runtime is absent"}`, stderr.String())
}

func (s *CommandSuite) TestMutationTimeoutHasUnknownOutcomeAndNoRetry() {
	var calls atomic.Int32
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		calls.Add(1)
		s.Equal("runtime.start", call.Method)
	})
	var out, stderr bytes.Buffer
	s.Equal(1, subcommand.Run(s.T().Context(), []string{"--url", endpoint, "-o", "json", "--timeout", "20ms", "runtime", "start", "agent"}, unreadableInput{}, &out, &stderr))
	s.Empty(out.String())
	s.Contains(stderr.String(), `"code":"outcome_unknown"`)
	s.Equal(int32(1), calls.Load())
}

func (s *CommandSuite) TestHelpExplainsRequiredInputsAndExclusiveModes() {
	for _, args := range [][]string{{"--help"}, {"runtime", "--help"}, {"runtime", "create", "--help"}, {"session", "create", "--help"}} {
		var out, stderr bytes.Buffer
		s.Equal(0, subcommand.Run(s.T().Context(), args, unreadableInput{}, &out, &stderr))
		s.Contains(out.String(), "Examples:")
		if len(args) > 2 {
			s.Contains(out.String(), "mutually exclusive")
		}
	}
}

func (s *CommandSuite) TestInterruptedMutationRetainsUnknownOutcome() {
	dispatched := make(chan struct{})
	endpoint := newRPCService(s.T(), "", func(call *rpcTestCall) {
		s.Equal("session.prompt", call.Method)
		close(dispatched)
	})
	ctx, cancel := context.WithCancel(s.T().Context())
	defer cancel()
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- subcommand.Run(ctx, []string{"--url", endpoint, "-o", "json", "session", "prompt", "id", "--text", "hello"}, unreadableInput{}, &out, &stderr)
	}()
	<-dispatched
	cancel()
	s.Equal(130, <-done)
	s.Empty(out.String())
	s.Contains(stderr.String(), `"code":"outcome_unknown"`)
}
