package dws

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/internal/triggercontent"
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

type nativeReplyTarget struct {
	Type           string            `json:"type"`
	ConversationID string            `json:"conversationId"`
	MessageID      string            `json:"messageId"`
	ActorID        string            `json:"actorId"`
	Address        map[string]string `json:"address"`
}

type nativeMessageRef struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
}

type queuedSubmission struct {
	coreclient.TriggerSubmission
	ConversationID string            `json:"conversationId"`
	SelfSent       bool              `json:"selfSent,omitempty"`
	Quote          *nativeMessageRef `json:"quote,omitempty"`
	Prepared       bool              `json:"prepared,omitempty"`
	ReleaseReason  string            `json:"releaseReason,omitempty"`
}

func dwsThreadKey(source, conversationID string) string {
	escape := func(value string) string { return strings.ReplaceAll(url.PathEscape(value), ":", "%3A") }
	return escape(source) + ":" + escape(conversationID)
}

func quoteRef(raw json.RawMessage, conversationID string) *nativeMessageRef {
	var ref nativeMessageRef
	if json.Unmarshal(raw, &ref) != nil || !validNativeID(ref.MessageID) {
		return nil
	}
	if ref.ConversationID == "" {
		ref.ConversationID = conversationID
	}
	if ref.ConversationID != conversationID {
		return nil
	}
	return &ref
}

func normalize(line []byte, c dwsConfig, target subscription) (queuedSubmission, error) {
	var e dwsEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return queuedSubmission{}, errors.New("malformed_ndjson")
	}
	if e.Type != target.eventKey {
		return queuedSubmission{}, errors.New("unsupported_event_type")
	}
	if !validNativeID(e.MessageID) || !validNativeID(e.ConversationID) || !validNativeID(e.SenderID) {
		return queuedSubmission{}, errors.New("missing_native_identity")
	}
	var rules []matchRule
	selfSent := false
	switch {
	case target.group != nil:
		if e.ConversationID != target.group.ConversationID {
			return queuedSubmission{}, errors.New("conversation_not_allowed")
		}
		rules = target.group.Match
	case c.SelfChat.Enabled && e.ConversationID == c.SelfChat.ConversationID:
		if e.SenderID != c.SelfID {
			return queuedSubmission{}, errors.New("unexpected_self_chat_sender")
		}
		selfSent = true
		rules = c.SelfChat.Match
	case target.eventKey == directEvent:
		if e.SenderID == c.SelfID {
			return queuedSubmission{}, errSelfDirect
		}
		rules = c.Direct.Match
	default:
		return queuedSubmission{}, errors.New("conversation_not_allowed")
	}
	if strings.TrimSpace(e.Content) == "" {
		return queuedSubmission{}, errors.New("unsupported_or_empty_content")
	}
	if !matchesText(rules, e.Content) {
		return queuedSubmission{}, errUnmatched
	}
	text := e.Content
	var reply *nativeReplyTarget
	chatType := "direct"
	if target.group != nil {
		chatType = "group"
		if target.group.ReplyEnabled {
			selfSent = e.SenderID == c.SelfID
			reply = &nativeReplyTarget{
				Type: "dws", ConversationID: e.ConversationID, MessageID: e.MessageID,
				ActorID: e.SenderID, Address: map[string]string{"groupOpenConversationId": e.ConversationID, "senderOpenDingtalkId": e.SenderID},
			}
		}
		if strings.TrimSpace(target.group.Prompt) != "" {
			text = "<dws-source>\n" + dwsTextEscaper.Replace(e.Content) + "\n</dws-source>\n\n<group-context>\n" +
				"群聊配置补充上下文\n" + dwsTextEscaper.Replace(target.group.Prompt) + "\n</group-context>"
		}
	} else {
		reply = &nativeReplyTarget{
			Type: "dws", ConversationID: e.ConversationID, MessageID: e.MessageID,
			ActorID: e.SenderID, Address: map[string]string{"peerOpenDingtalkId": e.SenderID, "senderOpenDingtalkId": e.SenderID},
		}
	}
	var at time.Time
	if e.EventTime > 0 {
		at = time.UnixMilli(e.EventTime)
	}
	meta := map[string]any{"chatType": chatType, "senderOpenDingtalkId": e.SenderID, "eventId": e.EventID}
	if len(e.Forward) > 0 {
		meta["forwardMessages"] = e.Forward
	}
	if len(e.Quoted) > 0 {
		meta["quotedMessage"] = e.Quoted
	}
	return newQueuedSubmission(coreclient.TriggerMessage{
		SourceMessageID: e.MessageID, ThreadKey: dwsThreadKey(c.Source, e.ConversationID),
		Content: triggercontent.SourceText(text, e.SenderID, "", at),
	}, reply, meta, "dws-message:"+e.MessageID, e.ConversationID, selfSent, e.Quoted)
}

func newQueuedSubmission(message coreclient.TriggerMessage, reply *nativeReplyTarget, meta map[string]any, operationID, conversationID string, selfSent bool, quoted json.RawMessage) (queuedSubmission, error) {
	ret := queuedSubmission{
		TriggerSubmission: coreclient.TriggerSubmission{OperationID: operationID, Message: message},
		ConversationID:    conversationID, SelfSent: selfSent, Quote: quoteRef(quoted, conversationID),
	}
	if reply != nil {
		raw, err := json.Marshal(reply)
		if err != nil {
			return queuedSubmission{}, err
		}
		ret.ReplyTarget = raw
	}
	if len(meta) != 0 {
		raw, err := json.Marshal(meta)
		if err != nil {
			return queuedSubmission{}, err
		}
		ret.Message.Meta = raw
	}
	return ret, nil
}

func decodeQueuedSubmission(raw []byte) (queuedSubmission, error) {
	var current queuedSubmission
	if err := json.Unmarshal(raw, &current); err != nil || current.Message.ThreadKey != "" {
		return current, err
	}
	var legacy coreclient.Submission
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return current, err
	}
	m := legacy.Message
	if legacy.OperationID == "" || m.ThreadRef.ID == "" || m.ThreadRef.Source == "" || m.SourceMessageID == "" {
		return current, errors.New("invalid legacy DWS queue record")
	}
	content, err := triggercontent.FromLegacyText(m.Text, m.Actor, m.At, m.URL)
	if err != nil {
		return current, err
	}
	meta := m.Meta
	if meta == nil {
		meta = make(map[string]any)
	}
	meta["chatType"] = m.Addressed
	meta["senderOpenDingtalkId"] = m.Actor
	meta["eventId"] = m.SourceEventID
	if len(m.Quoted) > 0 {
		meta["quotedMessage"] = m.Quoted
	}
	return newQueuedSubmission(coreclient.TriggerMessage{
		SourceMessageID: m.SourceMessageID, ThreadKey: dwsThreadKey(m.ThreadRef.Source, m.ThreadRef.ID), Content: content,
	}, legacyReplyTarget(m.Channel), meta, legacy.OperationID, m.ThreadRef.ID, legacy.SelfSent, m.Quoted)
}

func (s queuedSubmission) nativeReply() *nativeReplyTarget {
	var target nativeReplyTarget
	if json.Unmarshal(s.ReplyTarget, &target) != nil {
		return nil
	}
	return &target
}

func legacyReplyTarget(ch *coreclient.Channel) *nativeReplyTarget {
	if ch == nil {
		return nil
	}
	return &nativeReplyTarget{Type: ch.Type, ConversationID: ch.ConversationID, MessageID: ch.MessageID, ActorID: ch.ActorID, Address: ch.Address}
}
