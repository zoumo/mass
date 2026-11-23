package dws

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func TestNormalizeNativeIdentity(t *testing.T) {
	c := dwsConfig{
		Source: "dws", AccountID: "account", InstanceID: "instance", SelfID: "self",
		SelfChat: selfChatConfig{Enabled: true, ConversationID: fixtureSelfConversation, Match: allTextRules()},
	}
	if err := validateRules("selfChat", c.SelfChat.Match, true); err != nil {
		t.Fatal(err)
	}
	target := c.subscriptions()[0]
	id := strings.Repeat("Native+/=:", 100)
	line := testMarshalMessage(t, map[string]any{"type": "user_im_message_receive_o2o", "message_id": id, "conversation_id": c.SelfChat.ConversationID, "sender": "display name", "sender_open_dingtalk_id": "self", "content": "normal text without prefix", "event_time": 1794761143211, "quoted_message": map[string]any{"conversation_id": "another", "message_id": "quoted", "content": "quoted text"}})
	s, err := normalize(line, c, target)
	if err != nil {
		t.Fatal(err)
	}
	if s.Message.SourceMessageID != id || s.Message.ThreadID != dwsThreadID(c.Source, c.SelfChat.ConversationID) ||
		len(s.Message.Content) != 1 || s.Message.Content[0].Text == nil || s.Message.Content[0].Text.Text != "normal text without prefix\n\nAuthor: self\n\nSource time: 2026-11-15T16:45:43.211Z" || !s.SelfSent || s.Quote != nil {
		t.Fatalf("wrong normalized message: %+v", s)
	}
	b := testMarshalMessage(t, s)
	if strings.Contains(string(b), `"human"`) || strings.Contains(string(b), `"url"`) {
		t.Fatal("invented optional facts")
	}
	c.InstanceID = "restart"
	second, err := normalize(line, c, target)
	if err != nil || second.OperationID != s.OperationID {
		t.Fatal("unstable identity")
	}
}

func TestNormalizeRejects(t *testing.T) {
	c := dwsConfig{Source: "dws", SelfID: "self", SelfChat: selfChatConfig{Enabled: true, ConversationID: "allowed"}}
	target := c.subscriptions()[0]
	base := map[string]any{"type": "user_im_message_receive_o2o", "message_id": "m", "conversation_id": "allowed", "sender_open_dingtalk_id": "self", "content": "text"}
	cases := []struct {
		key    string
		value  any
		reason string
	}{{"type", "user_im_message_receive_group", "unsupported_event_type"}, {"message_id", "", "missing_native_identity"}, {"conversation_id", "", "missing_native_identity"}, {"sender_open_dingtalk_id", "", "missing_native_identity"}, {"conversation_id", "other", "conversation_not_allowed"}, {"content", "", "unsupported_or_empty_content"}}
	for _, tc := range cases {
		t.Run(tc.key+tc.reason, func(t *testing.T) {
			v := maps.Clone(base)
			v[tc.key] = tc.value
			b := testMarshalMessage(t, v)
			_, err := normalize(b, c, target)
			if err == nil || err.Error() != tc.reason {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := normalize([]byte("not json"), c, target); err == nil {
		t.Fatal("accepted malformed JSON")
	}
}

func testMarshalMessage(t *testing.T, message any) []byte {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
