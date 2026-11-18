package core_test

import (
	"encoding/json"
	"net/http"
	"testing"

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
		item := roleWork[core.OutboxItem](t, p, roleToken(t, f, router), "message_reply", core.ReplyRequest{
			OperationID: "echo-reply-" + native, MessageID: target.MessageID, Text: "outbound",
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
	require.Empty(t, routingDeliveries(t, p, msg.ID))
	require.Empty(t, receipt.EventID)
	receipt.Duplicate = true
	require.Equal(t, receipt, submit(t, p, f, exact))
}
