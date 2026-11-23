package dws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

const outboxPath = "/v1/core/outbox-messages"

func outboxDeliveryPath(id string) string {
	return outboxPath + "/" + url.PathEscape(id) + "?view=delivery"
}

type outboxReceipt struct {
	TaskID    string `json:"taskId,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	RetryAt   string `json:"retryAt,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

type outboxState struct {
	Status  string         `json:"status"`
	Receipt *outboxReceipt `json:"receipt,omitempty"`
}

type outboxItem struct {
	outboxState
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	AccountID   string          `json:"accountId"`
	Content     outboxContent   `json:"content"`
	ReplyTarget json.RawMessage `json:"replyTarget"`
}

type outboxContent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (item outboxItem) renderText() string {
	content := item.Content
	switch content.Type {
	case "markdown":
		var data struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(content.Data, &data) == nil {
			return data.Text
		}
	case "options":
		var data struct {
			Prompt  string `json:"prompt"`
			Options []struct {
				Label string `json:"label"`
			} `json:"options"`
		}
		if json.Unmarshal(content.Data, &data) != nil || strings.TrimSpace(data.Prompt) == "" || len(data.Options) < 2 {
			return ""
		}
		var text strings.Builder
		text.WriteString(data.Prompt)
		text.WriteByte('\n')
		for i, option := range data.Options {
			fmt.Fprintf(&text, "\n%d. %s", i+1, option.Label)
		}
		return text.String()
	}
	return ""
}

type outboxPage struct {
	Items      []outboxItem `json:"items"`
	NextCursor string       `json:"nextCursor"`
}

type outboxCommand struct {
	Expected  outboxState `json:"expected"`
	Outcome   string      `json:"outcome"`
	TaskID    string      `json:"taskId,omitempty"`
	ErrorCode string      `json:"errorCode,omitempty"`
}

type egress struct {
	config   dwsConfig
	core     *coreclient.Client
	evidence *sendEvidenceStore
	log      func(string, string)
	feedback *feedbackWorker
}

func (e egress) run(admit, drain context.Context) {
	for admit.Err() == nil && e.scan(admit, drain) {
		if !pause(admit, time.Second) {
			return
		}
	}
}

func (e egress) scan(admit, drain context.Context) bool {
	e.recover(admit, drain)
	for cursor := ""; admit.Err() == nil; {
		raw, code, retry := e.core.Request(admit, http.MethodGet, outboxPath+"?view=delivery&limit=20&cursor="+url.QueryEscape(cursor), nil, 8<<20)
		if code != "" {
			if admit.Err() == nil {
				e.log("outbox_poll_failed", code)
			}
			return retry && admit.Err() == nil
		}
		var page outboxPage
		if json.Unmarshal(raw, &page) != nil || page.Items == nil {
			e.log("outbox_poll_failed", "core_response_unknown")
			return true
		}
		for _, item := range page.Items {
			if admit.Err() != nil {
				return false
			}
			if item.ID != "" && item.Source == e.config.Source && item.AccountID == e.config.AccountID &&
				(item.Status == "pending" || item.Status == "failed") {
				e.handle(admit, drain, item)
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return true
		}
		cursor = page.NextCursor
	}
	return false
}

func (e egress) handle(admit, drain context.Context, item outboxItem) {
	var channel nativeReplyTarget
	if json.Unmarshal(item.ReplyTarget, &channel) != nil {
		channel = nativeReplyTarget{}
	}
	var taskID, retryAt string
	if item.Receipt != nil {
		taskID, retryAt = item.Receipt.TaskID, item.Receipt.RetryAt
	}
	if item.Status == "failed" {
		// A previous task may have succeeded after Core marked it uncertain.
		// The retry feed can remain available during individual read failures.
		if attempt, found := e.evidence.successfulAttempt(item.ID); found {
			e.settle(drain, item, attempt)
			return
		}
		// Task IDs on failed receipts describe the preceding attempt.
		taskID = ""
		if retryAt == "" {
			return
		}
	}
	peer, allowed := replyTarget(e.config, channel)
	if !allowed {
		e.update(drain, item, channelOutcome{Outcome: "permanent_failure", TaskID: taskID, ErrorCode: "dws_reply_target_not_allowed"})
		return
	}
	if err := e.feedback.trackReply(channel, item.ID); err != nil {
		e.log("feedback_state_failed", "track_reply")
	}
	key := sendEvidence{OutboxID: item.ID, RetryAt: retryAt, ConversationID: channel.ConversationID}
	attempt, found := e.evidence.get(key)
	if taskID != "" && (!found || attempt.Result.TaskID != taskID) {
		// A retry's Core deadline is cleared once the native task is recorded.
		attempt, found = e.evidence.byTask(item.ID, taskID)
		if !found {
			e.log("outbox_evidence_failed", "missing_task_attempt")
			return
		}
	}
	if found && attempt.Result.Outcome != "" && attempt.Result.Outcome != "pending" {
		e.settle(drain, item, attempt)
		return
	}
	if !found {
		key.StartedAt = time.Now().UTC()
		attempt = key
	}
	if attempt.Result.TaskID != "" {
		result := queryDWS(admit, e.config, attempt.Result.TaskID)
		if result.Outcome == "" || result.Outcome == "pending" {
			if taskID == "" {
				e.update(drain, item, attempt.Result)
			}
			return
		}
		result.TaskID = attempt.Result.TaskID
		e.recordAndSettle(drain, item, attempt, result)
		return
	}
	if admit.Err() != nil {
		return
	}
	text := item.renderText()
	if strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) {
		e.update(drain, item, channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_reply_content_invalid"})
		return
	}
	// The intent becomes visible to ingress before calling DWS. An interrupted
	// call runs again with the same key unless a task or result was persisted.
	if !found && !e.record(attempt) {
		return
	}
	result := sendDWS(drain, e.config, channel, attempt.sendKey(), peer, text)
	if result.Outcome == "" && result.TaskID != "" {
		result.Outcome = "pending"
	}
	e.recordAndSettle(drain, item, attempt, result)
}

func (e egress) record(attempt sendEvidence) bool {
	if err := e.evidence.put(attempt); err != nil {
		e.log("outbox_evidence_failed", "store_attempt")
		return false
	}
	return true
}

func (e egress) recordAndSettle(ctx context.Context, item outboxItem, attempt sendEvidence, result channelOutcome) {
	if result.Outcome == "sent" && result.ConversationID != "" && result.ConversationID != attempt.ConversationID {
		result.Outcome = "permanent_failure"
		result.ErrorCode = "dws_reply_destination_mismatch"
	}
	attempt.Result = result
	for !e.record(attempt) {
		if !pause(ctx, time.Duration(e.config.RetryMillis)*time.Millisecond) {
			return
		}
	}
	e.settle(ctx, item, attempt)
}

func (e egress) settle(ctx context.Context, item outboxItem, attempt sendEvidence) {
	confirmed := item.Status == "sent" && attempt.Result.Outcome == "sent"
	if !confirmed {
		confirmed = e.update(ctx, item, attempt.Result)
	}
	if confirmed && attempt.Result.Outcome == "sent" && !attempt.Settled {
		attempt.Settled = true
		if e.record(attempt) {
			e.feedback.notify()
		}
	}
}

// The Outbox feed omits settled items. Recover uncertain tasks and confirm a
// saved success even if Core committed before the adapter saw its receipt.
func (e egress) recover(admit, drain context.Context) {
	for _, attempt := range e.evidence.recoverable() {
		if admit.Err() != nil {
			return
		}
		if attempt.Result.Outcome == "unknown" || (attempt.Settled && attempt.equivocal()) {
			late := queryDWS(admit, e.config, attempt.Result.TaskID)
			if late.Outcome != "sent" {
				continue
			}
			late.TaskID = attempt.Result.TaskID
			if late.ConversationID != "" && late.ConversationID != attempt.ConversationID {
				continue
			}
			attempt.Result = late
			if !e.record(attempt) || attempt.Settled {
				continue
			}
		}
		raw, code, _ := e.core.Request(admit, http.MethodGet, outboxDeliveryPath(attempt.OutboxID), nil, 65536)
		var item outboxItem
		if code != "" || json.Unmarshal(raw, &item) != nil || item.ID != attempt.OutboxID {
			continue
		}
		if item.Status == "pending" || item.Status == "sent" || (item.Status == "failed" && item.Receipt != nil && item.Receipt.RetryAt != "") {
			e.settle(drain, item, attempt)
		}
	}
}

func replyTarget(c dwsConfig, channel nativeReplyTarget) (peer string, allowed bool) {
	if channel.Type != "dws" || !validNativeID(channel.ConversationID) || !validNativeID(channel.MessageID) ||
		!validNativeID(channel.ActorID) || len(channel.Address) != 2 ||
		channel.Address["senderOpenDingtalkId"] != channel.ActorID {
		return "", false
	}
	for _, group := range c.Groups {
		if channel.ConversationID == group.ConversationID {
			return "", group.ReplyEnabled && channel.Address["groupOpenConversationId"] == group.ConversationID
		}
	}
	peer = channel.Address["peerOpenDingtalkId"]
	if peer != channel.ActorID {
		return "", false
	}
	if c.SelfChat.Enabled && channel.ConversationID == c.SelfChat.ConversationID {
		return peer, peer == c.SelfID
	}
	return peer, c.Direct.Enabled && peer != c.SelfID
}

func (e egress) update(ctx context.Context, item outboxItem, result channelOutcome) bool {
	req := outboxCommand{
		Expected: item.outboxState, Outcome: result.Outcome, TaskID: result.TaskID, ErrorCode: result.ErrorCode,
	}
	delay := time.Duration(e.config.RetryMillis) * time.Millisecond
	for ctx.Err() == nil {
		raw, code, retry := e.core.Patch(ctx, outboxPath+"/"+url.PathEscape(item.ID), req)
		if code == "" {
			if validOutboxWrite(raw, item.ID, req) {
				return true
			}
			code, retry = "core_response_unknown", true
		}
		e.log("outbox_result_unconfirmed", code)
		if !retry || !pause(ctx, delay) {
			return false
		}
		delay = min(delay*2, 5*time.Second)
	}
	return false
}

func validOutboxWrite(raw []byte, id string, req outboxCommand) bool {
	var item outboxItem
	if json.Unmarshal(raw, &item) != nil || item.ID != id {
		return false
	}
	var receipt outboxReceipt
	if item.Receipt != nil {
		receipt = *item.Receipt
	}
	if receipt.TaskID != req.TaskID || receipt.ErrorCode != req.ErrorCode {
		return false
	}
	uncertain := req.Outcome == "unknown" || (req.Expected.Receipt != nil && req.Expected.Receipt.Uncertain)
	if receipt.Uncertain != uncertain {
		return false
	}
	switch req.Outcome {
	case "pending", "sent":
		return item.Status == req.Outcome && receipt.RetryAt == ""
	case "permanent_failure":
		return item.Status == "failed" && receipt.RetryAt == ""
	case "retryable_failure", "unknown":
		_, err := time.Parse(time.RFC3339Nano, receipt.RetryAt)
		return item.Status == "failed" && err == nil
	default:
		return false
	}
}

func pause(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
