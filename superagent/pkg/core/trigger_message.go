package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

// TriggerMessage is the channel input shared by all Trigger adapters.
type TriggerMessage struct {
	SourceMessageID string                `json:"sourceMessageId"`
	ThreadID        string                `json:"threadId"`
	Content         []events.ContentBlock `json:"content"`
	Meta            json.RawMessage       `json:"_meta,omitempty"`
}

type SubmitTriggerMessageRequest struct {
	OperationID       string          `json:"operationId"`
	Message           TriggerMessage  `json:"message"`
	ReplyTarget       json.RawMessage `json:"replyTarget,omitempty"`
	InReplyToOutboxID string          `json:"inReplyToOutboxId,omitempty"`
}

func validTriggerThreadID(id string) bool {
	return id != "" && len(id) <= 2048 && strings.TrimSpace(id) == id && utf8.ValidString(id) &&
		!strings.ContainsFunc(id, unicode.IsControl)
}

func (r SubmitTriggerMessageRequest) prepare(source Source) (Message, []byte, error) {
	m := r.Message
	if strings.TrimSpace(m.SourceMessageID) == "" || !validTriggerThreadID(m.ThreadID) {
		return Message{}, nil, problem("invalid_argument", "sourceMessageId and a valid threadId are required")
	}
	if err := (runtime.Input{Content: m.Content}).Validate(); err != nil {
		return Message{}, nil, problem("invalid_argument", err.Error())
	}
	if meta := bytes.TrimSpace(m.Meta); len(meta) != 0 && (meta[0] != '{' || !json.Valid(meta)) {
		return Message{}, nil, problem("invalid_argument", "_meta must be a JSON object")
	}
	target, err := canonicalReplyTarget(r.ReplyTarget)
	if err != nil {
		return Message{}, nil, err
	}
	if r.InReplyToOutboxID != "" && !validTriggerThreadID(r.InReplyToOutboxID) {
		return Message{}, nil, problem("invalid_argument", "inReplyToOutboxId must be a valid identifier")
	}
	message := Message{
		Source:            source.Source,
		AccountID:         source.AccountID,
		SourceMessageID:   m.SourceMessageID,
		ThreadID:          m.ThreadID,
		Content:           m.Content,
		TriggerMeta:       m.Meta,
		ReplyTarget:       target,
		InReplyToOutboxID: r.InReplyToOutboxID,
	}
	m.Meta = nil
	canonical, err := json.Marshal(struct {
		TriggerMessage
		ReplyTarget       json.RawMessage `json:"replyTarget,omitempty"`
		InReplyToOutboxID string          `json:"inReplyToOutboxId,omitempty"`
	}{m, target, r.InReplyToOutboxID})
	if err != nil {
		return Message{}, nil, problem("invalid_argument", "message cannot be encoded")
	}
	return message, canonical, nil
}

const maxReplyTargetBytes = 16 << 10

func canonicalReplyTarget(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxReplyTargetBytes {
		return nil, problem("invalid_argument", "replyTarget exceeds 16 KiB")
	}
	var target map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&target); err != nil || target == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, problem("invalid_argument", "replyTarget must be a JSON object")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return nil, problem("invalid_argument", "replyTarget cannot be encoded")
	}
	return canonical, nil
}

func (s *Store) SubmitTriggerMessage(ctx context.Context, source Source, request SubmitTriggerMessageRequest) (Receipt, error) {
	if strings.TrimSpace(request.OperationID) == "" {
		return Receipt{}, problem("invalid_argument", "operationId is required")
	}
	message, canonical, err := request.prepare(source)
	if err != nil {
		return Receipt{}, err
	}
	if strings.HasPrefix(message.ThreadID, scheduledTaskThreadPrefix) {
		return Receipt{}, problem("forbidden", "threadId uses the Core scheduled task namespace")
	}
	return s.submitMessage(ctx, source, message, canonical)
}

func (s *Store) submitChannelMessage(ctx context.Context, source Source, body io.Reader) (Receipt, error) {
	var raw json.RawMessage
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&raw); err != nil || !utf8.Valid(raw) {
		return Receipt{}, problem("invalid_argument", "invalid submitMessage JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Receipt{}, problem("invalid_argument", "exactly one JSON value is required")
	}
	var request SubmitTriggerMessageRequest
	if err := decodeStrict(raw, &request); err != nil {
		return Receipt{}, err
	}
	return s.SubmitTriggerMessage(ctx, source, request)
}
