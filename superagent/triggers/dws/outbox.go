package dws

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

const outboxPath = "/v1/channel/outbox"

type outboxReceipt struct {
	TaskID         string `json:"taskId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	ErrorCode      string `json:"errorCode,omitempty"`
	RetryAt        string `json:"retryAt,omitempty"`
	Uncertain      bool   `json:"uncertain,omitempty"`
}

type outboxState struct {
	Status  string         `json:"status"`
	Receipt *outboxReceipt `json:"receipt,omitempty"`
}

type outboxItem struct {
	outboxState
	ID        string             `json:"id"`
	Source    string             `json:"source"`
	AccountID string             `json:"accountId"`
	Text      string             `json:"text"`
	Content   outboxContent      `json:"content"`
	Channel   coreclient.Channel `json:"channel"`
}

type outboxContent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (item outboxItem) renderText() string {
	content := item.Content
	switch content.Type {
	case "":
		return item.Text
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
	OutboxID       string      `json:"outboxId"`
	Expected       outboxState `json:"expected"`
	Outcome        string      `json:"outcome"`
	TaskID         string      `json:"taskId,omitempty"`
	ErrorCode      string      `json:"errorCode,omitempty"`
	ConversationID string      `json:"conversationId,omitempty"`
	MessageID      string      `json:"messageId,omitempty"`
}

type egress struct {
	config   dwsConfig
	core     *coreclient.Client
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
	for cursor := ""; admit.Err() == nil; {
		raw, code, retry := e.core.Request(admit, http.MethodGet, outboxPath+"?limit=20&cursor="+url.QueryEscape(cursor), nil, 8<<20)
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
	peer, allowed := replyTarget(e.config, item.Channel)
	if !allowed {
		var taskID string
		if item.Receipt != nil {
			taskID = item.Receipt.TaskID
		}
		e.update(drain, item, channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_reply_target_not_allowed"}, taskID)
		return
	}
	if err := e.feedback.trackReply(item.Channel, item.ID); err != nil {
		e.log("feedback_state_failed", "track_reply")
	}
	if item.Status == "pending" && item.Receipt != nil && item.Receipt.TaskID != "" {
		result := queryDWS(admit, e.config, item.Receipt.TaskID)
		if result.Outcome != "" {
			e.update(drain, item, result, item.Receipt.TaskID)
		}
		return
	}
	if admit.Err() != nil {
		return
	}
	text := item.renderText()
	if strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) {
		e.update(drain, item, channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_reply_content_invalid"}, "")
		return
	}
	// Reuse the key until Core records a retryable result. A restart between a
	// send and its receipt then gives DWS a chance to deduplicate the request.
	var retryAt string
	if item.Receipt != nil {
		retryAt = item.Receipt.RetryAt
	}
	digest := sha256.Sum256([]byte(item.ID + "\x00" + retryAt))
	key := fmt.Sprintf("send_%x", digest[:16])
	result := sendDWS(drain, e.config, item.Channel, key, peer, text)
	if result.Outcome == "" && result.TaskID != "" {
		result.Outcome = "pending"
	}
	e.update(drain, item, result, result.TaskID)
}

func replyTarget(c dwsConfig, channel coreclient.Channel) (peer string, allowed bool) {
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

func (e egress) update(ctx context.Context, item outboxItem, result channelOutcome, taskID string) {
	if result.Outcome == "sent" && result.ConversationID != "" && result.ConversationID != item.Channel.ConversationID {
		result = channelOutcome{Outcome: "permanent_failure", ErrorCode: "dws_reply_destination_mismatch"}
	}
	req := outboxCommand{
		OutboxID: item.ID, Expected: item.outboxState, Outcome: result.Outcome, TaskID: taskID,
		ErrorCode: result.ErrorCode,
	}
	if result.Outcome == "sent" && strings.TrimSpace(result.ConversationID) != "" {
		req.ConversationID = result.ConversationID
		if strings.TrimSpace(result.MessageID) != "" {
			req.MessageID = result.MessageID
		}
	}
	delay := time.Duration(e.config.RetryMillis) * time.Millisecond
	for ctx.Err() == nil {
		raw, code, retry := e.core.Post(ctx, outboxPath+"/result", req)
		if code == "" {
			if validOutboxWrite(raw, req) {
				if req.Outcome == "sent" {
					e.feedback.notify()
				}
				return
			}
			code, retry = "core_response_unknown", true
		}
		e.log("outbox_result_unconfirmed", code)
		if !retry || !pause(ctx, delay) {
			return
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func validOutboxWrite(raw []byte, req outboxCommand) bool {
	var item outboxItem
	if json.Unmarshal(raw, &item) != nil || item.ID != req.OutboxID {
		return false
	}
	var receipt outboxReceipt
	if item.Receipt != nil {
		receipt = *item.Receipt
	}
	if receipt.TaskID != req.TaskID || receipt.ConversationID != req.ConversationID ||
		receipt.MessageID != req.MessageID || receipt.ErrorCode != req.ErrorCode {
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
