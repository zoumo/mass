package core_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestStructuredTriggerOutboxProcess(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	consumer, peer, other := strings.Repeat("r", 48), strings.Repeat("s", 48), strings.Repeat("t", 48)
	for name, token := range map[string]string{"consumer": consumer, "peer": peer, "other": other} {
		write(t, filepath.Join(f.dir, name+".token"), token)
	}
	write(t, f.config, string(config)+`
  - source: synthetic
    accountId: account-test
    instanceId: sender
    outboxConsumer: true
    tokenFile: ./consumer.token
    threadIdPrefixes: ['opaque/']
  - source: synthetic
    accountId: account-test
    instanceId: ingress
    tokenFile: ./peer.token
    threadIdPrefixes: ['opaque/']
  - source: another
    accountId: account-other
    instanceId: sender
    tokenFile: ./other.token
    threadIdPrefixes: ['opaque/']
`)
	f.execution(boundary)
	clock, advance := outboxClock(t, f)
	p := f.start(clock)
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	const thread = "opaque/Ticket-42"
	newInput := func(id, text string) core.SubmitTriggerMessageRequest {
		return core.SubmitTriggerMessageRequest{OperationID: id, Message: core.TriggerMessage{
			SourceMessageID: id, ThreadID: thread, Content: []events.ContentBlock{events.Text(text)},
		}}
	}
	const ingress = "/v1/core/trigger-messages"
	submit := func(input core.SubmitTriggerMessageRequest) core.Receipt {
		t.Helper()
		status, raw := request(t, http.MethodPost, p.endpoint, ingress, peer, input)
		require.Equal(t, http.StatusOK, status, string(raw))
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		return receipt
	}
	rejectSubmit := func(token string, input core.SubmitTriggerMessageRequest, status int, code string) {
		t.Helper()
		got, raw := request(t, http.MethodPost, p.endpoint, ingress, token, input)
		errorCode(t, got, raw, status, code)
	}
	target := json.RawMessage(`{"route":{"path":["queue",42,null],"pointer":null},"offset":18446744073709551615}`)
	origin := newInput("question-origin", "Please check the rollout.")
	origin.ReplyTarget = target
	message := submit(origin)
	receiptPath := ingress + "?sourceMessageId=question-origin"
	status, raw := request(t, http.MethodGet, p.endpoint, receiptPath, consumer, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var receipt core.Receipt
	require.NoError(t, json.Unmarshal(raw, &receipt))
	require.Equal(t, message, receipt)
	status, raw = request(t, http.MethodGet, p.endpoint, receiptPath, other, nil)
	errorCode(t, status, raw, http.StatusNotFound, "not_found")

	changed := origin
	changed.ReplyTarget = json.RawMessage(`{"offset":18446744073709551615,"route":{"pointer":null,"path":["queue",42,null]}}`)
	replay := submit(changed)
	require.True(t, replay.Duplicate)
	replay.Duplicate = false
	require.Equal(t, message, replay)
	changed.ReplyTarget = json.RawMessage(`{"offset":18446744073709551614,"route":{"pointer":null,"path":["queue",42,null]}}`)
	rejectSubmit(peer, changed, http.StatusConflict, "conflict")
	for _, raw := range []string{`null`, `[]`, `"route"`, `{"route":"` + strings.Repeat("x", 16<<10) + `"}`} {
		invalid := newInput("bad-target", "Invalid target")
		invalid.ReplyTarget = json.RawMessage(raw)
		rejectSubmit(peer, invalid, http.StatusBadRequest, "invalid_argument")
	}

	noTarget := submit(newInput("no-target", "Informational only"))
	status, raw = requestMCPTool(t, p.endpoint, "send_reply", routerToken, core.ReplyRequest{
		OperationID: "unreplyable", MessageID: noTarget.MessageID, Content: markdownReply("Missing target"),
	})
	errorCode(t, status, raw, http.StatusConflict, "failed_precondition")
	decision := decide(t, p, routerToken, newRoute("structured-outbox", []string{message.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	role := roleToken(t, f, topic)
	issue := roleWork[core.WorkReceipt](t, p, role, "create_issue", issueInput(f.dir, "quoted-outbox")).Issue
	addBlocker := func(issue core.Issue, id string) core.Issue {
		t.Helper()
		return roleWork[core.WorkReceipt](t, p, role, "issue_update_blockers", core.UpdateBlockersRequest{
			OperationID: id, IssueID: issue.ID, ExpectedRevision: issue.Revision, Add: []core.BlockerInput{human("Confirm the target")},
		}).Issue
	}
	issue = addBlocker(issue, "wait-for-target")
	ref := &core.BlockerRef{IssueID: issue.ID, BlockerID: issue.Blockers[0].ID}
	reply := core.ReplyRequest{
		OperationID: "ask-target", MessageID: message.MessageID, Content: markdownReply("Which target?"), BlockerRef: ref,
	}
	item := roleWork[core.OutboxItem](t, p, role, "send_reply", reply)
	require.Equal(t, item, roleWork[core.OutboxItem](t, p, role, "send_reply", reply))
	require.Equal(t, ref, item.BlockerRef)
	visible := listOutbox(t, p.endpoint, consumer, "").Items
	require.Len(t, visible, 1)
	require.NotContains(t, string(visible[0]), `"channel"`)
	var pulled struct {
		core.OutboxItem
		Source      string
		AccountID   string
		ReplyTarget json.RawMessage
	}
	require.NoError(t, json.Unmarshal(visible[0], &pulled))
	require.Equal(t, item, pulled.OutboxItem)
	require.Equal(t, "synthetic", pulled.Source)
	require.Equal(t, "account-test", pulled.AccountID)
	require.JSONEq(t, string(target), string(pulled.ReplyTarget))
	require.Contains(t, string(pulled.ReplyTarget), "18446744073709551615")
	record := outboxResult(item, "pending", "")
	record.TaskID = "adapter:opaque:send:01"
	detail := channelOutbox + "/" + item.ID + "?view=delivery"
	for _, test := range []struct {
		token  string
		status int
		code   string
	}{{peer, http.StatusForbidden, "forbidden"}, {other, http.StatusNotFound, "not_found"}} {
		gotStatus, response := request(t, http.MethodGet, p.endpoint, detail, test.token, nil)
		errorCode(t, gotStatus, response, test.status, test.code)
		gotStatus, response = patchOutbox(t, p, test.token, record)
		errorCode(t, gotStatus, response, test.status, test.code)
	}
	require.Empty(t, listOutbox(t, p.endpoint, other, "").Items)
	status, raw = request(t, http.MethodGet, p.endpoint, ingress+"/"+message.MessageID, "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NotContains(t, string(raw), `"replyTarget"`)
	require.NotContains(t, string(raw), `"inReplyToOutboxId"`)
	status, raw = request(t, http.MethodGet, p.endpoint, channelOutbox+"/"+item.ID, "", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NotContains(t, string(raw), `"replyTarget"`)
	require.NotContains(t, string(raw), `"accountId"`)
	status, raw = request(t, http.MethodGet, p.endpoint, detail+"&view=", "", nil)
	errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
	agent := roleWork[core.Message](t, p, role, "get_message", map[string]string{"messageId": message.MessageID})
	require.Empty(t, agent.ReplyTarget)

	answer := newInput("answer", "Use the east region.")
	answer.InReplyToOutboxID = item.ID
	rejectSubmit(peer, answer, http.StatusConflict, "failed_precondition")
	task := resultOutbox(t, p, consumer, record)
	require.Equal(t, record.TaskID, task.Receipt.TaskID)
	require.Equal(t, task, resultOutbox(t, p, consumer, record))
	native := map[string]any{"outboxId": "body-override", "expected": task.OutboxState, "outcome": "sent", "taskId": task.Receipt.TaskID}
	status, raw = request(t, http.MethodPatch, p.endpoint, channelOutbox+"/"+task.ID, consumer, native)
	errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
	uncertain := outboxResult(task, "unknown", "result_missing")
	failed := resultOutbox(t, p, consumer, uncertain)
	require.True(t, failed.Receipt.Uncertain)
	require.NotEmpty(t, failed.Receipt.RetryAt)
	require.Empty(t, listOutbox(t, p.endpoint, consumer, "").Items)
	rejectSubmit(peer, answer, http.StatusConflict, "failed_precondition")
	advance(61 * time.Second)
	require.Equal(t, failed, resultOutbox(t, p, consumer, uncertain))
	require.Len(t, listOutbox(t, p.endpoint, consumer, "").Items, 1)
	command := outboxResult(failed, "sent", "")
	sent := resultOutbox(t, p, consumer, command)
	require.Equal(t, "sent", sent.Status)
	require.True(t, sent.Receipt.Uncertain)
	require.Equal(t, sent, resultOutbox(t, p, consumer, command))
	status, raw = patchOutbox(t, p, consumer, uncertain)
	errorCode(t, status, raw, http.StatusConflict, "conflict")
	require.Equal(t, sent, readOutbox(t, p, item.ID))
	status, raw = request(t, http.MethodGet, p.endpoint, detail, consumer, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NoError(t, json.Unmarshal(raw, &pulled))
	require.Equal(t, sent, pulled.OutboxItem)
	require.JSONEq(t, string(target), string(pulled.ReplyTarget))

	rejectSubmit(other, answer, http.StatusConflict, "failed_precondition")
	wrong := answer
	wrong.Message.ThreadID = "opaque/Other"
	rejectSubmit(peer, wrong, http.StatusConflict, "failed_precondition")
	accepted := submit(answer)
	replay = submit(answer)
	require.True(t, replay.Duplicate)
	replay.Duplicate = false
	require.Equal(t, accepted, replay)
	wrong = answer
	wrong.InReplyToOutboxID = ""
	rejectSubmit(peer, wrong, http.StatusConflict, "conflict")
	association := roleWork[quotedReplyRecord](t, p, role, "get_message", map[string]string{"messageId": accepted.MessageID})
	require.NotNil(t, association.QuotedReply)
	require.Equal(t, item.ID, association.QuotedReply.OutboxID)
	require.Equal(t, *ref, association.QuotedReply.BlockerRef)
	require.Equal(t, association, readRecord[quotedReplyRecord](t, p, "trigger-messages/"+accepted.MessageID))
	waitRouteStatus(t, p, accepted.EventID, "sent")
	promptEvent := func(receipt core.Receipt) map[string]json.RawMessage {
		t.Helper()
		boundary.driver.mu.Lock()
		defer boundary.driver.mu.Unlock()
		for _, input := range boundary.driver.inputs[topic.SessionID] {
			var batch struct {
				Events []map[string]json.RawMessage
			}
			require.NoError(t, json.Unmarshal([]byte(input.Content[0].Text.Text), &batch))
			for _, event := range batch.Events {
				if string(event["event_id"]) == `"`+receipt.EventID+`"` {
					return event
				}
			}
		}
		t.Fatalf("Topic Session did not receive event %s", receipt.EventID)
		return nil
	}
	event := promptEvent(accepted)
	require.Contains(t, string(event["quotedReply"]), item.ID)
	require.Contains(t, string(event["issue"]), ref.BlockerID)
	require.Contains(t, string(event["messageContent"]), accepted.MessageID)
	require.Equal(t, issue, readRecord[core.Issue](t, p, "issues/"+issue.ID))
	resolve := func(issue core.Issue, id, messageID string) core.UpdateBlockersRequest {
		return core.UpdateBlockersRequest{
			OperationID: id, IssueID: issue.ID, ExpectedRevision: issue.Revision,
			UnblockedStatus: "pending",
			Resolve:         []core.BlockerResolution{{BlockerID: issue.Blockers[0].ID, Reason: "Target confirmed", MessageID: messageID}},
		}
	}
	issue = roleWork[core.WorkReceipt](t, p, role, "issue_update_blockers", resolve(issue, "resolve-target", accepted.MessageID)).Issue
	require.Empty(t, issue.Blockers)
	issue = addBlocker(issue, "replacement-target")
	late := newInput("late-answer", "Still east.")
	late.InReplyToOutboxID = item.ID
	lateReceipt := submit(late)
	waitRouteStatus(t, p, lateReceipt.EventID, "sent")
	event = promptEvent(lateReceipt)
	require.NotContains(t, event, "quotedReply")
	require.NotContains(t, event, "issue")
	require.Contains(t, string(event["messageContent"]), lateReceipt.MessageID)
	status, raw = requestMCPTool(t, p.endpoint, "issue_update_blockers", role, resolve(issue, "late-resolution", lateReceipt.MessageID))
	errorCode(t, status, raw, http.StatusConflict, "failed_precondition")
	require.Equal(t, issue, readRecord[core.Issue](t, p, "issues/"+issue.ID))

	reply.OperationID, reply.BlockerRef = "ordinary-reply", nil
	ordinary := roleWork[core.OutboxItem](t, p, role, "send_reply", reply)
	sent = resultOutbox(t, p, consumer, outboxResult(ordinary, "sent", ""))
	require.Nil(t, sent.Receipt)
	quoted := newInput("ordinary-quote", strings.Repeat("Long answer. ", 200))
	quoted.InReplyToOutboxID = ordinary.ID
	quoteReceipt := submit(quoted)
	waitRouteStatus(t, p, quoteReceipt.EventID, "sent")
	event = promptEvent(quoteReceipt)
	require.JSONEq(t, `{"outboxId":"`+ordinary.ID+`"}`, string(event["quotedReply"]))
	require.NotContains(t, event, "issue")
	require.NotContains(t, event, "messageContent")

	reply.OperationID, reply.BlockerRef = "undeliverable", nil
	permanent := roleWork[core.OutboxItem](t, p, role, "send_reply", reply)
	rejected := outboxResult(permanent, "permanent_failure", "recipient_missing")
	require.Equal(t, "failed", resultOutbox(t, p, consumer, rejected).Status)
	require.Equal(t, "failed", resultOutbox(t, p, consumer, rejected).Status)
	notices := workEvents(t, p, "outbox_failed")
	require.Len(t, notices, 1)
	require.Equal(t, topic.OwnerID, notices[0].Recipient.TopicID)
	waitWorkEvent(t, p, notices[0].ID, "sent")
}
