package coreclient

import "encoding/json"

type Message struct {
	Source            string          `json:"source"`
	AccountID         string          `json:"accountId"`
	SourceMessageID   string          `json:"sourceMessageId"`
	ThreadRef         ThreadRef       `json:"threadRef"`
	Actor             string          `json:"actor"`
	Text              string          `json:"text"`
	At                string          `json:"at,omitempty"`
	Addressed         string          `json:"addressed,omitempty"`
	Channel           *Channel        `json:"channel,omitempty"`
	Human             *bool           `json:"human,omitempty"`
	URL               string          `json:"url,omitempty"`
	Quoted            json.RawMessage `json:"quotedMessage,omitempty"`
	Meta              map[string]any  `json:"meta,omitempty"`
	SourceEventID     string          `json:"sourceEventId,omitempty"`
	CollectorInstance string          `json:"collectorInstance"`
}
type ThreadRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}
type Channel struct {
	Type           string            `json:"type"`
	ConversationID string            `json:"conversationId"`
	MessageID      string            `json:"messageId"`
	ActorID        string            `json:"actorId"`
	Address        map[string]string `json:"address"`
}
type Submission struct {
	OperationID string  `json:"operationId"`
	Message     Message `json:"message"`
	SelfSent    bool    `json:"selfSent,omitempty"`
}
