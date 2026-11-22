package triggers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	egressConversation = "CID:other-dm"
	operatorToken      = "test-only-operator-token-01234567890123456789"
)

type egressFixture struct {
	t                 *testing.T
	dir, exe, address string
	endpoint, config  string
	source, operator  string
	mcpFiles          string
	role              string
	started           time.Time
	core              *process
}

func newEgressFixture(t *testing.T, clock bool, source ...string) *egressFixture {
	t.Helper()
	f := &egressFixture{t: t, dir: t.TempDir(), exe: corePath(t), address: freeAddress(t), started: time.Now().UTC()}
	if clock {
		f.exe = os.Getenv("SUPERAGENT_CORE_CLOCK")
		if f.exe == "" {
			t.Fatal("SUPERAGENT_CORE_CLOCK must name a real Core build with the coretest clock")
		}
		f.advance(0)
	}
	// Core provisions the Router MCP credential. No Agent is launched
	// and no massd outcome is needed to enqueue a role-authored reply.
	unavailable := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(unavailable.Close)
	f.source, f.operator = filepath.Join(f.dir, "test-source"), filepath.Join(f.dir, "test-operator")
	massdTokenFile := filepath.Join(f.dir, "test-massd")
	testWrite(t, f.source, []byte(testToken))
	testWrite(t, f.operator, []byte(operatorToken))
	testWrite(t, massdTokenFile, []byte("test-only-massd-token"))
	sourceName := "dws"
	if len(source) != 0 {
		sourceName = source[0]
	}
	f.config = filepath.Join(f.dir, "core.yaml")
	f.mcpFiles = filepath.Join(f.dir, "data", "mcp-files")
	testWrite(t, f.config, fmt.Appendf(nil, `dataDir: %q
listen: %q
sources:
  - source: %s
    accountId: account
    instanceId: fixture-instance
    tokenFile: %q
massd:
  endpoint: %q
  tokenFile: %q
operatorTokenFile: %q
sessions:
  namespace: trigger-fixture
  routerWorkspace: %q
  roles:
    router:
      runtimeRef: test-runtime
      model: test-model
    topic:
      runtimeRef: test-runtime
      model: test-model
  issueTemplates:
    - runtimeRef: test-runtime
      model: test-model
      description: Fixture Issue template
`, filepath.Join(f.dir, "data"), f.address, sourceName, f.source,
		"ws"+strings.TrimPrefix(unavailable.URL, "http")+"/jsonrpc", massdTokenFile, f.operator, f.dir))
	f.startCore(clock)
	f.role = f.waitRoleToken("router", "")
	return f
}

func (f *egressFixture) waitRoleToken(role, ownerID string) string {
	f.t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "trigger-fixture", "version": "1"},
		},
	}
	var token string
	wait(f.t, role+" HTTP MCP credential", func() bool {
		paths, _ := filepath.Glob(filepath.Join(f.mcpFiles, "*.env"))
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			line, prefixed := strings.CutPrefix(string(data), "AUTHORIZATION='Bearer ")
			candidate, suffixed := strings.CutSuffix(line, "'\n")
			if !prefixed || !suffixed || candidate == "" {
				continue
			}
			var response struct {
				Result struct {
					Meta struct {
						Binding struct{ Role, OwnerID string } `json:"mass.dev/session-binding"`
					} `json:"_meta"`
				}
			}
			status := testRequest(f.t, http.MethodPost, f.endpoint, "/v1/mcp", candidate, request, &response)
			binding := response.Result.Meta.Binding
			if status == http.StatusOK && binding.Role == role && binding.OwnerID == ownerID {
				token = candidate
				return true
			}
		}
		return false
	})
	return token
}

func (f *egressFixture) startCore(clock bool) *process {
	env := []string{}
	if clock {
		env = append(env, "SUPERAGENT_TEST_OUTBOX_CLOCK="+filepath.Join(f.dir, "clock"))
	}
	p := start(f.t, f.exe, []string{"serve", "--config", f.config}, env)
	f.endpoint = p.endpoint(f.t, "endpoint")
	f.core = p
	return p
}

func (f *egressFixture) advance(seconds int) {
	testWrite(f.t, filepath.Join(f.dir, "clock"), []byte(f.started.Add(time.Duration(seconds)*time.Second).Format(time.RFC3339Nano)))
}

func testWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".next", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
}

func (f *egressFixture) trigger(endpoint string, options ...func(*Config)) *process {
	return f.triggerFeed(endpoint, f.dir, options...)
}

func (f *egressFixture) triggerFeed(endpoint, feed string, options ...func(*Config)) *process {
	f.t.Helper()
	c := Config{
		DWS: fixtureBinary, Profile: "corp:account", Source: "dws", AccountID: "account", SelfID: "self",
		InstanceID: "fixture-instance", CoreEndpoint: endpoint, TokenEnv: "TEST_CORE_TOKEN", Listen: "127.0.0.1:0",
		Direct:       directConfig{Enabled: true, Match: allTextRules()},
		SelfChat:     selfChatConfig{Enabled: true, ConversationID: fixtureSelfConversation, Match: allTextRules()},
		DatabasePath: filepath.Join(f.t.TempDir(), "dws.sqlite"),
		Capacity:     100, RetryMillis: 20, RequestMillis: 1000,
	}
	for _, configure := range options {
		configure(&c)
	}
	path := configFile(f.t, c)
	return start(f.t, triggerBinary, []string{"triggers", "dws", "serve", "--config", path}, []string{
		"TEST_CORE_TOKEN=" + testToken, "DWS_FIXTURE_INPUT=" + feed,
		"DWS_FIXTURE_MODE=feed", "DWS_FIXTURE_EGRESS=" + f.dir,
	})
}

func (f *egressFixture) feed(n int, id, actor, text string) {
	f.feedIn(f.dir, n, id, actor, text)
}

func (f *egressFixture) feedIn(dir string, n int, id, actor, text string) {
	f.t.Helper()
	value := event(id, text)
	value["type"] = directEvent
	value["sender_open_dingtalk_id"] = actor
	if actor != "self" {
		value["conversation_id"] = egressConversation
	}
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	testWrite(f.t, filepath.Join(dir, fmt.Sprintf("event-%03d.json", n)), data)
}

func (f *egressFixture) proxy(hook func(http.ResponseWriter, *http.Request) bool, after func(*http.Response) error) *httptest.Server {
	f.t.Helper()
	target, err := url.Parse(f.endpoint)
	if err != nil {
		f.t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
		},
		ModifyResponse: after,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook == nil || !hook(w, r) {
			proxy.ServeHTTP(w, r)
		}
	}))
	f.t.Cleanup(s.Close)
	return s
}

func testRequest(t *testing.T, method, endpoint, path, token string, body, result any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := httpClient(2 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if result != nil && json.NewDecoder(res.Body).Decode(result) != nil {
		t.Fatalf("invalid fixture response to %s %s", method, path)
	}
	return res.StatusCode
}

func callRoleTool[T any](t *testing.T, endpoint, token, name string, arguments any) T {
	t.Helper()
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": arguments},
	}
	status := testRequest(t, http.MethodPost, endpoint, "/v1/mcp", token, request, &reply)
	if status != http.StatusOK || reply.JSONRPC != "2.0" || reply.ID != 1 || len(reply.Error) != 0 || reply.Result.IsError {
		t.Fatalf("Role MCP %s failed: HTTP %d, RPC error %s, tool result %s", name, status, reply.Error, reply.Result.StructuredContent)
	}
	var result T
	if err := json.Unmarshal(reply.Result.StructuredContent, &result); err != nil {
		t.Fatalf("decode Role MCP %s structured result: %v", name, err)
	}
	return result
}

func (f *egressFixture) message(nativeID string) map[string]any {
	f.t.Helper()
	var found map[string]any
	wait(f.t, "Message "+nativeID, func() bool {
		for _, m := range readMessages(f.t, f.endpoint) {
			if m["sourceMessageId"] == nativeID {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

func (f *egressFixture) reply(nativeID, text string) string {
	f.t.Helper()
	m := f.message(nativeID)
	reply := callRoleTool[outboxItem](f.t, f.endpoint, f.role, "send_reply", map[string]any{
		"operationId": "reply-" + nativeID,
		"messageId":   m["id"],
		"content": map[string]any{
			"type": "markdown",
			"data": map[string]string{"text": text},
		},
	})
	if reply.ID == "" {
		f.t.Fatal("Core did not return an Outbox item ID")
	}
	return reply.ID
}

func TestOutboxRendersStructuredReply(t *testing.T) {
	item := outboxItem{Content: outboxContent{Type: "options", Data: json.RawMessage(`{
		"prompt": "**Choose a response**",
		"options": [{"id": "later", "label": "Review later"}, {"id": "now", "label": "Start now"}]
	}`)}}
	const want = "**Choose a response**\n\n1. Review later\n2. Start now"
	if got := item.renderText(); got != want {
		t.Fatalf("rendered options = %q, want %q", got, want)
	}
	item.Content = outboxContent{Type: "markdown", Data: json.RawMessage(`{"text":"**Reply**"}`)}
	if got := item.renderText(); got != "**Reply**" {
		t.Fatalf("rendered Markdown = %q", got)
	}
}

func (f *egressFixture) outbox(id string) outboxItem {
	f.t.Helper()
	var item outboxItem
	if code := testRequest(f.t, "GET", f.endpoint, "/v1/observer/outbox/"+id, "", nil, &item); code != http.StatusOK {
		f.t.Fatalf("outbox readback: %d", code)
	}
	return item
}

func (f *egressFixture) settle(task, conversation, message string) {
	f.t.Helper()
	testWrite(f.t, filepath.Join(f.dir, task+".json"), fmt.Appendf(nil,
		`{"success":true,"openTaskId":%q,"result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":%q}}`,
		task, conversation, message))
}

func (f *egressFixture) calls(queries bool) [][]string {
	f.t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.dir, "calls.ndjson"))
	var picked [][]string
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		var args []string
		if json.Unmarshal(line, &args) == nil && len(args) > 0 && (args[0] == "+messages-query-send-status") == queries {
			picked = append(picked, args)
		}
	}
	return picked
}

func (f *egressFixture) assertContentSentOnce(want string) {
	f.t.Helper()
	calls := f.calls(false)
	if len(calls) != 1 {
		f.t.Fatalf("expected one send, got %v", calls)
	}
	content := slices.Index(calls[0], "--content")
	if content < 0 || content+1 >= len(calls[0]) || calls[0][content+1] != want {
		f.t.Fatalf("send %q must include --content %q", calls[0], want)
	}
}

func echoStatus(m map[string]any) string {
	echo, _ := m["echo"].(map[string]any)
	status, _ := echo["status"].(string)
	return status
}

func (f *egressFixture) echo(id, want string) map[string]any {
	f.t.Helper()
	var message map[string]any
	wait(f.t, id+" echo status "+want, func() bool {
		message = f.message(id)
		return echoStatus(message) == want
	})
	return message
}

type routingEvent struct {
	Kind      string `json:"kind"`
	Recipient struct {
		TopicID string `json:"topic_id"`
	} `json:"recipient"`
}

func (f *egressFixture) routingEvents(m map[string]any) []routingEvent {
	f.t.Helper()
	var page struct {
		Items []routingEvent `json:"items"`
	}
	path := "/v1/observer/routing-events?messageId=" + url.QueryEscape(m["id"].(string))
	if code := testRequest(f.t, "GET", f.endpoint, path, "", nil, &page); code != http.StatusOK {
		f.t.Fatalf("read routing events: HTTP %d", code)
	}
	return page.Items
}

func (f *egressFixture) assertRouted(m map[string]any, want int) {
	f.t.Helper()
	events := f.routingEvents(m)
	if len(events) != want {
		f.t.Fatalf("routing events for %v: got %d want %d", m["sourceMessageId"], len(events), want)
	}
	for _, item := range events {
		if item.Kind != "route_request" {
			f.t.Fatalf("unexpected routing event kind: %s", item.Kind)
		}
	}
}

func TestEgressDurableTaskExactEchoAndLostWriteReceipt(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	var dropped, taskSeen, allowTask atomic.Bool
	var mu sync.Mutex
	var writes []outboxCommand
	proxy := f.proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != outboxPath+"/result" {
			return false
		}
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		var req outboxCommand
		_ = json.Unmarshal(data, &req)
		if req.Outcome == "pending" && !allowTask.Load() {
			taskSeen.Store(true)
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		if req.Outcome == "sent" {
			mu.Lock()
			writes = append(writes, req)
			mu.Unlock()
		}
		return false
	}, func(res *http.Response) error {
		if res.Request.URL.Path != outboxPath+"/result" || res.StatusCode != http.StatusOK {
			return nil
		}
		mu.Lock()
		sent := len(writes) > 0
		mu.Unlock()
		if sent && !dropped.Swap(true) {
			return errors.New("fixture dropped committed receipt")
		}
		return nil
	})
	f.feed(1, "self-before", "self", "legitimate self input")
	p := f.trigger(proxy.URL)
	f.assertRouted(f.echo("self-before", ""), 1)
	id := f.reply("self-before", "identical text")
	wait(t, "task write waiting for Core", taskSeen.Load)
	if d := f.outbox(id); d.Status != "pending" || d.Receipt != nil || len(f.calls(false)) != 1 || len(f.calls(true)) != 0 {
		t.Fatalf("before durable task: %+v", d)
	}
	f.feed(2, "actual-echo", "self", "identical text")
	f.feed(3, "same-text-distinct", "self", "identical text")
	f.feed(4, "peer-during-send", "peer", "identical text")
	f.assertRouted(f.echo("actual-echo", "held"), 0)
	f.assertRouted(f.echo("same-text-distinct", "held"), 0)
	f.assertRouted(f.echo("peer-during-send", ""), 1)
	allowTask.Store(true)
	task := f.waitTask(id)
	if calls := f.calls(false); len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "+messages-reply --open-dingtalk-id self --group "+fixtureSelfConversation+" --message-id self-before --ref-sender self --yes") {
		t.Fatalf("unexpected sends: %v", calls)
	}
	f.settle(task, fixtureSelfConversation, "actual-echo")
	wait(t, "lost final receipt replayed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(writes) >= 2
	})
	mu.Lock()
	replayed := len(writes) == 2 && reflect.DeepEqual(writes[0], writes[1])
	mu.Unlock()
	if !replayed || !dropped.Load() || !strings.Contains(p.text(), "outbox_result_unconfirmed") {
		t.Fatal("expected the same successful write after losing the committed response")
	}
	d := f.outbox(id)
	if d.Status != "sent" || d.Receipt == nil || d.Receipt.TaskID != task || d.Receipt.MessageID != "actual-echo" || len(f.calls(false)) != 1 {
		t.Fatalf("unexpected durable send receipt: %+v", d)
	}
	f.assertContentSentOnce("identical text")
	f.assertRouted(f.echo("actual-echo", "outbound_echo"), 0)
	f.assertRouted(f.echo("same-text-distinct", "cleared"), 1)
	f.feed(5, "self-after", "self", "identical text")
	f.assertRouted(f.echo("self-after", ""), 1)
}

func (f *egressFixture) waitTask(id string) string {
	f.t.Helper()
	var task string
	wait(f.t, "original task persisted: "+id, func() bool {
		d := f.outbox(id)
		if d.Status == "pending" && d.Receipt != nil {
			task = d.Receipt.TaskID
		}
		return task != ""
	})
	return task
}

func TestEgressRestartRecoversSavedTask(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	f.feed(1, "ask", "self", "request")
	first := f.trigger(f.endpoint)
	const prompt = "**Choose a response**\n\nDetails about the request."
	const rendered = prompt + "\n\n1. Review later\n2. Start now"
	id := f.reply("ask", rendered)
	task := f.waitTask(id)
	wait(t, "original task queried", func() bool { return len(f.calls(true)) > 0 })
	killAdapter(t, first)
	terminate(t, f.core)
	f.startCore(false)

	// An inconclusive query must leave the same task recoverable.
	testWrite(t, filepath.Join(f.dir, task+".json"), []byte(`{"success":false,"error":{"reason":"permission_denied"}}`))
	queriesBefore := len(f.calls(true))
	_ = f.trigger(f.endpoint)
	wait(t, "original task queried after restart", func() bool { return len(f.calls(true)) > queriesBefore })
	if d := f.outbox(id); d.Status != "pending" || d.Receipt == nil || d.Receipt.TaskID != task || len(f.calls(false)) != 1 {
		t.Fatalf("inconclusive query changed persisted task: %+v", d)
	}
	f.settle(task, fixtureSelfConversation, "recovered-echo")
	wait(t, "old task sent", func() bool { return f.outbox(id).Status == "sent" })
	if d := f.outbox(id); d.Receipt == nil || d.Receipt.TaskID != task || len(f.calls(false)) != 1 {
		t.Fatalf("expected one original send: %+v", d)
	}
	for _, args := range f.calls(true) {
		if len(args) < 3 || args[1] != "--open-task-id" || args[2] != task {
			t.Fatalf("queried a different task: %v", args)
		}
	}
	f.assertContentSentOnce(rendered)
	f.feed(2, "recovered-echo", "self", rendered)
	f.assertRouted(f.echo("recovered-echo", "outbound_echo"), 0)
}

func TestEgressDWSAtomicSendDataReceipt(t *testing.T) {
	t.Parallel()
	const task = "task-send_atomic_fixture"
	for name, data := range map[string]string{
		"direct-task":        `{"openTaskId":"task-send_atomic_fixture","success":true}`,
		"result-task":        `{"result":{"openTaskId":"task-send_atomic_fixture"},"success":true}`,
		"nested-data-task":   `{"data":{"openTaskId":"task-send_atomic_fixture","result":[]},"success":true}`,
		"nested-result-task": `{"data":{"result":{"openTaskId":"task-send_atomic_fixture"}},"success":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newEgressFixture(t, false)
			testWrite(t, filepath.Join(f.dir, "send-result.json"), fmt.Appendf(nil,
				`{"ok":true,"outcome":"pending","data":%s,"meta":{"operation":{"id":%q,"state":"processing","next_command":%q}}}`,
				data, task, "dws chat message query-send-status --open-task-id "+task))
			f.settle(task, fixtureSelfConversation, "atomic-echo")
			f.feed(1, "ask", "self", "request")
			_ = f.trigger(f.endpoint)
			const text = "**Plain reply**"
			id := f.reply("ask", text)
			var detail outboxItem
			wait(t, "atomic send reached sent or unknown", func() bool {
				detail = f.outbox(id)
				return detail.Status == "sent" || detail.Status == "failed"
			})
			if detail.Status != "sent" || detail.Receipt == nil ||
				detail.Receipt.TaskID != task || len(f.calls(false)) != 1 {
				t.Fatalf("atomic send did not record the original task and settle once: %+v", detail)
			}
			queries := f.calls(true)
			if len(queries) != 1 || queries[0][2] != task {
				t.Fatalf("expected one original-task query: %v", queries)
			}
			f.assertContentSentOnce(text)
			f.feed(2, "atomic-echo", "self", text)
			f.assertRouted(f.echo("atomic-echo", "outbound_echo"), 0)
		})
	}
}

func TestEgressUnknownResultRetriesAfterDeadline(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		name := "no task from send"
		if missing {
			name = "saved task explicitly unavailable"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newEgressFixture(t, true)
			override := filepath.Join(f.dir, "send-result.json")
			if !missing {
				testWrite(t, override, []byte(`{}`))
			}
			f.feed(1, "request", "peer", "request")
			first := f.trigger(f.endpoint)
			id := f.reply("request", "reply")
			var oldTask string
			if missing {
				oldTask = f.waitTask(id)
				testWrite(t, filepath.Join(f.dir, oldTask+".json"), []byte(`{"success":false,"error":{"reason":"task_not_found"}}`))
			}
			wait(t, "unknown outcome with retry deadline", func() bool {
				d := f.outbox(id)
				return d.Status == "failed" && d.Receipt != nil && d.Receipt.RetryAt != "" && d.Receipt.Uncertain
			})
			killAdapter(t, first)
			if !missing {
				if err := os.Remove(override); err != nil {
					t.Fatal(err)
				}
			}
			terminate(t, f.core)
			f.startCore(true)
			_ = f.trigger(f.endpoint)
			f.advance(59)
			time.Sleep(1200 * time.Millisecond)
			if len(f.calls(false)) != 1 || f.outbox(id).Status != "failed" {
				t.Fatal("sent before the unknown wait elapsed")
			}
			f.advance(61)
			task := f.waitTask(id)
			calls := f.calls(false)
			if task == oldTask || len(calls) != 2 || reflect.DeepEqual(calls[0], calls[1]) {
				t.Fatalf("expected one fresh send of the same Outbox item: %v", calls)
			}
			f.settle(task, egressConversation, "retried-echo")
			wait(t, "retry settled", func() bool { return f.outbox(id).Status == "sent" })
		})
	}
}

func TestEgressCrashBeforeTaskCommitAndEchoRelease(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, true)
	var blocked atomic.Bool
	proxy := f.proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != outboxPath+"/result" {
			return false
		}
		blocked.Store(true)
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}, nil)
	f.feed(1, "request", "self", "original")
	first := f.trigger(proxy.URL)
	id := f.reply("request", "could have been delivered")
	wait(t, "DWS returned task, save blocked", blocked.Load)
	if before := f.outbox(id); before.Status != "pending" || before.Receipt != nil || len(f.calls(true)) != 0 {
		t.Fatalf("query or task before Core commit: %+v", before)
	}
	f.feed(2, "uncertain-self", "self", "genuine self message")
	f.assertRouted(f.echo("uncertain-self", "held"), 0)
	killAdapter(t, first)
	f.advance(61)
	unknown := f.echo("uncertain-self", "echo_unknown")
	f.assertRouted(unknown, 0)
	terminate(t, f.core)
	f.startCore(true)
	f.assertRouted(f.echo("uncertain-self", "echo_unknown"), 0)
	releasePath := "/v1/operator/echo-release"
	release := map[string]any{"messageId": unknown["id"], "operationId": "release-test", "reason": "verified this fixture Message was typed by the operator"}
	if code := testRequest(t, "POST", f.endpoint, releasePath, testToken, release, nil); code != http.StatusUnauthorized {
		t.Fatalf("channel token must not authorize release: %d", code)
	}
	args := []string{
		"release-echo", unknown["id"].(string), "--endpoint", f.endpoint, "--operator-token-file", f.operator,
		"--operation-id", "release-test", "--reason", release["reason"].(string),
	}
	for range 2 {
		if out, err := exec.CommandContext(t.Context(), f.exe, args...).CombinedOutput(); err != nil {
			t.Fatalf("operator release: %v, %s", err, out)
		}
	}
	f.assertRouted(f.echo("uncertain-self", "released"), 1)
	release["operationId"] = "second-release"
	if code := testRequest(t, "POST", f.endpoint, releasePath, operatorToken, release, nil); code != http.StatusConflict {
		t.Fatalf("second release must be rejected: %d", code)
	}
	_ = f.trigger(f.endpoint)
	task := f.waitTask(id)
	if calls := f.calls(false); len(calls) != 2 || !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatalf("resend before saved receipt must reuse the DWS key: %v", calls)
	}
	f.settle(task, fixtureSelfConversation, "late-receipt")
	wait(t, "recovered send settled", func() bool { return f.outbox(id).Status == "sent" })
}

func TestEgressKnownSuccessOnlyRetriesWrite(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	var allow, waiting atomic.Bool
	proxy := f.proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == outboxPath+"/result" && !allow.Load() {
			waiting.Store(true)
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}, nil)
	testWrite(t, filepath.Join(f.dir, "send-result.json"), fmt.Appendf(nil,
		`{"success":true,"openTaskId":"task-send_already_done","result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":"direct-echo"}}`,
		fixtureSelfConversation))
	f.feed(1, "request", "self", "original")
	_ = f.trigger(proxy.URL)
	id := f.reply("request", "answer")
	wait(t, "confirmed send waiting for Core write", waiting.Load)
	f.feed(2, "second", "self", "another request")
	second := f.reply("second", "second answer")
	f.feed(3, "direct-echo", "self", "answer")
	f.assertRouted(f.echo("direct-echo", "held"), 0)
	terminate(t, f.core)
	f.startCore(false)
	if d := f.outbox(id); d.Status != "pending" || d.Receipt != nil || len(f.calls(false)) != 1 || len(f.calls(true)) != 0 {
		t.Fatalf("a blocked write must serialize all channel calls: %+v", d)
	}
	testWrite(t, filepath.Join(f.dir, "send-result.json"), fmt.Appendf(nil,
		`{"success":true,"result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":"second-echo"}}`, fixtureSelfConversation))
	allow.Store(true)
	wait(t, "known success was written after restart", func() bool {
		return f.outbox(id).Status == "sent" && f.outbox(second).Status == "sent"
	})
	if d := f.outbox(id); d.Receipt == nil || d.Receipt.TaskID != "task-send_already_done" ||
		d.Receipt.MessageID != "direct-echo" || len(f.calls(false)) != 2 || len(f.calls(true)) != 0 {
		t.Fatalf("unexpected send, query, or successful receipt: %+v", d)
	}
	f.assertRouted(f.echo("direct-echo", "outbound_echo"), 0)
}

func TestEgressDefiniteRejectionStopsAutomaticSend(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, true)
	testWrite(t, filepath.Join(f.dir, "send-result.json"), []byte(`{"success":false,"error":{"reason":"permission_denied"}}`))
	f.feed(1, "request", "new-peer", "original")
	_ = f.trigger(f.endpoint, func(c *Config) { c.SelfChat = selfChatConfig{} })
	id := f.reply("request", "reply")
	wait(t, "known error recorded", func() bool {
		d := f.outbox(id)
		return d.Status == "failed" && d.Receipt != nil && d.Receipt.ErrorCode == "dws_send_rejected" && d.Receipt.RetryAt == ""
	})
	f.advance(3600)
	time.Sleep(1200 * time.Millisecond)
	if len(f.calls(false)) != 1 || len(f.calls(true)) != 0 {
		t.Fatal("definite rejection retried or created a task query")
	}
	if calls := f.calls(false); !strings.Contains(strings.Join(calls[0], " "), "--open-dingtalk-id new-peer") {
		t.Fatalf("expected the admitted native sender as the peer: %v", calls)
	}
}

func TestEgressRequiresSelfIdentityForEveryListener(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	f.feed(1, "self-question", "self", "genuine self input")
	_ = f.trigger(f.endpoint)
	f.assertRouted(f.echo("self-question", ""), 1)
	id := f.reply("self-question", "answer")
	task := f.waitTask(id)
	overlappingFeed := t.TempDir()
	f.feedIn(overlappingFeed, 1, "early-echo", "self", "answer")
	{
		p := f.triggerFeed(f.endpoint, overlappingFeed, func(c *Config) { c.SelfID = "" })
		select {
		case err := <-p.done:
			p.done <- err
			if err == nil || !strings.Contains(p.text(), "selfOpenDingtalkId is required") {
				t.Fatalf("expected a configuration error: %v, %s", err, p.text())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("overlapping Trigger started without native self identity")
		}
	}
	for _, message := range readMessages(t, f.endpoint) {
		if message["sourceMessageId"] == "early-echo" {
			t.Fatal("invalid configuration submitted the early echo")
		}
	}
	f.feed(2, "early-echo", "self", "answer")
	f.assertRouted(f.echo("early-echo", "held"), 0)
	f.settle(task, fixtureSelfConversation, "early-echo")
	f.assertRouted(f.echo("early-echo", "outbound_echo"), 0)
	t.Log("configuration without selfOpenDingtalkId failed before overlapping intake; the configured Trigger routed genuine self input and suppressed its early outbound echo")
}

func killAdapter(t *testing.T, p *process) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.done:
		p.done <- err
		if err == nil {
			t.Fatal("expected killed process")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("killed adapter did not exit")
	}
}
