package dws

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

func (f *egressFixture) feedbackConfig(c *dwsConfig) {
	c.Feedback = &feedbackConfig{
		StatePath: filepath.Join(f.Dir, "feedback.json"),
		Thinking:  textEmotion{ID: "thinking-id", BackgroundID: "thinking-bg", Name: "thinking", Text: "thinking"},
		Done:      textEmotion{ID: "done-id", BackgroundID: "done-bg", Name: "done", Text: "done"},
	}
}

func (f *egressFixture) feedbackPhase(conversation, message, expected string) {
	f.t.Helper()
	key := feedbackKey(coreclient.Channel{ConversationID: conversation, MessageID: message})
	triggertest.Wait(f.t, message+" feedback phase "+expected, func() bool {
		raw, err := os.ReadFile(filepath.Join(f.Dir, "feedback.json"))
		var state feedbackState
		return err == nil && json.Unmarshal(raw, &state) == nil && state.Records[key].Phase == expected
	})
}

func (f *egressFixture) requireEmotion(message, id string) {
	f.t.Helper()
	path := filepath.Join(f.Dir, "reaction-"+message+".json")
	raw, err := os.ReadFile(path)
	if id == "" && os.IsNotExist(err) {
		return
	}
	var reactions []textEmotion
	if err != nil || json.Unmarshal(raw, &reactions) != nil || len(reactions) != 1 || reactions[0].ID != id {
		f.t.Fatalf("message %s reaction %q: %s (%v)", message, id, raw, err)
	}
}

func TestDWSFeedbackRecoversNativeQuoteAndLostReactionReceipts(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false)
	f.feed(1, "self-question", "self", "request")
	f.feed(2, "peer-question", "peer", "request")
	first := f.trigger(f.Endpoint, f.feedbackConfig)
	first.Endpoint(t, "statusEndpoint")
	f.feedbackPhase(fixtureSelfConversation, "self-question", feedbackThinking)
	f.feedbackPhase(egressConversation, "peer-question", feedbackThinking)
	f.requireEmotion("self-question", "thinking-id")
	f.requireEmotion("peer-question", "thinking-id")

	selfReply := f.reply("self-question", "answer")
	task := f.waitTask(selfReply)
	f.requireEmotion("self-question", "thinking-id")
	killAdapter(t, first)
	f.settle(task, fixtureSelfConversation, "self-answer")
	restarted := f.trigger(f.Endpoint, f.feedbackConfig)
	restarted.Endpoint(t, "statusEndpoint")
	f.feedbackPhase(fixtureSelfConversation, "self-question", feedbackDone)
	f.requireEmotion("self-question", "done-id")
	f.requireEmotion("peer-question", "thinking-id")
	if calls := f.calls(false); len(calls) != 1 ||
		!strings.Contains(strings.Join(calls[0], " "), "--group "+fixtureSelfConversation+" --message-id self-question --ref-sender self") {
		t.Fatalf("expected exactly one native self quote after recovery: %v", calls)
	}

	triggertest.Write(t, filepath.Join(f.Dir, "send-result.json"), []byte(`{"success":false,"error":{"reason":"permission_denied"}}`))
	peerReply := f.reply("peer-question", "peer reply")
	triggertest.Wait(t, "peer reply rejected", func() bool { return f.outbox(peerReply).Status == "failed" })
	f.requireEmotion("peer-question", "thinking-id")
	if calls := f.calls(false); len(calls) != 2 ||
		!strings.Contains(strings.Join(calls[1], " "), "--open-dingtalk-id peer --group "+egressConversation+" --message-id peer-question --ref-sender peer") {
		t.Fatalf("expected the peer's own message as the native quote target: %v", calls)
	}

	block := filepath.Join(f.Dir, "block-reaction-peer-delayed")
	triggertest.Write(t, block, nil)
	f.feed(3, "peer-delayed", "peer", "another question")
	f.feedbackPhase(egressConversation, "peer-delayed", feedbackReady)
	delayedReply := f.reply("peer-delayed", "another reply")
	triggertest.Wait(t, "reaction failure does not block native sending", func() bool {
		return f.outbox(delayedReply).Status == "failed"
	})
	f.requireEmotion("peer-delayed", "")
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	f.feedbackPhase(egressConversation, "peer-delayed", feedbackThinking)
	f.requireEmotion("peer-delayed", "thinking-id")
}

func TestDWSGroupFeedbackSuppressesMatchingEchoAndUnmatchedInput(t *testing.T) {
	t.Parallel()
	const group = "CID:test-group"
	f := newEgressFixture(t, false)
	feed := func(n int, id, text string) {
		input := event(id, text)
		input["type"], input["conversation_id"], input["sender_open_dingtalk_id"] = groupEvent, group, "self"
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		triggertest.Write(t, filepath.Join(f.Dir, fmt.Sprintf("event-%03d.json", n)), raw)
	}
	feed(1, "unmatched", "unmatched")
	feed(2, "group-question", "test question")
	feed(3, "group-question", "test question")
	trigger := f.trigger(f.Endpoint, f.feedbackConfig, func(c *dwsConfig) {
		c.Direct, c.SelfChat = directConfig{}, selfChatConfig{}
		prefix := "test "
		c.Groups = []groupConfig{{ConversationID: group, ReplyEnabled: true, Match: []matchRule{{Prefix: &prefix}}}}
	})
	trigger.Endpoint(t, "statusEndpoint")
	f.feedbackPhase(group, "group-question", feedbackThinking)
	f.requireEmotion("group-question", "thinking-id")
	id := f.reply("group-question", "test answer")
	task := f.waitTask(id)
	feed(4, "group-answer", "test answer")
	f.AssertRouted(f.echo("group-answer", "held"), 0)
	f.requireEmotion("group-answer", "")
	f.settle(task, group, "group-answer")
	f.feedbackPhase(group, "group-question", feedbackDone)
	f.feedbackPhase(group, "group-answer", feedbackSuppressed)
	f.requireEmotion("group-question", "done-id")
	f.requireEmotion("group-answer", "")
	f.requireEmotion("unmatched", "")
	f.AssertRouted(f.echo("group-answer", "outbound_echo"), 0)
	if calls := f.calls(false); len(calls) != 1 ||
		!strings.Contains(strings.Join(calls[0], " "), "--group "+group+" --message-id group-question --ref-sender self") ||
		strings.Contains(strings.Join(calls[0], " "), "--open-dingtalk-id") {
		t.Fatalf("expected exactly one native group quote: %v", calls)
	}
}
