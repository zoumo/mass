package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	rpc2 "github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type discardRPCHandler struct{}

func (discardRPCHandler) Handle(context.Context, *rpc2.Conn, *rpc2.Request) {}

func newSubscriptionWriter(t *testing.T) (*rpcSubscription, *websocket.Conn) {
	t.Helper()
	serverConn := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConn <- conn
	}))
	t.Cleanup(httpServer.Close)
	peer, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	stream, err := rpcwire.NewStream(<-serverConn, rpcwire.RoleServer, time.Second)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	conn := rpc2.NewConn(ctx, stream, discardRPCHandler{})
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
	})
	return &rpcSubscription{id: "1", conn: conn}, peer
}

func TestRPCSubscriptionWriterRejectsLossyEventBeforePeer(t *testing.T) {
	subscription, peer := newSubscriptionWriter(t)
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "demo",
		SessionName: "observed-\xff",
		SessionID:   "observed",
		Time:        time.Now().UTC(),
		ItemID:      uuid.NewString(),
	}, events.AgentMessage{
		MessageID: uuid.NewString(),
		Content:   []events.ContentBlock{events.Text("hello")},
	}, events.Source{})
	require.NoError(t, err)

	require.ErrorIs(t, subscription.notifyEvent(event), rpcwire.ErrLossyJSON)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(150*time.Millisecond)))
	_, data, err := peer.ReadMessage()
	require.Error(t, err)
	require.NotContains(t, string(data), "\ufffd")
}

func TestRPCSubscriptionCloseSanitizesLossyCause(t *testing.T) {
	subscription, peer := newSubscriptionWriter(t)
	subscription.sendClosed(&runtime.OperationError{
		Code:    "internal",
		Reason:  "lossy",
		Message: "bad-\xff",
	})

	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	_, data, err := peer.ReadMessage()
	require.NoError(t, err)
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Error *runtime.OperationError `json:"error"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(data, &frame))
	require.Equal(t, "subscription.closed", frame.Method)
	require.Equal(t, &runtime.OperationError{
		Code:    "internal",
		Reason:  "event_delivery_failed",
		Message: "Event delivery failed",
	}, frame.Params.Error)
}
