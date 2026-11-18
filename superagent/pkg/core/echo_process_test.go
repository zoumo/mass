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
	item := roleWork[core.OutboxItem](t, p, roleToken(t, f, router), "message_reply", core.ReplyRequest{
		OperationID: "echo-reply", MessageID: target.MessageID, Text: "outbound",
	})
	grant := beginOutbox(t, p, f.token, "echo-claim", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("echo-confirm", grant))
	for _, native := range []string{"native-exact", "native-other-conversation"} {
		channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finishCommand(native, grant, "sent", "", native))
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
	require.Equal(t, &core.EchoReference{OutboxID: item.ID, AttemptID: grant.AttemptID}, msg.Echo.Match)
	require.Empty(t, deliveriesFor(t, p, msg.EventID))
	require.Equal(t, "message.outbound_echo", readRecord[core.Event](t, p, "events/"+msg.EventID).Type)
	require.Equal(t, receipt, submit(t, p, f, exact))
}
