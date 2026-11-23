package coreclient

import (
	"encoding/json"

	"github.com/zoumo/mass/massd/pkg/events"
)

type TriggerMessage struct {
	SourceMessageID string                `json:"sourceMessageId"`
	ThreadKey       string                `json:"threadKey"`
	Content         []events.ContentBlock `json:"content"`
	Meta            json.RawMessage       `json:"_meta,omitempty"`
}

type TriggerSubmission struct {
	OperationID       string          `json:"operationId"`
	Message           TriggerMessage  `json:"message"`
	ReplyTarget       json.RawMessage `json:"replyTarget,omitempty"`
	InReplyToOutboxID string          `json:"inReplyToOutboxId,omitempty"`
}
