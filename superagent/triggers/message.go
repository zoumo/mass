package triggers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type DWSEvent struct {
	Type           string            `json:"type"`
	EventID        string            `json:"event_id"`
	MessageID      string            `json:"message_id"`
	ConversationID string            `json:"conversation_id"`
	SenderID       string            `json:"sender_open_dingtalk_id"`
	Content        string            `json:"content"`
	EventTime      int64             `json:"event_time"`
	Quoted         json.RawMessage   `json:"quoted_message"`
	Forward        []json.RawMessage `json:"forward_messages"`
}
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

var (
	errSelfDirect = errors.New("self_direct_ignored")
	errUnmatched  = errors.New("message_rule_unmatched")
)

var dwsTextEscaper = strings.NewReplacer(
	"<dws-source>", "&lt;dws-source&gt;", "</dws-source>", "&lt;/dws-source&gt;",
	"<group-context>", "&lt;group-context&gt;", "</group-context>", "&lt;/group-context&gt;",
)

func normalize(line []byte, c Config, target subscription) (Submission, error) {
	var e DWSEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return Submission{}, errors.New("malformed_ndjson")
	}
	if e.Type != target.eventKey {
		return Submission{}, errors.New("unsupported_event_type")
	}
	if !validNativeID(e.MessageID) || !validNativeID(e.ConversationID) || !validNativeID(e.SenderID) {
		return Submission{}, errors.New("missing_native_identity")
	}
	var rules []matchRule
	selfSent := false
	switch {
	case target.group != nil:
		if e.ConversationID != target.group.ConversationID {
			return Submission{}, errors.New("conversation_not_allowed")
		}
		rules = target.group.Match
	case c.SelfChat.Enabled && e.ConversationID == c.SelfChat.ConversationID:
		if e.SenderID != c.SelfID {
			return Submission{}, errors.New("unexpected_self_chat_sender")
		}
		selfSent = true
		rules = c.SelfChat.Match
	case target.eventKey == directEvent:
		if e.SenderID == c.SelfID {
			return Submission{}, errSelfDirect
		}
		rules = c.Direct.Match
	default:
		return Submission{}, errors.New("conversation_not_allowed")
	}
	if strings.TrimSpace(e.Content) == "" {
		return Submission{}, errors.New("unsupported_or_empty_content")
	}
	if !matchesText(rules, e.Content) {
		return Submission{}, errUnmatched
	}
	m := Message{Source: c.Source, AccountID: c.AccountID, SourceMessageID: e.MessageID, ThreadRef: ThreadRef{c.Source, e.ConversationID}, Actor: e.SenderID, Text: e.Content, SourceEventID: e.EventID, CollectorInstance: c.InstanceID, Quoted: e.Quoted}
	if target.group != nil {
		m.Addressed = "group"
		if target.group.ReplyEnabled {
			m.Channel = &Channel{
				Type: "dws", ConversationID: e.ConversationID, MessageID: e.MessageID,
				ActorID: e.SenderID, Address: map[string]string{"groupOpenConversationId": e.ConversationID, "senderOpenDingtalkId": e.SenderID},
			}
			selfSent = e.SenderID == c.SelfID
		}
		if strings.TrimSpace(target.group.Prompt) != "" {
			m.Text = "<dws-source>\n" + dwsTextEscaper.Replace(e.Content) + "\n</dws-source>\n\n<group-context>\n" +
				"群聊配置补充上下文\n" + dwsTextEscaper.Replace(target.group.Prompt) + "\n</group-context>"
		}
	} else {
		m.Addressed = "direct"
		m.Channel = &Channel{
			Type: "dws", ConversationID: e.ConversationID, MessageID: e.MessageID,
			ActorID: e.SenderID, Address: map[string]string{"peerOpenDingtalkId": e.SenderID, "senderOpenDingtalkId": e.SenderID},
		}
	}
	if e.EventTime > 0 {
		m.At = time.UnixMilli(e.EventTime).UTC().Format(time.RFC3339Nano)
	}
	if len(e.Forward) > 0 {
		m.Meta = map[string]any{"forwardMessages": e.Forward}
	}
	return Submission{OperationID: "dws-message:" + e.MessageID, Message: m, SelfSent: selfSent}, nil
}
