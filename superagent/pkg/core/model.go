// Package core owns superagent business persistence and ingress.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
)

type Error struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	Retryable       bool   `json:"retryable"`
	Resource        string `json:"resource,omitempty"`
	CurrentRevision int    `json:"currentRevision,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func problem(code, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: code == "unavailable"}
}

func storageError() *Error {
	return problem("unavailable", "storage operation failed; acceptance is unconfirmed; check the current resource before retrying")
}

func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

const utcTimestamp = "2006-01-02T15:04:05.000000000Z"

func now() string { return time.Now().UTC().Format(utcTimestamp) }

type Message struct {
	Source            string                `json:"source"`
	AccountID         string                `json:"accountId"`
	SourceMessageID   string                `json:"sourceMessageId"`
	Content           []events.ContentBlock `json:"content"`
	TriggerMeta       json.RawMessage       `json:"_meta,omitempty"`
	ReplyTarget       json.RawMessage       `json:"replyTarget,omitempty"`
	InReplyToOutboxID string                `json:"inReplyToOutboxId,omitempty"`
	ID                string                `json:"id"`
	ThreadKey         string                `json:"threadKey"`
	ReceivedAt        string                `json:"receivedAt"`
	Acceptance        string                `json:"acceptance"`
	EventID           string                `json:"eventId,omitempty"`
	// Historical diagnostics retained by the schema 25 migration.
	LegacyEcho       json.RawMessage `json:"echo,omitempty"`
	LegacyEchoStatus string          `json:"legacyEchoStatus,omitempty"`
}

var _ AgentPromptView = Message{}

func (m Message) MarshalAgentPromptJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": m.ID, "source": m.Source, "threadKey": m.ThreadKey, "content": m.Content})
}

type Thread struct {
	Key           string `json:"key"`
	CreatedAt     string `json:"createdAt"`
	LastMessageAt string `json:"lastMessageAt,omitempty"`
	Label         string `json:"label,omitempty"`
}
type Receipt struct {
	Status     string `json:"status"`
	Duplicate  bool   `json:"duplicate"`
	MessageID  string `json:"messageId"`
	ThreadKey  string `json:"threadKey"`
	AcceptedAt string `json:"acceptedAt"`
	EventID    string `json:"eventId,omitempty"`
	EchoStatus string `json:"echoStatus,omitempty"`
}
type Page struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}
