package core_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const channelOutbox = "/v1/channel/outbox"

func configureOutboxSource(t *testing.T, f *fixture) {
	t.Helper()
	cfg, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(cfg), "    tokenFile:", "    instanceId: collector-1\n    outboxConsumer: true\n    tokenFile:", 1))
}

func addOutboxPeerSources(t *testing.T, f *fixture) (sameSource, otherSource, ingressOnly string) {
	t.Helper()
	cfg, err := os.ReadFile(f.config)
	require.NoError(t, err)
	sameSource, otherSource, ingressOnly = strings.Repeat("b", 48), strings.Repeat("c", 48), strings.Repeat("d", 48)
	for name, token := range map[string]string{"same": sameSource, "other": otherSource, "ingress": ingressOnly} {
		write(t, filepath.Join(f.dir, name+".token"), token)
	}
	write(t, f.config, string(cfg)+
		"  - source: dws\n    accountId: account-1\n    instanceId: collector-2\n    tokenFile: ./same.token\n"+
		"  - source: other\n    accountId: account-2\n    instanceId: collector-1\n    tokenFile: ./other.token\n"+
		"  - source: dws\n    accountId: account-1\n    tokenFile: ./ingress.token\n")
	return sameSource, otherSource, ingressOnly
}

func readOutbox(t *testing.T, p *process, id string) core.OutboxItem {
	t.Helper()
	code, data := request(t, "GET", p.endpoint, "/v1/observer/outbox/"+id, "", nil)
	require.Equal(t, 200, code, "%s", data)
	require.NotContains(t, string(data), "attempts")
	var detail core.OutboxItem
	require.NoError(t, json.Unmarshal(data, &detail))
	return detail
}

func listOutbox(t *testing.T, endpoint, token, query string) core.Page {
	t.Helper()
	code, data := request(t, "GET", endpoint, channelOutbox+query, token, nil)
	require.Equal(t, 200, code, "%s", data)
	var result core.Page
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func resultOutbox(t *testing.T, p *process, token string, req core.OutboxCommand) core.OutboxItem {
	t.Helper()
	code, data := request(t, "POST", p.endpoint, channelOutbox+"/result", token, req)
	require.Equal(t, 200, code, "%s", data)
	var result core.OutboxItem
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, req.OutboxID, result.ID)
	stored := readOutbox(t, p, req.OutboxID)
	require.Equal(t, stored.OutboxState, result.OutboxState)
	return stored
}

func outboxResult(item core.OutboxItem, outcome, code, native string) core.OutboxCommand {
	r := core.OutboxCommand{OutboxID: item.ID, Expected: item.OutboxState, Outcome: outcome, ErrorCode: code, MessageID: native}
	if native != "" {
		r.ConversationID = "actual:conversation"
	}
	if item.Receipt != nil && item.Status == "pending" {
		r.TaskID = item.Receipt.TaskID
	}
	return r
}

func outboxClock(t *testing.T, f *fixture) (string, func(time.Duration) time.Time) {
	t.Helper()
	return processClock(t, f, "SUPERAGENT_TEST_OUTBOX_CLOCK")
}

func processClock(t *testing.T, f *fixture, env string) (string, func(time.Duration) time.Time) {
	t.Helper()
	path := filepath.Join(f.dir, env)
	start := time.Now().UTC()
	advance := func(offset time.Duration) time.Time {
		t.Helper()
		at := start.Add(offset)
		write(t, path+".next", at.Format(time.RFC3339Nano))
		require.NoError(t, os.Rename(path+".next", path))
		return at
	}
	advance(0)
	return env + "=" + path, advance
}

func TestOutboxRoleTargetAndScope(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	peer, other, intake := addOutboxPeerSources(t, f)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	routerMCP := startRoleMCP(t, f, router)
	require.Contains(t, string(routerMCP.call("tools/list", map[string]any{})["result"]), "message_reply")

	addressless := input("addressless")
	addressless.Message.Channel = nil
	addressless.Message.At = "2098-01-02T08:00:00Z"
	first := submit(t, p, f, addressless)
	addressed := input("verified-instance")
	addressed.Message.At = "2000-01-01T12:00:00Z"
	addressed.Message.CollectorInstance = "collector-2"
	second := submit(t, p, f, addressed)
	code, raw := request(t, "POST", p.endpoint, "/v1/channel/messages", peer, addressed)
	require.Equal(t, 200, code, "%s", raw)
	route := decide(t, p, routerToken, newRoute("outbox-scope", []string{second.MessageID, first.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)
	unrelatedInput := input("unrelated")
	unrelatedInput.Message.ThreadRef.ID, unrelatedInput.Message.Channel.ConversationID = "another-conversation", "another-conversation"
	unrelated := submit(t, p, f, unrelatedInput)
	otherDecision := decide(t, p, routerToken, newRoute("other-topic", []string{unrelated.MessageID}, f.dir))
	otherTopic := waitBinding(t, p, "topic", otherDecision.Targets[0].TopicID, "ready")

	for _, tc := range []struct {
		token string
		req   core.ReplyRequest
		code  string
		http  int
	}{
		{routerToken, core.ReplyRequest{OperationID: "empty", Text: "no current range"}, "invalid_argument", 400},
		{routerToken, core.ReplyRequest{OperationID: "no-address-router", Text: "no fallback", MessageIDs: []string{second.MessageID, first.MessageID}}, "failed_precondition", 409},
		{routerToken, core.ReplyRequest{OperationID: "missing", Text: "require every supplied Message", MessageIDs: []string{second.MessageID, "absent"}}, "not_found", 404},
		{topicToken, core.ReplyRequest{OperationID: "no-address-topic", Text: "no fallback"}, "failed_precondition", 409},
		{topicToken, core.ReplyRequest{OperationID: "outside", Text: "out of Topic", MessageID: unrelated.MessageID}, "forbidden", 403},
	} {
		code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", tc.token, tc.req)
		errorCode(t, code, raw, tc.http, tc.code)
	}
	topicMCP := startRoleMCP(t, f, topic)
	args := core.ReplyRequest{OperationID: "explicit", Text: "From the owning Topic", MessageID: second.MessageID}
	mcpReply := topicMCP.call("tools/call", map[string]any{"name": "message_reply", "arguments": args})
	var mcpResult struct {
		StructuredContent core.OutboxItem
	}
	require.NoError(t, json.Unmarshal(mcpReply["result"], &mcpResult))
	reply := roleWork[core.OutboxItem](t, p, topicToken, "message_reply", args)
	require.Equal(t, mcpResult.StructuredContent, reply)
	require.Equal(t, second.MessageID, reply.MessageID)
	require.Equal(t, "pending", reply.Status)
	require.Nil(t, reply.Receipt)
	args.Text = "changed payload"
	code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", topicToken, args)
	errorCode(t, code, raw, 409, "conflict")

	topicReply := roleWork[core.OutboxItem](t, p, roleToken(t, f, otherTopic), "message_reply", core.ReplyRequest{OperationID: "default", Text: "Own oldest Message"})
	require.Equal(t, unrelated.MessageID, topicReply.MessageID)
	rangeArgs := core.ReplyRequest{OperationID: "router-default", Text: "Current range oldest", MessageIDs: []string{unrelated.MessageID, second.MessageID}}
	routerReply := roleWork[core.OutboxItem](t, p, routerToken, "message_reply", rangeArgs)
	require.Equal(t, second.MessageID, routerReply.MessageID)
	rangeArgs.MessageIDs[0], rangeArgs.MessageIDs[1] = rangeArgs.MessageIDs[1], rangeArgs.MessageIDs[0]
	require.Equal(t, routerReply, roleWork[core.OutboxItem](t, p, routerToken, "message_reply", rangeArgs))

	create := issueInput(f.dir, "outbox-worker")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "issue_create", create)
	worker := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	workerMCP := startRoleMCP(t, f, worker)
	require.NotContains(t, string(workerMCP.call("tools/list", map[string]any{})["result"]), "message_reply")
	code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", roleToken(t, f, worker), args)
	errorCode(t, code, raw, 403, "forbidden")
	code, raw = request(t, "POST", p.endpoint, "/v1/role/source_query", topicToken, map[string]string{"collection": "outbox", "id": topicReply.ID})
	errorCode(t, code, raw, 404, "not_found")
	own := roleWork[core.Page](t, p, topicToken, "source_query", map[string]string{"collection": "outbox", "threadKey": second.ThreadKey})
	require.Len(t, own.Items, 1)
	require.Contains(t, string(own.Items[0]), reply.ID)
	require.Len(t, page(t, p, "outbox?messageId="+second.MessageID).Items, 2)
	require.Len(t, page(t, p, "outbox?threadKey="+url.QueryEscape(second.ThreadKey)).Items, 2)

	firstPage := listOutbox(t, p.endpoint, f.token, "?limit=1")
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "?cursor="+firstPage.NextCursor).Items, 2)
	require.Empty(t, listOutbox(t, p.endpoint, other, "").Items)
	var address struct {
		AccountID, Source string
		Channel           core.Channel
	}
	code, raw = request(t, "GET", p.endpoint, channelOutbox+"/items/"+reply.ID, f.token, nil)
	require.Equal(t, 200, code, "%s", raw)
	require.NoError(t, json.Unmarshal(raw, &address))
	require.Equal(t, "account-1", address.AccountID)
	require.Equal(t, "dws", address.Source)
	require.Equal(t, *addressed.Message.Channel, address.Channel)

	result := outboxResult(reply, "sent", "", "forbidden-write")
	for _, tc := range []struct {
		token   string
		code    int
		problem string
	}{{peer, 403, "forbidden"}, {other, 404, "not_found"}, {intake, 403, "forbidden"}} {
		code, raw = request(t, "GET", p.endpoint, channelOutbox+"/items/"+reply.ID, tc.token, nil)
		errorCode(t, code, raw, tc.code, tc.problem)
		code, raw = request(t, "POST", p.endpoint, channelOutbox+"/result", tc.token, result)
		errorCode(t, code, raw, tc.code, tc.problem)
	}
	code, raw = request(t, "GET", p.endpoint, channelOutbox, peer, nil)
	errorCode(t, code, raw, 403, "forbidden")
	code, raw = request(t, "POST", p.endpoint, "/v1/channel/messages", peer, input("passive-collector"))
	require.Equal(t, 200, code, "%s", raw)
	var passive core.Receipt
	require.NoError(t, json.Unmarshal(raw, &passive))
	roleWork[core.OutboxItem](t, p, routerToken, "message_reply",
		core.ReplyRequest{OperationID: "reply-on-account", Text: "Send via configured consumer", MessageID: passive.MessageID})
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 4)
	code, raw = request(t, "GET", p.endpoint, channelOutbox, "", nil)
	errorCode(t, code, raw, 401, "unauthorized")
	code, _ = request(t, "POST", p.endpoint, "/v1/observer/outbox/"+reply.ID, "", struct{}{})
	require.Equal(t, 405, code)
	require.Equal(t, "pending", readOutbox(t, p, reply.ID).Status)
	require.Nil(t, readOutbox(t, p, reply.ID).Receipt)
	t.Log("Router/Topic MCP and role operations, authenticated Message provenance, earliest target/address errors, scoped history and read-only pulls verified")
}

func TestOutboxDurableResultAndRetry(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	message := submit(t, p, f, input("durable-reply"))
	item := roleWork[core.OutboxItem](t, p, token, "message_reply", core.ReplyRequest{
		OperationID: "reply", MessageID: message.MessageID, Text: "Reply in progress",
	})
	record := outboxResult(item, "pending", "", "")
	record.TaskID = "durable-task"
	saved := resultOutbox(t, p, f.token, record)
	require.Equal(t, "pending", saved.Status)
	require.Equal(t, record.TaskID, saved.Receipt.TaskID)
	p.stop(t, true)
	p = f.start(env)
	require.Equal(t, saved, readOutbox(t, p, item.ID))
	require.Equal(t, saved, resultOutbox(t, p, f.token, record))
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)

	wrongTask := outboxResult(saved, "sent", "", "actual-native")
	wrongTask.TaskID = "different-task"
	code, body := request(t, "POST", p.endpoint, channelOutbox+"/result", f.token, wrongTask)
	errorCode(t, code, body, 409, "conflict")
	retry := outboxResult(saved, "retryable_failure", "confirmed_failure", "")
	failed := resultOutbox(t, p, f.token, retry)
	require.Equal(t, "failed", failed.Status)
	require.NotEmpty(t, failed.Receipt.RetryAt)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	advance(59 * time.Second)
	require.Equal(t, failed, resultOutbox(t, p, f.token, retry))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	advance(61 * time.Second)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)
	settle := outboxResult(failed, "sent", "", "actual-native")
	sent := resultOutbox(t, p, f.token, settle)
	require.Equal(t, settle.MessageID, sent.Receipt.MessageID)
	require.Equal(t, sent, resultOutbox(t, p, f.token, settle))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	code, body = request(t, "POST", p.endpoint, channelOutbox+"/result", f.token, retry)
	errorCode(t, code, body, 409, "conflict")
	require.Equal(t, sent, readOutbox(t, p, sent.ID))

	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	var columns string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT group_concat(name) FROM pragma_table_info('outbox')").Scan(&columns))
	require.Equal(t, "id,message_id,text,status,receipt,blocker_ref", columns)
	var oldTables int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name IN ('outbox_attempts','outbox_claims','message_echo','message_echo_candidates','outbound_message_ids')").Scan(&oldTables))
	require.Zero(t, oldTables)
	var brokenKeys int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&brokenKeys))
	require.Zero(t, brokenKeys)
}

func TestOutboxPermanentFailureDelivery(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	target := submit(t, p, f, input("failed-reply"))
	item := roleWork[core.OutboxItem](t, p, token, "message_reply", core.ReplyRequest{
		OperationID: "reply", MessageID: target.MessageID, Text: "Cannot reach this peer",
	})
	cmd := outboxResult(item, "permanent_failure", "permission_denied", "")
	failed := resultOutbox(t, p, f.token, cmd)
	require.Equal(t, "failed", failed.Status)
	require.Empty(t, failed.Receipt.RetryAt)
	p.stop(t, true)
	p = f.start()
	require.Equal(t, failed, resultOutbox(t, p, f.token, cmd))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	items := workDeliveries(t, p, "outbox_failed")
	require.Len(t, items, 1)
	require.Equal(t, "router", items[0].Recipient.Kind)
	require.NotEmpty(t, waitWorkDelivery(t, p, items[0].ID, "sent").SentAt)
}

func seedObserverOutbox(t *testing.T, p *process, f *fixture, routerToken, channel string) {
	t.Helper()
	source := input("outbox-observer-source")
	source.Message.ThreadRef.ID, source.Message.Channel.ConversationID = "outbox-observer-conversation", "outbox-observer-conversation"
	m := submit(t, p, f, source)
	for _, outcome := range []string{"pending", "task", "unknown", "permanent_failure", "sent"} {
		op := "browser-outbox-" + outcome
		item := roleWork[core.OutboxItem](t, p, routerToken, "message_reply", core.ReplyRequest{OperationID: op, Text: "Observer reply " + outcome, MessageID: m.MessageID})
		if outcome == "pending" {
			continue
		}
		var req core.OutboxCommand
		switch outcome {
		case "task":
			req = outboxResult(item, "pending", "", "")
			req.TaskID = "observer-task-42"
		case "sent":
			code, nativeID := request(t, http.MethodPost, channel, "/send", "", item)
			require.Equal(t, http.StatusOK, code)
			req = outboxResult(item, outcome, "", string(nativeID))
		default:
			req = outboxResult(item, outcome, "observer_"+outcome, "")
		}
		resultOutbox(t, p, f.token, req)
	}
	echo := input("observer-actual-message")
	echo.Message.ThreadRef.ID, echo.Message.Channel.ConversationID = "actual:conversation", "actual:conversation"
	submit(t, p, f, echo)
}
