// Package core owns superagent business persistence and ingress.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
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

type ThreadRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// ThreadKey uses an already canonical PR URL or escapes both native Thread ref components.
func ThreadKey(r ThreadRef) string {
	if r.Source == pullRequestSource {
		return r.ID
	}
	return strings.ReplaceAll(url.PathEscape(r.Source), ":", "%3A") + ":" + strings.ReplaceAll(url.PathEscape(r.ID), ":", "%3A")
}

func ParseThreadKey(key string) (ThreadRef, error) {
	// Generic keys always escape the slash, so absolute URLs occupy a separate namespace.
	if scheme, _, ok := strings.Cut(key, "://"); ok && (strings.EqualFold(scheme, "https") || strings.EqualFold(scheme, "http")) {
		canonical, err := canonicalPullRequestURL(key)
		if err != nil || canonical != key {
			return ThreadRef{}, errors.New("noncanonical PR thread key")
		}
		return ThreadRef{Source: pullRequestSource, ID: key}, nil
	}
	a, b, ok := strings.Cut(key, ":")
	if !ok {
		return ThreadRef{}, errors.New("invalid thread key")
	}
	source, err := url.PathUnescape(a)
	if err != nil {
		return ThreadRef{}, err
	}
	id, err := url.PathUnescape(b)
	if err != nil {
		return ThreadRef{}, err
	}
	r := ThreadRef{source, id}
	if source == "" || source == pullRequestSource || id == "" || ThreadKey(r) != key {
		return ThreadRef{}, errors.New("noncanonical thread key")
	}
	return r, nil
}

type Channel struct {
	Type           string         `json:"type"`
	ConversationID string         `json:"conversationId"`
	MessageID      string         `json:"messageId"`
	ActorID        string         `json:"actorId,omitempty"`
	Address        map[string]any `json:"address,omitempty"`
}
type MessageInput struct {
	Source            string          `json:"source"`
	AccountID         string          `json:"accountId"`
	SourceMessageID   string          `json:"sourceMessageId"`
	ThreadRef         ThreadRef       `json:"threadRef,omitzero"`
	Actor             string          `json:"actor,omitempty"`
	Text              string          `json:"text,omitempty"`
	At                string          `json:"at,omitempty"`
	Human             *bool           `json:"human,omitempty"`
	Addressed         string          `json:"addressed,omitempty"`
	Channel           *Channel        `json:"channel,omitempty"`
	URL               string          `json:"url,omitempty"`
	Meta              map[string]any  `json:"meta,omitempty"`
	QuotedMessage     json.RawMessage `json:"quotedMessage,omitempty"`
	SourceEventID     string          `json:"sourceEventId,omitempty"`
	CollectedAt       string          `json:"collectedAt,omitempty"`
	CollectorInstance string          `json:"collectorInstance,omitempty"`
}
type SubmitRequest struct {
	OperationID string       `json:"operationId"`
	Message     MessageInput `json:"message"`
	SelfSent    bool         `json:"selfSent,omitempty"`
}
type Message struct {
	MessageInput
	Content     []events.ContentBlock `json:"content,omitempty"`
	TriggerMeta json.RawMessage       `json:"_meta,omitempty"`
	ID          string                `json:"id"`
	ThreadKey   string                `json:"threadKey"`
	ReceivedAt  string                `json:"receivedAt"`
	Acceptance  string                `json:"acceptance"`
	EventID     string                `json:"eventId,omitempty"`
	Echo        *MessageEcho          `json:"echo,omitempty"`
}

var _ AgentPromptView = Message{}

func (m Message) MarshalAgentPromptJSON() ([]byte, error) {
	if len(m.Content) != 0 {
		return json.Marshal(map[string]any{"id": m.ID, "source": m.Source, "threadKey": m.ThreadKey, "content": m.Content})
	}
	view := map[string]string{"id": m.ID, "text": m.Text, "actor": m.Actor}
	if m.At != "" {
		view["at"] = m.At
	}
	if m.URL != "" {
		view["url"] = m.URL
	}
	return json.Marshal(view)
}

type Thread struct {
	Key           string    `json:"key"`
	Ref           ThreadRef `json:"ref"`
	CreatedAt     string    `json:"createdAt"`
	LastMessageAt string    `json:"lastMessageAt,omitempty"`
	Label         string    `json:"label,omitempty"`
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

func (m *MessageInput) validate(source Source) error {
	isPR := m.ThreadRef.Source == pullRequestSource
	if m.Source != source.Source || m.AccountID != source.AccountID || (!isPR && m.ThreadRef.Source != source.Source) {
		return problem("forbidden", "message source/account must match authenticated source")
	}
	if strings.TrimSpace(m.SourceMessageID) == "" || strings.TrimSpace(m.ThreadRef.ID) == "" || strings.TrimSpace(m.Actor) == "" || strings.TrimSpace(m.Text) == "" {
		return problem("invalid_argument", "sourceMessageId, threadRef.id, actor and text are required; native identity cannot be synthesized")
	}
	if m.Channel != nil {
		if m.Channel.Type == "" {
			return problem("invalid_argument", "channel type is required when a reply address is provided")
		}
		if m.Channel.Type == "dws" {
			if m.Channel.ConversationID != m.ThreadRef.ID || m.Channel.MessageID != m.SourceMessageID {
				return problem("invalid_argument", "DWS channel requires matching native conversationId/messageId")
			}
			if m.Channel.ActorID != "" && m.Channel.ActorID != m.Actor {
				return problem("invalid_argument", "DWS channel actorId must match actor")
			}
		}
	}
	if m.Source == "dws" || m.Channel != nil && m.Channel.Type == "dws" {
		switch m.Addressed {
		case "direct":
		case "group":
			if m.Channel != nil && (m.Channel.Type != "dws" || m.Channel.ActorID != m.Actor ||
				len(m.Channel.Address) != 2 || m.Channel.Address["groupOpenConversationId"] != m.ThreadRef.ID ||
				m.Channel.Address["senderOpenDingtalkId"] != m.Actor) {
				return problem("invalid_argument", "DWS group reply address requires matching native conversation, message and sender IDs")
			}
		default:
			return problem("invalid_argument", "DWS requires addressed=direct or addressed=group")
		}
	}
	if isPR {
		if m.Source == "dws" || m.Channel != nil && m.Channel.Type == "dws" {
			return problem("invalid_argument", "PR Threads cannot carry a DWS source or reply address")
		}
		canonical, err := canonicalPullRequestURL(m.ThreadRef.ID)
		if err != nil {
			return err
		}
		m.ThreadRef.ID = canonical
	}
	if m.At != "" {
		t, err := time.Parse(time.RFC3339Nano, m.At)
		if err != nil {
			return problem("invalid_argument", "at must be an absolute RFC3339 time")
		}
		m.At = t.UTC().Format(time.RFC3339Nano)
	}
	if m.URL != "" {
		u, err := url.Parse(m.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return problem("invalid_argument", "url must be an absolute HTTP(S) URL without credentials")
		}
	}
	if len(m.QuotedMessage) > 0 {
		var v any
		if err := json.Unmarshal(m.QuotedMessage, &v); err != nil {
			return problem("invalid_argument", "quotedMessage must be JSON")
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return problem("invalid_argument", "quotedMessage cannot be encoded")
		}
		m.QuotedMessage = encoded
	}
	return nil
}

func (m *MessageInput) canonical() ([]byte, error) {
	canonical := *m
	canonical.CollectedAt = ""
	canonical.CollectorInstance = ""
	canonical.SourceEventID = ""
	return json.Marshal(canonical)
}
