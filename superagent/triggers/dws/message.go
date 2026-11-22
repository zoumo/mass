package dws

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

type dwsEvent struct {
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

var (
	errSelfDirect = errors.New("self_direct_ignored")
	errUnmatched  = errors.New("message_rule_unmatched")
)

var dwsTextEscaper = strings.NewReplacer(
	"<dws-source>", "&lt;dws-source&gt;", "</dws-source>", "&lt;/dws-source&gt;",
	"<group-context>", "&lt;group-context&gt;", "</group-context>", "&lt;/group-context&gt;",
)

func normalize(line []byte, c dwsConfig, target subscription) (coreclient.Submission, error) {
	var e dwsEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return coreclient.Submission{}, errors.New("malformed_ndjson")
	}
	if e.Type != target.eventKey {
		return coreclient.Submission{}, errors.New("unsupported_event_type")
	}
	if !validNativeID(e.MessageID) || !validNativeID(e.ConversationID) || !validNativeID(e.SenderID) {
		return coreclient.Submission{}, errors.New("missing_native_identity")
	}
	var rules []matchRule
	selfSent := false
	switch {
	case target.group != nil:
		if e.ConversationID != target.group.ConversationID {
			return coreclient.Submission{}, errors.New("conversation_not_allowed")
		}
		rules = target.group.Match
	case c.SelfChat.Enabled && e.ConversationID == c.SelfChat.ConversationID:
		if e.SenderID != c.SelfID {
			return coreclient.Submission{}, errors.New("unexpected_self_chat_sender")
		}
		selfSent = true
		rules = c.SelfChat.Match
	case target.eventKey == directEvent:
		if e.SenderID == c.SelfID {
			return coreclient.Submission{}, errSelfDirect
		}
		rules = c.Direct.Match
	default:
		return coreclient.Submission{}, errors.New("conversation_not_allowed")
	}
	if strings.TrimSpace(e.Content) == "" {
		return coreclient.Submission{}, errors.New("unsupported_or_empty_content")
	}
	if !matchesText(rules, e.Content) {
		return coreclient.Submission{}, errUnmatched
	}
	m := coreclient.Message{Source: c.Source, AccountID: c.AccountID, SourceMessageID: e.MessageID, ThreadRef: coreclient.ThreadRef{Source: c.Source, ID: e.ConversationID}, Actor: e.SenderID, Text: e.Content, SourceEventID: e.EventID, CollectorInstance: c.InstanceID, Quoted: e.Quoted}
	if target.group != nil {
		m.Addressed = "group"
		if target.group.ReplyEnabled {
			m.Channel = &coreclient.Channel{
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
		m.Channel = &coreclient.Channel{
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
	return coreclient.Submission{OperationID: "dws-message:" + e.MessageID, Message: m, SelfSent: selfSent}, nil
}
