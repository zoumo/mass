package dws

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

const egressConversation = "CID:other-dm"

type egressFixture struct {
	*triggertest.CoreFixture
	t *testing.T
}

func newEgressFixture(t *testing.T, clock bool) *egressFixture {
	t.Helper()
	return &egressFixture{CoreFixture: triggertest.NewCoreFixture(t, clock, "dws", "dws:"), t: t}
}

func (f *egressFixture) trigger(endpoint string, options ...func(*dwsConfig)) *triggertest.Process {
	return f.triggerFeed(endpoint, f.Dir, options...)
}

func (f *egressFixture) triggerFeed(endpoint, feed string, options ...func(*dwsConfig)) *triggertest.Process {
	f.t.Helper()
	c := dwsConfig{
		DWS: fixtureBinary, Profile: "corp:account", Source: "dws", AccountID: "account", SelfID: "self",
		InstanceID: "fixture-instance", CoreEndpoint: endpoint, TokenEnv: "TEST_CORE_TOKEN", Listen: "127.0.0.1:0",
		Direct:       directConfig{Enabled: true, Match: allTextRules()},
		SelfChat:     selfChatConfig{Enabled: true, ConversationID: fixtureSelfConversation, Match: allTextRules()},
		DatabasePath: filepath.Join(f.Dir, "dws.sqlite"),
		Capacity:     100, RetryMillis: 20, RequestMillis: 1000,
	}
	for _, configure := range options {
		configure(&c)
	}
	path := configFile(f.t, c)
	return triggertest.Start(f.t, triggerBinary, []string{"triggers", "dws", "serve", "--config", path}, []string{
		"TEST_CORE_TOKEN=" + triggertest.Token, "DWS_FIXTURE_INPUT=" + feed,
		"DWS_FIXTURE_MODE=feed", "DWS_FIXTURE_EGRESS=" + f.Dir,
	})
}

func (f *egressFixture) feed(n int, id, actor, text string) {
	f.feedIn(f.Dir, n, id, actor, text)
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
	triggertest.Write(f.t, filepath.Join(dir, fmt.Sprintf("event-%03d.json", n)), data)
}

func (f *egressFixture) message(nativeID string) map[string]any {
	f.t.Helper()
	var found map[string]any
	triggertest.Wait(f.t, "Message "+nativeID, func() bool {
		for _, m := range triggertest.ReadMessages(f.t, f.Endpoint) {
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
	reply := triggertest.CallRoleTool[outboxItem](f.t, f.Endpoint, f.RoleToken, "send_reply", map[string]any{
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
	if code := triggertest.Request(f.t, "GET", f.Endpoint, "/v1/core/outbox-messages/"+id, "", nil, &item); code != http.StatusOK {
		f.t.Fatalf("outbox readback: %d", code)
	}
	return item
}

func (f *egressFixture) settle(task, conversation, message string) {
	f.t.Helper()
	triggertest.Write(f.t, filepath.Join(f.Dir, task+".json"), fmt.Appendf(nil,
		`{"success":true,"openTaskId":%q,"result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":%q}}`,
		task, conversation, message))
}

func (f *egressFixture) calls(queries bool) [][]string {
	f.t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.Dir, "calls.ndjson"))
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

func (f *egressFixture) assertLocalEcho(id, want string) {
	f.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.Dir, "dws.sqlite"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	triggertest.Wait(f.t, id+" echo status "+want, func() bool {
		var status string
		return db.QueryRowContext(f.t.Context(), `SELECT coalesce(failure,'held') FROM dws_submissions WHERE native_message_id=?`, id).Scan(&status) == nil && status == want
	})
	for _, msg := range triggertest.ReadMessages(f.t, f.Endpoint) {
		if msg["sourceMessageId"] == id {
			f.t.Fatalf("echo %q reached Core: %v", id, msg)
		}
	}
}

func (f *egressFixture) nativeResult(id, messageID string) bool {
	files, _ := filepath.Glob(filepath.Join(f.Dir, "dws.sqlite.dws-sends", "*.json"))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		var attempt sendEvidence
		if err == nil && json.Unmarshal(raw, &attempt) == nil && attempt.OutboxID == id && attempt.Result.MessageID == messageID {
			return true
		}
	}
	return false
}

func TestEgressDurableTaskExactEchoAndLostWriteReceipt(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	var dropped, taskSeen, allowTask atomic.Bool
	var mu sync.Mutex
	var writes []outboxCommand
	proxy := f.Proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, outboxPath+"/") {
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
		if (res.Request.Method != http.MethodPatch || !strings.HasPrefix(res.Request.URL.Path, outboxPath+"/")) || res.StatusCode != http.StatusOK {
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
	f.AssertRouted(f.message("self-before"), 1)
	id := f.reply("self-before", "identical text")
	triggertest.Wait(t, "task write waiting for Core", taskSeen.Load)
	if d := f.outbox(id); d.Status != "pending" || d.Receipt != nil || len(f.calls(false)) != 1 || len(f.calls(true)) != 0 {
		t.Fatalf("before durable task: %+v", d)
	}
	f.feed(2, "actual-echo", "self", "identical text")
	f.feed(3, "same-text-distinct", "self", "identical text")
	f.feed(4, "peer-during-send", "peer", "identical text")
	f.assertLocalEcho("actual-echo", "held")
	f.assertLocalEcho("same-text-distinct", "held")
	f.AssertRouted(f.message("peer-during-send"), 1)
	allowTask.Store(true)
	task := f.waitTask(id)
	if calls := f.calls(false); len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "+messages-reply --open-dingtalk-id self --group "+fixtureSelfConversation+" --message-id self-before --ref-sender self --yes") {
		t.Fatalf("unexpected sends: %v", calls)
	}
	f.settle(task, fixtureSelfConversation, "actual-echo")
	triggertest.Wait(t, "lost final receipt replayed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(writes) >= 2
	})
	mu.Lock()
	replayed := len(writes) == 2 && reflect.DeepEqual(writes[0], writes[1])
	mu.Unlock()
	if !replayed || !dropped.Load() || !strings.Contains(p.Text(), "outbox_result_unconfirmed") {
		t.Fatal("expected the same successful write after losing the committed response")
	}
	d := f.outbox(id)
	if d.Status != "sent" || d.Receipt == nil || d.Receipt.TaskID != task || !f.nativeResult(id, "actual-echo") || len(f.calls(false)) != 1 {
		t.Fatalf("unexpected durable send receipt: %+v", d)
	}
	f.assertContentSentOnce("identical text")
	f.assertLocalEcho("actual-echo", "outbound_echo")
	f.AssertRouted(f.message("same-text-distinct"), 1)
	f.feed(5, "self-after", "self", "identical text")
	f.AssertRouted(f.message("self-after"), 1)
}

func (f *egressFixture) waitTask(id string) string {
	f.t.Helper()
	var task string
	triggertest.Wait(f.t, "original task persisted: "+id, func() bool {
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
	first := f.trigger(f.Endpoint)
	const prompt = "**Choose a response**\n\nDetails about the request."
	const rendered = prompt + "\n\n1. Review later\n2. Start now"
	id := f.reply("ask", rendered)
	task := f.waitTask(id)
	triggertest.Wait(t, "original task queried", func() bool { return len(f.calls(true)) > 0 })
	killAdapter(t, first)
	triggertest.Terminate(t, f.Core)
	f.StartCore(false)

	// An inconclusive query must leave the same task recoverable.
	triggertest.Write(t, filepath.Join(f.Dir, task+".json"), []byte(`{"success":false,"error":{"reason":"permission_denied"}}`))
	queriesBefore := len(f.calls(true))
	_ = f.trigger(f.Endpoint)
	triggertest.Wait(t, "original task queried after restart", func() bool { return len(f.calls(true)) > queriesBefore })
	if d := f.outbox(id); d.Status != "pending" || d.Receipt == nil || d.Receipt.TaskID != task || len(f.calls(false)) != 1 {
		t.Fatalf("inconclusive query changed persisted task: %+v", d)
	}
	f.settle(task, fixtureSelfConversation, "recovered-echo")
	triggertest.Wait(t, "old task sent", func() bool { return f.outbox(id).Status == "sent" })
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
	f.assertLocalEcho("recovered-echo", "outbound_echo")
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
			triggertest.Write(t, filepath.Join(f.Dir, "send-result.json"), fmt.Appendf(nil,
				`{"ok":true,"outcome":"pending","data":%s,"meta":{"operation":{"id":%q,"state":"processing","next_command":%q}}}`,
				data, task, "dws chat message query-send-status --open-task-id "+task))
			f.settle(task, fixtureSelfConversation, "atomic-echo")
			f.feed(1, "ask", "self", "request")
			_ = f.trigger(f.Endpoint)
			const text = "**Plain reply**"
			id := f.reply("ask", text)
			var detail outboxItem
			triggertest.Wait(t, "atomic send reached sent or unknown", func() bool {
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
			f.assertLocalEcho("atomic-echo", "outbound_echo")
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
			override := filepath.Join(f.Dir, "send-result.json")
			if !missing {
				triggertest.Write(t, override, []byte(`{}`))
			}
			f.feed(1, "request", "peer", "request")
			first := f.trigger(f.Endpoint)
			id := f.reply("request", "reply")
			var oldTask string
			if missing {
				oldTask = f.waitTask(id)
				triggertest.Write(t, filepath.Join(f.Dir, oldTask+".json"), []byte(`{"success":false,"error":{"reason":"task_not_found"}}`))
			}
			triggertest.Wait(t, "unknown outcome with retry deadline", func() bool {
				d := f.outbox(id)
				return d.Status == "failed" && d.Receipt != nil && d.Receipt.RetryAt != "" && d.Receipt.Uncertain
			})
			killAdapter(t, first)
			if !missing {
				if err := os.Remove(override); err != nil {
					t.Fatal(err)
				}
			}
			triggertest.Terminate(t, f.Core)
			f.StartCore(true)
			_ = f.trigger(f.Endpoint)
			f.Advance(59)
			time.Sleep(1200 * time.Millisecond)
			if len(f.calls(false)) != 1 || f.outbox(id).Status != "failed" {
				t.Fatal("sent before the unknown wait elapsed")
			}
			f.Advance(61)
			task := f.waitTask(id)
			calls := f.calls(false)
			if task == oldTask || len(calls) != 2 || reflect.DeepEqual(calls[0], calls[1]) {
				t.Fatalf("expected one fresh send of the same Outbox item: %v", calls)
			}
			f.settle(task, egressConversation, "retried-echo")
			triggertest.Wait(t, "retry settled", func() bool { return f.outbox(id).Status == "sent" })
		})
	}
}

func TestEgressCrashBeforeTaskCommitAndEchoRelease(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, true)
	var blocked atomic.Bool
	proxy := f.Proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, outboxPath+"/") {
			return false
		}
		blocked.Store(true)
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}, nil)
	f.feed(1, "request", "self", "original")
	first := f.trigger(proxy.URL)
	id := f.reply("request", "could have been delivered")
	triggertest.Wait(t, "DWS returned task, save blocked", blocked.Load)
	if before := f.outbox(id); before.Status != "pending" || before.Receipt != nil || len(f.calls(true)) != 0 {
		t.Fatalf("query or task before Core commit: %+v", before)
	}
	f.feed(2, "uncertain-self", "self", "genuine self message")
	f.assertLocalEcho("uncertain-self", "held")
	killAdapter(t, first)
	f.Advance(61)
	f.assertLocalEcho("uncertain-self", "held")
	triggertest.Terminate(t, f.Core)
	f.StartCore(true)
	c := dwsConfig{
		DWS: fixtureBinary, Profile: "corp:account", Source: "dws", AccountID: "account", SelfID: "self",
		InstanceID: "fixture-instance", CoreEndpoint: f.Endpoint, TokenEnv: "TEST_CORE_TOKEN", Listen: "127.0.0.1:0",
		SelfChat:     selfChatConfig{Enabled: true, ConversationID: fixtureSelfConversation, Match: allTextRules()},
		DatabasePath: filepath.Join(f.Dir, "dws.sqlite"),
	}
	config := configFile(t, c)
	inspect, err := dwsCommand(t, "inspect-evidence", "--config", config).CombinedOutput()
	if err != nil || !bytes.Contains(inspect, []byte(`"status":"held","sourceMessageId":"uncertain-self"`)) {
		t.Fatalf("inspect held input: %v %s", err, inspect)
	}
	args := []string{"release-input", "uncertain-self", "--config", config, "--reason", "verified this fixture Message was typed by the operator"}
	for range 2 {
		if out, err := dwsCommand(t, args...).CombinedOutput(); err != nil {
			t.Fatalf("operator release: %v, %s", err, out)
		}
	}
	_ = f.trigger(f.Endpoint)
	f.AssertRouted(f.message("uncertain-self"), 1)
	task := f.waitTask(id)
	if calls := f.calls(false); len(calls) != 1 {
		t.Fatalf("restart must query the saved task without another send: %v", calls)
	}
	f.settle(task, fixtureSelfConversation, "late-receipt")
	triggertest.Wait(t, "recovered send settled", func() bool { return f.outbox(id).Status == "sent" })
}

func TestEgressKnownSuccessOnlyRetriesWrite(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	var allow, waiting atomic.Bool
	proxy := f.Proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, outboxPath+"/") && !allow.Load() {
			waiting.Store(true)
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}, nil)
	triggertest.Write(t, filepath.Join(f.Dir, "send-result.json"), fmt.Appendf(nil,
		`{"success":true,"openTaskId":"task-send_already_done","result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":"direct-echo"}}`,
		fixtureSelfConversation))
	f.feed(1, "request", "self", "original")
	_ = f.trigger(proxy.URL)
	id := f.reply("request", "answer")
	triggertest.Wait(t, "confirmed send waiting for Core write", waiting.Load)
	f.feed(2, "second", "self", "another request")
	second := f.reply("second", "second answer")
	f.feed(3, "direct-echo", "self", "answer")
	f.assertLocalEcho("direct-echo", "outbound_echo")
	triggertest.Terminate(t, f.Core)
	f.StartCore(false)
	if d := f.outbox(id); d.Status != "pending" || d.Receipt != nil || len(f.calls(false)) != 1 || len(f.calls(true)) != 0 {
		t.Fatalf("a blocked write must serialize all channel calls: %+v", d)
	}
	triggertest.Write(t, filepath.Join(f.Dir, "send-result.json"), fmt.Appendf(nil,
		`{"success":true,"result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":"second-echo"}}`, fixtureSelfConversation))
	allow.Store(true)
	triggertest.Wait(t, "known success was written after restart", func() bool {
		return f.outbox(id).Status == "sent" && f.outbox(second).Status == "sent"
	})
	if d := f.outbox(id); d.Receipt == nil || d.Receipt.TaskID != "task-send_already_done" ||
		!f.nativeResult(id, "direct-echo") || len(f.calls(false)) != 2 || len(f.calls(true)) != 0 {
		t.Fatalf("unexpected send, query, or successful receipt: %+v", d)
	}
	f.assertLocalEcho("direct-echo", "outbound_echo")
}

func TestEgressDefiniteRejectionStopsAutomaticSend(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, true)
	triggertest.Write(t, filepath.Join(f.Dir, "send-result.json"), []byte(`{"success":false,"error":{"reason":"permission_denied"}}`))
	f.feed(1, "request", "new-peer", "original")
	_ = f.trigger(f.Endpoint, func(c *dwsConfig) { c.SelfChat = selfChatConfig{} })
	id := f.reply("request", "reply")
	triggertest.Wait(t, "known error recorded", func() bool {
		d := f.outbox(id)
		return d.Status == "failed" && d.Receipt != nil && d.Receipt.ErrorCode == "dws_send_rejected" && d.Receipt.RetryAt == ""
	})
	f.Advance(3600)
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
	_ = f.trigger(f.Endpoint)
	f.AssertRouted(f.message("self-question"), 1)
	id := f.reply("self-question", "answer")
	task := f.waitTask(id)
	overlappingFeed := t.TempDir()
	f.feedIn(overlappingFeed, 1, "early-echo", "self", "answer")
	{
		p := f.triggerFeed(f.Endpoint, overlappingFeed, func(c *dwsConfig) { c.SelfID = "" })
		select {
		case err := <-p.Done:
			p.Done <- err
			if err == nil || !strings.Contains(p.Text(), "selfOpenDingtalkId is required") {
				t.Fatalf("expected a configuration error: %v, %s", err, p.Text())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("overlapping Trigger started without native self identity")
		}
	}
	for _, message := range triggertest.ReadMessages(t, f.Endpoint) {
		if message["sourceMessageId"] == "early-echo" {
			t.Fatal("invalid configuration submitted the early echo")
		}
	}
	f.feed(2, "early-echo", "self", "answer")
	f.assertLocalEcho("early-echo", "held")
	f.settle(task, fixtureSelfConversation, "early-echo")
	f.assertLocalEcho("early-echo", "outbound_echo")
	t.Log("configuration without selfOpenDingtalkId failed before overlapping intake; the configured Trigger routed genuine self input and suppressed its early outbound echo")
}

func killAdapter(t *testing.T, p *triggertest.Process) {
	t.Helper()
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.Done:
		p.Done <- err
		if err == nil {
			t.Fatal("expected killed process")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("killed adapter did not exit")
	}
}

func TestEgressEarlyQuoteWaitsForCoreAndRecoversAfterRestart(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	var allow, sawSent atomic.Bool
	proxy := f.Proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, outboxPath+"/") {
			return false
		}
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		var request outboxCommand
		if json.Unmarshal(data, &request) == nil && request.Outcome == "sent" && !allow.Load() {
			sawSent.Store(true)
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}, nil)
	f.feed(1, "request", "peer", "request")
	first := f.trigger(proxy.URL)
	id := f.reply("request", "answer")
	task := f.waitTask(id)
	quote := event("quoted", "responding to your answer")
	quote["type"], quote["conversation_id"], quote["sender_open_dingtalk_id"] = directEvent, egressConversation, "peer"
	quote["quoted_message"] = map[string]string{"message_id": "answer"}
	triggertest.Write(t, filepath.Join(f.Dir, "event-002.json"), testMarshalMessage(t, quote))
	f.assertLocalEcho("quoted", "held")
	f.feed(3, "unrelated", "peer", "normal input")
	f.AssertRouted(f.message("unrelated"), 1)
	f.settle(task, egressConversation, "answer")
	triggertest.Wait(t, "native success recorded before Core settlement", func() bool {
		return sawSent.Load() && f.nativeResult(id, "answer")
	})
	f.assertLocalEcho("quoted", "held")
	killAdapter(t, first)
	allow.Store(true)
	_ = f.trigger(proxy.URL)
	msg := f.message("quoted")
	var detail map[string]any
	code := triggertest.Request(t, http.MethodGet, f.Endpoint, "/v1/core/trigger-messages/"+msg["id"].(string), "", nil, &detail)
	association, _ := detail["quotedReply"].(map[string]any)
	if code != http.StatusOK || association["outboxId"] != id {
		t.Fatalf("missing native quote association: HTTP %d, %+v", code, detail)
	}
	f.AssertRouted(msg, 1)
	f.assertContentSentOnce("answer")
}

func TestDWSStateOwnerRejectsDifferentDatabase(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	first := f.trigger(f.Endpoint)
	first.Endpoint(t, "statusEndpoint")
	second := f.trigger(f.Endpoint, func(c *dwsConfig) {
		c.Profile = "different-corp:account"
		c.DatabasePath = filepath.Join(t.TempDir(), "another.sqlite")
		c.CoreEndpoint += "/"
	})
	expectProcessFailure(t, second, "another DWS process owns this account and Core endpoint")
}

func TestEgressLateTaskResultSettlesBeforeRetry(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, true)
	proxy := f.Proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, outboxPath+"/") {
			http.Error(w, "test unavailable", http.StatusServiceUnavailable)
			return true
		}
		return false
	}, nil)
	f.feed(1, "request", "self", "request")
	trigger := f.trigger(proxy.URL)
	id := f.reply("request", "answer")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("DWS Trigger log:\n%s", trigger.Text())
		}
	})
	task := f.waitTask(id)
	triggertest.Write(t, filepath.Join(f.Dir, task+".json"), []byte(`{"success":false,"error":{"reason":"task_not_found"}}`))
	triggertest.Wait(t, "task became unavailable", func() bool {
		item := f.outbox(id)
		return item.Status == "failed" && item.Receipt != nil && item.Receipt.Uncertain
	})
	f.feed(2, "late-echo", "self", "answer")
	f.assertLocalEcho("late-echo", "held")
	f.settle(task, fixtureSelfConversation, "late-echo")
	f.assertLocalEcho("late-echo", "outbound_echo")
	f.Advance(61)
	triggertest.Wait(t, "late receipt settled after the retry deadline", func() bool {
		return f.outbox(id).Status == "sent"
	})
	f.assertContentSentOnce("answer")
}
