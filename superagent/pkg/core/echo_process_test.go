package core_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestEchoUsesActualSourceConversationAndMessage(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	_, other, _ := addOutboxPeerSources(t, f)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	target := submit(t, p, f, input("echo-original"))
	var sentIDs []string
	for _, native := range []string{"native-exact", "native-other-conversation"} {
		item := roleWork[core.OutboxItem](t, p, roleToken(t, f, router), "send_reply", core.ReplyRequest{
			OperationID: "echo-reply-" + native, MessageID: target.MessageID, Content: markdownReply("outbound"),
		})
		resultOutbox(t, p, f.token, outboxResult(item, "sent", "", native))
		sentIDs = append(sentIDs, item.ID)
	}

	foreign := input("native-exact")
	foreign.Message.Source, foreign.Message.AccountID = "other", "account-2"
	foreign.Message.ThreadRef = core.ThreadRef{Source: "other", ID: "actual:conversation"}
	foreign.Message.Channel.ConversationID = "actual:conversation"
	var foreignReceipt core.Receipt
	status, body := request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", other, foreign)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, json.Unmarshal(body, &foreignReceipt))
	require.Nil(t, readRecord[core.Message](t, p, "messages/"+foreignReceipt.MessageID).Echo)

	otherConversation := submit(t, p, f, input("native-other-conversation"))
	require.Nil(t, readRecord[core.Message](t, p, "messages/"+otherConversation.MessageID).Echo)

	exact := input("native-exact")
	exact.Message.ThreadRef.ID, exact.Message.Channel.ConversationID = "actual:conversation", "actual:conversation"
	receipt := submit(t, p, f, exact)
	msg := readRecord[core.Message](t, p, "messages/"+receipt.MessageID)
	require.NotNil(t, msg.Echo)
	require.Equal(t, "outbound_echo", msg.Echo.Status)
	require.Equal(t, &core.EchoReference{OutboxID: sentIDs[0]}, msg.Echo.Match)
	require.Empty(t, routingEvents(t, p, msg.ID))
	require.Empty(t, receipt.EventID)
	receipt.Duplicate = true
	require.Equal(t, receipt, submit(t, p, f, exact))
}

func TestDWSGroupReplyAndEchoReceipt(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	peer, other, _ := addOutboxPeerSources(t, f)
	operator := strings.Repeat("O", 48)
	operatorFile := filepath.Join(f.dir, "operator.token")
	write(t, operatorFile, operator)
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, string(config)+"operatorTokenFile: "+operatorFile+"\n")
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)

	group := func(id string) core.SubmitRequest {
		r := input(id)
		r.Message.Addressed = "group"
		r.Message.Channel.Address = map[string]any{
			"groupOpenConversationId": r.Message.ThreadRef.ID,
			"senderOpenDingtalkId":    r.Message.Actor,
		}
		return r
	}
	lookupPath := func(id string) string {
		return "/v1/channel/messages?sourceMessageId=" + url.QueryEscape(id)
	}
	receiptFor := func(token, id string) core.Receipt {
		t.Helper()
		status, raw := request(t, http.MethodGet, p.endpoint, lookupPath(id), token, nil)
		require.Equal(t, http.StatusOK, status, "%s", raw)
		require.NotContains(t, string(raw), `"text"`)
		var receipt core.Receipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		return receipt
	}

	invalid := []struct {
		name string
		edit func(*core.Channel)
	}{
		{"wrong-channel", func(c *core.Channel) { c.Type = "other" }},
		{"missing-sender", func(c *core.Channel) { c.ActorID = "" }},
		{"wrong-conversation", func(c *core.Channel) { c.ConversationID = "other" }},
		{"wrong-message", func(c *core.Channel) { c.MessageID = "other" }},
		{"wrong-address-conversation", func(c *core.Channel) { c.Address["groupOpenConversationId"] = "other" }},
		{"wrong-address-sender", func(c *core.Channel) { c.Address["senderOpenDingtalkId"] = "other" }},
		{"extra-peer", func(c *core.Channel) { c.Address["peerOpenDingtalkId"] = "other" }},
		{"non-text-sender", func(c *core.Channel) { c.Address["senderOpenDingtalkId"] = []string{"sender-1"} }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			r := group(tc.name)
			tc.edit(r.Message.Channel)
			status, raw := request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", f.token, r)
			errorCode(t, status, raw, http.StatusBadRequest, "invalid_argument")
		})
	}

	original := group("group-message:/+% 中文")
	target := submit(t, p, f, original)
	require.Equal(t, target, receiptFor(peer, original.Message.SourceMessageID))
	lookup := lookupPath(original.Message.SourceMessageID)
	for _, tc := range []struct {
		path, token, code string
		want              int
	}{
		{lookup, other, "not_found", http.StatusNotFound},
		{lookup, "", "unauthorized", http.StatusUnauthorized},
		{lookupPath("missing"), f.token, "not_found", http.StatusNotFound},
		{"/v1/channel/messages", f.token, "invalid_argument", http.StatusBadRequest},
		{lookup + "&sourceMessageId=another", f.token, "invalid_argument", http.StatusBadRequest},
	} {
		status, raw := request(t, http.MethodGet, p.endpoint, tc.path, tc.token, nil)
		errorCode(t, status, raw, tc.want, tc.code)
	}

	reply := func(id string) core.OutboxItem {
		t.Helper()
		return roleWork[core.OutboxItem](t, p, routerToken, "send_reply", core.ReplyRequest{
			OperationID: id, MessageID: target.MessageID, Content: markdownReply("Group reply"),
		})
	}
	settle := func(item core.OutboxItem, id string) core.OutboxItem {
		t.Helper()
		result := outboxResult(item, "sent", "", id)
		result.ConversationID = original.Message.ThreadRef.ID
		return resultOutbox(t, p, f.token, result)
	}
	checkItemScope := func(id string, expected core.OutboxItem) {
		t.Helper()
		path := channelOutbox + "/items/" + id
		status, raw := request(t, http.MethodGet, p.endpoint, path, f.token, nil)
		require.Equal(t, http.StatusOK, status, "%s", raw)
		var detail struct {
			core.OutboxItem
			Channel core.Channel
		}
		require.NoError(t, json.Unmarshal(raw, &detail))
		require.Equal(t, *original.Message.Channel, detail.Channel)
		require.Equal(t, expected, detail.OutboxItem)
		status, raw = request(t, http.MethodGet, p.endpoint, path, other, nil)
		errorCode(t, status, raw, http.StatusNotFound, "not_found")
	}

	item := reply("first-reply")
	checkItemScope(item.ID, item)
	echo, ownInput := group("group-echo"), group("group-own-input")
	echo.SelfSent, ownInput.SelfSent = true, true
	for _, r := range []core.SubmitRequest{echo, ownInput} {
		held := submit(t, p, f, r)
		require.Equal(t, "held", held.EchoStatus)
		require.Empty(t, held.EventID)
		require.Equal(t, held, receiptFor(f.token, r.Message.SourceMessageID))
	}
	sent := settle(item, echo.Message.SourceMessageID)
	checkItemScope(item.ID, sent)
	echoReceipt := receiptFor(f.token, echo.Message.SourceMessageID)
	require.Equal(t, "outbound_echo", echoReceipt.EchoStatus)
	require.Empty(t, routingEvents(t, p, echoReceipt.MessageID))
	echoReceipt.Duplicate = true
	require.Equal(t, echoReceipt, submit(t, p, f, echo))
	ownReceipt := receiptFor(f.token, ownInput.Message.SourceMessageID)
	require.Equal(t, "cleared", ownReceipt.EchoStatus)
	require.Len(t, routingEvents(t, p, ownReceipt.MessageID), 1)

	lateItem := reply("late-reply")
	late := group("group-echo-late-match")
	late.SelfSent = true
	lateReceipt := submit(t, p, f, late)
	require.Equal(t, "held", lateReceipt.EchoStatus)
	advance(2 * time.Minute)
	require.Eventually(t, func() bool {
		return receiptFor(f.token, late.Message.SourceMessageID).EchoStatus == "echo_unknown"
	}, 5*time.Second, 50*time.Millisecond)
	status, raw := request(t, http.MethodPost, p.endpoint, "/v1/operator/echo-release", operator,
		core.EchoReleaseRequest{MessageID: lateReceipt.MessageID, OperationID: "echo-release", Reason: "Provisional operator decision"})
	require.Equal(t, http.StatusOK, status, "%s", raw)
	require.Equal(t, "released", receiptFor(f.token, late.Message.SourceMessageID).EchoStatus)
	settle(lateItem, late.Message.SourceMessageID)
	require.Equal(t, "outbound_echo", receiptFor(f.token, late.Message.SourceMessageID).EchoStatus)
	require.Len(t, routingEvents(t, p, lateReceipt.MessageID), 1)
}
