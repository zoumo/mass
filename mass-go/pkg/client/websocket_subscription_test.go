package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type clientSubscriptionAPI struct {
	service.RuntimeService
	service.SessionService

	broadcast *events.EventBroadcast

	mu          sync.Mutex
	prePublish  []events.SessionEvent
	cancelCalls int
}

func (a *clientSubscriptionAPI) SubscribeEvents(resource.SessionRef) (*service.Subscription, error) {
	subscription, err := a.broadcast.Subscribe()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	pending := append([]events.SessionEvent(nil), a.prePublish...)
	a.mu.Unlock()
	for _, event := range pending {
		if err := a.broadcast.Publish(event); err != nil {
			_ = subscription.Close()
			return nil, err
		}
	}
	return subscription, nil
}

func (*clientSubscriptionAPI) GetSession(resource.SessionRef) (resource.Session, error) {
	return resource.Session{
		Metadata: resource.Metadata{Namespace: "demo", Name: "observed"},
		Status:   resource.SessionStatus{SessionID: "observed", State: events.Idle},
	}, nil
}

func (a *clientSubscriptionAPI) Cancel(context.Context, resource.SessionRef) (runtime.CancelReceipt, error) {
	a.mu.Lock()
	a.cancelCalls++
	a.mu.Unlock()
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (a *clientSubscriptionAPI) setPrePublish(subscriptions ...events.SessionEvent) {
	a.mu.Lock()
	a.prePublish = subscriptions
	a.mu.Unlock()
}

func (a *clientSubscriptionAPI) cancelCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelCalls
}

type clientSubscriptionFixture struct {
	api       *clientSubscriptionAPI
	http      *httptest.Server
	broadcast *events.EventBroadcast
}

func newClientSubscriptionFixture(t *testing.T) *clientSubscriptionFixture {
	t.Helper()
	broadcast, err := events.NewBroadcast("demo", "observed", "observed", 64)
	require.NoError(t, err)
	api := &clientSubscriptionAPI{broadcast: broadcast}
	server, err := transport.New(api, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		_ = broadcast.Close()
		server.Shutdown()
		httpServer.Close()
	})
	return &clientSubscriptionFixture{api: api, http: httpServer, broadcast: broadcast}
}

func clientSubscriptionEvent(t *testing.T, text string) events.SessionEvent {
	t.Helper()
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "demo",
		SessionName: "observed",
		SessionID:   "observed",
		ItemID:      uuid.NewString(),
		Time:        time.Now().UTC(),
	}, events.AgentMessage{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}, events.Source{})
	require.NoError(t, err)
	return event
}

func nextClientSubscription(t *testing.T, subscription *client.Subscription) events.SessionEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	event, err := subscription.Next(ctx)
	require.NoError(t, err)
	return event
}

func TestWebSocketClientSubscriptionConnectionsAndOwnership(t *testing.T) {
	fixture := newClientSubscriptionFixture(t)
	prePublished := clientSubscriptionEvent(t, "pre-published")
	fixture.api.setPrePublish(prePublished)
	wsClient := client.NewWebSocket(fixture.http.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	var initialized initialized
	require.NoError(t, wsClient.Initialize(t.Context(), nil, &initialized))
	first, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)
	fixture.api.setPrePublish()
	second, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, prePublished.EventID, nextClientSubscription(t, first).EventID)

	event := clientSubscriptionEvent(t, "both")
	require.NoError(t, fixture.broadcast.Publish(event))
	require.Equal(t, event.EventID, nextClientSubscription(t, first).EventID)
	require.Equal(t, event.EventID, nextClientSubscription(t, second).EventID)

	require.NoError(t, first.Close())
	after := clientSubscriptionEvent(t, "after first close")
	require.NoError(t, fixture.broadcast.Publish(after))
	require.Equal(t, after.EventID, nextClientSubscription(t, second).EventID)

	var session resource.Session
	require.NoError(t, wsClient.GetSession(t.Context(), resource.SessionRef{SessionID: "observed"}, &session))
	require.Equal(t, "observed", session.Status.SessionID)

	require.NoError(t, wsClient.Close())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = second.Next(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client is closed")
	require.Equal(t, 0, fixture.api.cancelCount())
}

func TestWebSocketClientSubscriptionUnsubscribe(t *testing.T) {
	fixture := newClientSubscriptionFixture(t)
	wsClient := client.NewWebSocket(fixture.http.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	subscription, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)

	event := clientSubscriptionEvent(t, "before unsubscribe")
	require.NoError(t, fixture.broadcast.Publish(event))
	require.Equal(t, event.EventID, nextClientSubscription(t, subscription).EventID)
	require.NoError(t, subscription.Unsubscribe(t.Context()))
	require.ErrorIs(t, subscription.Err(), io.EOF)
	require.Equal(t, 0, fixture.api.cancelCount())
}

func TestWebSocketClientSubscriptionNaturalTermination(t *testing.T) {
	fixture := newClientSubscriptionFixture(t)
	wsClient := client.NewWebSocket(fixture.http.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	subscription, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)

	require.NoError(t, fixture.broadcast.Close())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = subscription.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 0, fixture.api.cancelCount())
}

func TestWebSocketClientSubscriptionLocalOverflowFailsObservation(t *testing.T) {
	fixture := newClientSubscriptionFixture(t)
	wsClient := client.NewWebSocket(fixture.http.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	subscription, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)

	for range 200 {
		require.NoError(t, fixture.broadcast.Publish(clientSubscriptionEvent(t, "flood")))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var terminal error
	for terminal == nil {
		_, terminal = subscription.Next(ctx)
	}
	var operation *runtime.OperationError
	require.ErrorAs(t, terminal, &operation)
	require.Equal(t, "resource_exhausted", operation.Code)
	require.Equal(t, "slow_subscriber", operation.Reason)
	require.Equal(t, 0, fixture.api.cancelCount())
}

func TestWebSocketClientSubscriptionEOFWithoutClosedIsAbnormal(t *testing.T) {
	httpServer := newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		for range 2 {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(data, &request) != nil {
				return
			}
			switch request.Method {
			case "initialize":
				_ = conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil,
					`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"1","serverInfo":{"name":"mass","version":"test"}}}`,
					request.ID,
				))
			case "session.subscribe":
				_ = conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil,
					`{"jsonrpc":"2.0","id":%s,"result":{"subscriptionId":"1"}}`,
					request.ID,
				))
			default:
				return
			}
		}
	})
	wsClient := client.NewWebSocket(httpServer.URL, "")
	t.Cleanup(func() { _ = wsClient.Close() })
	subscription, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = subscription.Next(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NotErrorIs(t, err, io.EOF)
}

func TestWebSocketClientSubscriptionRejectsMalformedNotification(t *testing.T) {
	httpServer := newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		for range 2 {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(data, &request) != nil {
				return
			}
			if request.Method == "initialize" {
				_ = conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil,
					`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"1","serverInfo":{"name":"mass","version":"test"}}}`,
					request.ID,
				))
			} else {
				_ = conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil,
					`{"jsonrpc":"2.0","id":%s,"result":{"subscriptionId":"1"}}`,
					request.ID,
				))
			}
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(
			`{"jsonrpc":"2.0","method":"session.event","params":{"subscriptionId":"other"}}`,
		))
	})
	wsClient := client.NewWebSocket(httpServer.URL, "")
	t.Cleanup(func() { _ = wsClient.Close() })
	subscription, err := wsClient.Subscribe(t.Context(), resource.SessionRef{SessionID: "observed"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = subscription.Next(ctx)
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
	require.True(t, strings.Contains(err.Error(), "invalid session.event notification") || errors.Is(err, io.ErrUnexpectedEOF))
}
