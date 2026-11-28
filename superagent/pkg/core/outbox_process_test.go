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

const channelOutbox = "/apis/core/v1/outbox-messages"

func markdownReply(text string) core.StructuredContent {
	data, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		panic(err)
	}
	return core.StructuredContent{Type: "markdown", Data: data}
}

func readOutbox(t *testing.T, p *process, id string) core.OutboxItem {
	t.Helper()
	code, data := request(t, "GET", p.endpoint, channelOutbox+"/"+id, "", nil)
	require.Equal(t, 200, code, "%s", data)
	require.NotContains(t, string(data), "attempts")
	var detail core.OutboxItem
	require.NoError(t, json.Unmarshal(data, &detail))
	return detail
}

func listOutbox(t *testing.T, endpoint, token, query string, sources ...core.Source) core.Page {
	t.Helper()
	code, data := request(t, "GET", endpoint, channelOutbox+"?view=delivery&"+strings.TrimPrefix(query, "?"), token, nil, sources...)
	require.Equal(t, 200, code, "%s", data)
	var result core.Page
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func resultOutbox(t *testing.T, p *process, token string, req core.OutboxCommand, sources ...core.Source) core.OutboxItem {
	t.Helper()
	code, data := patchOutbox(t, p, token, req, sources...)
	require.Equal(t, 200, code, "%s", data)
	var result core.OutboxItem
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, req.OutboxID, result.ID)
	stored := readOutbox(t, p, req.OutboxID)
	require.Equal(t, stored.OutboxState, result.OutboxState)
	return stored
}

func patchOutbox(t *testing.T, p *process, token string, req core.OutboxCommand, sources ...core.Source) (int, []byte) {
	t.Helper()
	body := struct {
		Expected  core.OutboxState `json:"expected"`
		Outcome   string           `json:"outcome"`
		TaskID    string           `json:"taskId,omitempty"`
		ErrorCode string           `json:"errorCode,omitempty"`
	}{Expected: req.Expected, Outcome: req.Outcome, TaskID: req.TaskID, ErrorCode: req.ErrorCode}
	return request(t, http.MethodPatch, p.endpoint, channelOutbox+"/"+req.OutboxID, token, body, sources...)
}

func outboxResult(item core.OutboxItem, outcome, code string) core.OutboxCommand {
	r := core.OutboxCommand{OutboxID: item.ID, Expected: item.OutboxState, Outcome: outcome, ErrorCode: code}
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
	other := core.Source{Source: "dws", AccountID: "account-2"}

	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	routerMCP := startRoleMCP(t, f, router)
	require.Contains(t, string(routerMCP.call("tools/list", map[string]any{})["result"]), "send_reply")

	addressless := input("addressless")
	addressless.ReplyTarget = nil
	addressless.Message.Meta = json.RawMessage(`{"sourceTime":"2098-01-02T08:00:00Z"}`)
	first := submit(t, p, f, addressless)
	addressed := input("verified-instance")
	addressed.Message.Meta = json.RawMessage(`{"sourceTime":"2000-01-01T12:00:00Z","collectorInstance":"collector-2"}`)
	second := submit(t, p, f, addressed)
	var code int
	var raw []byte
	route := decide(t, p, routerToken, newRoute("outbox-scope", []string{second.MessageID, first.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)
	unrelatedInput := input("unrelated")
	moveInput(&unrelatedInput, "another-conversation")
	unrelated := submit(t, p, f, unrelatedInput)
	otherDecision := decide(t, p, routerToken, newRoute("other-topic", []string{unrelated.MessageID}, f.dir))
	otherTopic := waitBinding(t, p, "topic", otherDecision.Targets[0].TopicID, "ready")

	for _, tc := range []struct {
		token string
		req   core.ReplyRequest
		code  string
		http  int
	}{
		{routerToken, core.ReplyRequest{OperationID: "empty", Content: markdownReply("no current range")}, "invalid_argument", 400},
		{routerToken, core.ReplyRequest{OperationID: "no-address-router", Content: markdownReply("no fallback"), MessageID: first.MessageID}, "failed_precondition", 409},
		{routerToken, core.ReplyRequest{OperationID: "missing", Content: markdownReply("require every supplied Message"), MessageID: "absent"}, "not_found", 404},
		{topicToken, core.ReplyRequest{OperationID: "no-address-topic", MessageID: first.MessageID, Content: markdownReply("no fallback")}, "failed_precondition", 409},
		{topicToken, core.ReplyRequest{OperationID: "outside", Content: markdownReply("out of Topic"), MessageID: unrelated.MessageID}, "forbidden", 403},
	} {
		code, raw = requestMCPTool(t, p.endpoint, "send_reply", tc.token, tc.req)
		errorCode(t, code, raw, tc.http, tc.code)
	}
	topicMCP := startRoleMCP(t, f, topic)
	args := core.ReplyRequest{OperationID: "explicit", Content: markdownReply("From the owning Topic"), MessageID: second.MessageID}
	mcpReply := topicMCP.call("tools/call", map[string]any{"name": "send_reply", "arguments": args})
	var mcpResult struct {
		StructuredContent core.OutboxItem
	}
	require.NoError(t, json.Unmarshal(mcpReply["result"], &mcpResult))
	reply := roleWork[core.OutboxItem](t, p, topicToken, "send_reply", args)
	require.Equal(t, mcpResult.StructuredContent, reply)
	require.Equal(t, second.MessageID, reply.MessageID)
	require.Equal(t, "pending", reply.Status)
	require.Nil(t, reply.Receipt)
	args.Content = markdownReply("changed payload")
	code, raw = requestMCPTool(t, p.endpoint, "send_reply", topicToken, args)
	errorCode(t, code, raw, 409, "conflict")

	topicReply := roleWork[core.OutboxItem](t, p, roleToken(t, f, otherTopic), "send_reply", core.ReplyRequest{OperationID: "own-message", MessageID: unrelated.MessageID, Content: markdownReply("Own Message")})
	require.Equal(t, unrelated.MessageID, topicReply.MessageID)
	routerArgs := core.ReplyRequest{OperationID: "router-reply", Content: markdownReply("Router reply"), MessageID: second.MessageID}
	routerReply := roleWork[core.OutboxItem](t, p, routerToken, "send_reply", routerArgs)
	require.Equal(t, second.MessageID, routerReply.MessageID)
	require.Equal(t, routerReply, roleWork[core.OutboxItem](t, p, routerToken, "send_reply", routerArgs))

	create := issueInput(f.dir, "outbox-issue")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "create_issue", create)
	issue := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	issueMCP := startRoleMCP(t, f, issue)
	require.NotContains(t, string(issueMCP.call("tools/list", map[string]any{})["result"]), "send_reply")
	code, raw = requestMCPTool(t, p.endpoint, "send_reply", roleToken(t, f, issue), args)
	errorCode(t, code, raw, 403, "forbidden")
	own := roleWork[struct{ Replies []core.OutboxItem }](t, p, topicToken, "get_message", map[string]any{
		"messageId": second.MessageID, "include": []string{"replies"},
	})
	require.Len(t, own.Replies, 1)
	require.Equal(t, reply.ID, own.Replies[0].ID)
	require.Len(t, page(t, p, "outbox-messages?messageId="+second.MessageID).Items, 2)
	require.Len(t, page(t, p, "outbox-messages?threadId="+url.QueryEscape(second.ThreadID)).Items, 2)

	firstPage := listOutbox(t, p.endpoint, f.token, "?limit=1")
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "?cursor="+firstPage.NextCursor).Items, 2)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "", other).Items)
	code, raw = request(t, "GET", p.endpoint, channelOutbox+"?view=delivery&cursor="+firstPage.NextCursor, f.token, nil, other)
	errorCode(t, code, raw, http.StatusBadRequest, "invalid_argument")
	var address struct {
		AccountID, Source string
		ReplyTarget       json.RawMessage
	}
	code, raw = request(t, "GET", p.endpoint, channelOutbox+"/"+reply.ID+"?view=delivery", f.token, nil)
	require.Equal(t, 200, code, "%s", raw)
	require.NoError(t, json.Unmarshal(raw, &address))
	require.Equal(t, "account-1", address.AccountID)
	require.Equal(t, "dws", address.Source)
	require.JSONEq(t, string(addressed.ReplyTarget), string(address.ReplyTarget))

	result := outboxResult(reply, "sent", "")
	for _, tc := range []struct {
		source  core.Source
		code    int
		problem string
	}{
		{other, http.StatusNotFound, "not_found"},
		{core.Source{Source: "another", AccountID: fixtureTriggerSource.AccountID}, http.StatusNotFound, "not_found"},
		{core.Source{Source: "dws"}, http.StatusBadRequest, "invalid_argument"},
	} {
		code, raw = request(t, "GET", p.endpoint, channelOutbox+"/"+reply.ID+"?view=delivery", f.token, nil, tc.source)
		errorCode(t, code, raw, tc.code, tc.problem)
		code, raw = patchOutbox(t, p, f.token, result, tc.source)
		errorCode(t, code, raw, tc.code, tc.problem)
	}
	code, raw = request(t, http.MethodPost, p.endpoint, "/apis/core/v1/trigger-messages", f.token, input("new-account"), other)
	require.Equal(t, http.StatusOK, code, "%s", raw)
	var accepted core.Receipt
	require.NoError(t, json.Unmarshal(raw, &accepted))
	newReply := roleWork[core.OutboxItem](t, p, routerToken, "send_reply",
		core.ReplyRequest{OperationID: "reply-on-account", Content: markdownReply("Reply to a new account"), MessageID: accepted.MessageID})
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 3)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "", other).Items, 1)
	require.Equal(t, "sent", resultOutbox(t, p, f.token, outboxResult(newReply, "sent", ""), other).Status)
	for _, token := range []string{"", "wrong-token"} {
		for _, path := range []string{channelOutbox, channelOutbox + "/" + reply.ID} {
			code, raw = request(t, http.MethodGet, p.endpoint, path+"?view=delivery", token, nil)
			errorCode(t, code, raw, http.StatusUnauthorized, "unauthorized")
		}
		code, raw = patchOutbox(t, p, token, result)
		errorCode(t, code, raw, http.StatusUnauthorized, "unauthorized")
	}
	code, _ = request(t, "POST", p.endpoint, "/apis/core/v1/outbox-messages/"+reply.ID, "", struct{}{})
	require.Equal(t, 405, code)
	require.Equal(t, "pending", readOutbox(t, p, reply.ID).Status)
	require.Nil(t, readOutbox(t, p, reply.ID).Receipt)
	t.Log("Router/Topic MCP and role operations, authenticated Message provenance, earliest target/address errors, scoped history and read-only pulls verified")
}

func TestOutboxDurableResultAndRetry(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)

	f.execution(boundary)
	env, advance := outboxClock(t, f)
	receiver, spans := coreOTLPReceiver(t, "")
	telemetryEnv := append(traceTestEnv(receiver.URL), env)
	p := f.start(telemetryEnv...)
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	message := submit(t, p, f, input("durable-reply"))
	item := roleWork[core.OutboxItem](t, p, token, "send_reply", core.ReplyRequest{
		OperationID: "reply", MessageID: message.MessageID, Content: markdownReply("Reply in progress"),
	})
	enqueued := observedTraces(t, p, "outbox-messages", item.ID)
	require.Len(t, enqueued, 1)
	require.Equal(t, "enqueue", enqueued[0].Phase)
	record := outboxResult(item, "pending", "")
	record.TaskID = "durable-task"
	saved := resultOutbox(t, p, f.token, record)
	require.Equal(t, "pending", saved.Status)
	require.Equal(t, record.TaskID, saved.Receipt.TaskID)
	p.stop(t, true)
	p = f.start(telemetryEnv...)
	require.Equal(t, enqueued, observedTraces(t, p, "outbox-messages", item.ID))
	require.Equal(t, saved, readOutbox(t, p, item.ID))
	require.Equal(t, saved, resultOutbox(t, p, f.token, record))
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)

	wrongTask := outboxResult(saved, "sent", "")
	wrongTask.TaskID = "different-task"
	code, body := patchOutbox(t, p, f.token, wrongTask)
	errorCode(t, code, body, 409, "conflict")
	retry := outboxResult(saved, "retryable_failure", "confirmed_failure")
	failed := resultOutbox(t, p, f.token, retry)
	require.Equal(t, "failed", failed.Status)
	require.NotEmpty(t, failed.Receipt.RetryAt)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	advance(59 * time.Second)
	require.Equal(t, failed, resultOutbox(t, p, f.token, retry))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	advance(61 * time.Second)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)
	settle := outboxResult(failed, "sent", "")
	sent := resultOutbox(t, p, f.token, settle)
	require.Equal(t, "sent", sent.Status)
	require.Nil(t, sent.Receipt)
	require.Equal(t, sent, resultOutbox(t, p, f.token, settle))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	code, body = patchOutbox(t, p, f.token, retry)
	errorCode(t, code, body, 409, "conflict")
	require.Equal(t, sent, readOutbox(t, p, sent.ID))

	p.stop(t, false)

	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	var columns string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT group_concat(name) FROM pragma_table_info('outbox')").Scan(&columns))
	require.Equal(t, "id,message_id,content,status,receipt,blocker_ref", columns)
	for _, invalid := range []any{nil, "plain text", "[]", "null"} {
		_, err = db.ExecContext(t.Context(), "UPDATE outbox SET content=? WHERE id=?", invalid, sent.ID)
		require.ErrorContains(t, err, "constraint failed")
	}
	var oldTables int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name IN ('outbox_attempts','outbox_claims','message_echo','message_echo_candidates','outbound_message_ids')").Scan(&oldTables))
	require.Zero(t, oldTables)
	var brokenKeys int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&brokenKeys))
	require.Zero(t, brokenKeys)
	var settlements int
	for _, span := range spans() {
		if span.GetName() == "superagent.outbox.settle" {
			require.True(t, spanLinksTrace(span, enqueued[0].TraceID))
			settlements++
		}
	}
	require.GreaterOrEqual(t, settlements, 3)
}

func TestOutboxStructuredContentDiscoveryAndValidation(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)

	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	client := startRoleMCP(t, f, router)
	tools := client.call("tools/list", map[string]any{})
	require.Contains(t, string(tools["result"]), `"name":"send_reply"`)
	require.NotContains(t, string(tools["result"]), `"name":"message_reply"`)
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(tools["result"], &result))
	var contentSchema json.RawMessage
	for _, tool := range result.Tools {
		if tool.Name == "send_reply" {
			contentSchema = tool.InputSchema.Properties["content"]
			break
		}
	}
	require.NotEmpty(t, contentSchema)
	require.Contains(t, string(contentSchema), `"const":"markdown"`)
	require.Contains(t, string(contentSchema), `"const":"options"`)
	require.Contains(t, string(contentSchema), `"required":["prompt","options"]`)
	message := submit(t, p, f, input("structured-reply"))
	call := func(content any) map[string]any {
		return map[string]any{"operationId": "structured", "messageId": message.MessageID, "content": content}
	}
	for _, invalid := range []string{
		`[]`, `{"type":"unsupported","data":{}}`, `{"type":"markdown","data":{}}`,
		`{"type":"options","data":{"prompt":"Prompt"}}`,
		`{"type":"options","data":{"prompt":"Prompt","options":[{"id":"same","label":"A"},{"id":"same","label":"B"}]}}`,
	} {
		mcpWorkError(t, client, "send_reply", "invalid_argument", call(json.RawMessage(invalid)))
	}
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	content := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"options":[{"label":"Continue","id":"yes"},{"label":"Pause","id":"no"}],"prompt":"**First line**\n\nSecond line"}`)}
	item := mcpWork[core.OutboxItem](t, client, "send_reply", call(content))
	reordered := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**First line**\n\nSecond line","options":[{"id":"yes","label":"Continue"},{"id":"no","label":"Pause"}]}`)}
	require.Equal(t, item, mcpWork[core.OutboxItem](t, client, "send_reply", call(reordered)))
	changed := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**First line**\n\nSecond line","options":[{"id":"no","label":"Pause"},{"id":"yes","label":"Continue"}]}`)}
	mcpWorkError(t, client, "send_reply", "conflict", call(changed))
	require.JSONEq(t, string(content.Data), string(item.Content.Data))
	markdown := markdownReply("**First line**\n\nSecond line")
	markdownItem := mcpWork[core.OutboxItem](t, client, "send_reply", core.ReplyRequest{
		OperationID: "markdown", MessageID: message.MessageID, Content: markdown,
	})
	require.Equal(t, markdown, markdownItem.Content)
	p.stop(t, true)
	p = f.start()
	require.Equal(t, item, readOutbox(t, p, item.ID))
	require.Equal(t, markdownItem, readOutbox(t, p, markdownItem.ID))
	rows := listOutbox(t, p.endpoint, f.token, "").Items
	require.Len(t, rows, 2)
	var channelItems []core.OutboxItem
	for _, row := range rows {
		var channelItem core.OutboxItem
		require.NoError(t, json.Unmarshal(row, &channelItem))
		channelItems = append(channelItems, channelItem)
	}
	require.ElementsMatch(t, []core.OutboxItem{item, markdownItem}, channelItems)
	code, raw := request(t, "GET", p.endpoint, channelOutbox+"/"+item.ID+"?view=delivery", f.token, nil)
	require.Equal(t, http.StatusOK, code)
	var channelItem core.OutboxItem
	require.NoError(t, json.Unmarshal(raw, &channelItem))
	require.Equal(t, item, channelItem)
}

func TestOutboxPermanentFailureEvent(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)

	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	target := submit(t, p, f, input("failed-reply"))
	item := roleWork[core.OutboxItem](t, p, token, "send_reply", core.ReplyRequest{
		OperationID: "reply", MessageID: target.MessageID, Content: markdownReply("Cannot reach this peer"),
	})
	cmd := outboxResult(item, "permanent_failure", "permission_denied")
	failed := resultOutbox(t, p, f.token, cmd)
	require.Equal(t, "failed", failed.Status)
	require.Empty(t, failed.Receipt.RetryAt)
	p.stop(t, true)
	p = f.start()
	require.Equal(t, failed, resultOutbox(t, p, f.token, cmd))
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	items := workEvents(t, p, "outbox_failed")
	require.Len(t, items, 1)
	require.Equal(t, "router", items[0].Recipient.Kind)
	require.NotEmpty(t, waitWorkEvent(t, p, items[0].ID, "sent").SentAt)
}

func seedObserverOutbox(t *testing.T, p *process, f *fixture, routerToken, channel string) {
	t.Helper()
	source := input("outbox-observer-source")
	moveInput(&source, "outbox-observer-conversation")
	m := submit(t, p, f, source)
	for _, outcome := range []string{"pending", "task", "unknown", "permanent_failure", "sent"} {
		op := "browser-outbox-" + outcome
		item := roleWork[core.OutboxItem](t, p, routerToken, "send_reply", core.ReplyRequest{OperationID: op, Content: markdownReply("Observer reply " + outcome), MessageID: m.MessageID})
		if outcome == "pending" {
			continue
		}
		var req core.OutboxCommand
		switch outcome {
		case "task":
			req = outboxResult(item, "pending", "")
			req.TaskID = "observer-task-42"
		case "sent":
			code, _ := request(t, http.MethodPost, channel, "/send", "", item)
			require.Equal(t, http.StatusOK, code)
			req = outboxResult(item, outcome, "")
			req.TaskID = "observer-send-task"
		default:
			req = outboxResult(item, outcome, "observer_"+outcome)
		}
		resultOutbox(t, p, f.token, req)
	}
	roleWork[core.OutboxItem](t, p, routerToken, "send_reply", core.ReplyRequest{
		OperationID: "browser-outbox-markdown", MessageID: m.MessageID,
		Content: markdownReply("**Formatted reply**"),
	})
	roleWork[core.OutboxItem](t, p, routerToken, "send_reply", core.ReplyRequest{
		OperationID: "browser-outbox-options", MessageID: m.MessageID,
		Content: core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**Choose what to do.**\n\n![Do not fetch](https://example.invalid/outbox-image.png)\n\n<span data-marker=\"outbox-raw-html\">raw HTML</span>","options":[{"id":"pause","label":"Pause"},{"id":"continue","label":"Continue"}]}`)},
	})
}
