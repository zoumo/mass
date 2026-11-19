package client_test

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestInjectClientInvalidInputIsKnownLocalRejection(t *testing.T) {
	c, err := client.New("invalid-url", "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	defer c.Close()
	err = c.Inject(t.Context(), resource.SessionRef{SessionID: "native"}, runtime.Input{}, nil)
	var p *runtime.OperationError
	require.ErrorAs(t, err, &p)
	require.Equal(t, "invalid_argument", p.Code)
}

func TestClientInjectReplacesReceipt(t *testing.T) {
	sparse := map[string]any{"acceptance": map[string]any{"sent": true}}
	for _, test := range []struct {
		name  string
		reply map[string]any
		code  string
	}{
		{name: "success", reply: map[string]any{"result": sparse}},
		{
			name: "service error", code: "outcome_unknown",
			reply: map[string]any{"error": map[string]any{
				"code": rpcwire.CodeServiceError, "message": "Operation failed",
				"data": map[string]any{
					"code": "outcome_unknown", "reason": "native_reply_lost", "message": "Native reply lost", "receipt": sparse,
				},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var typed runtime.InjectReceipt
			var dynamic map[string]any
			for _, out := range []any{&typed, &dynamic} {
				inject, accept := injectReceiptClient(t, test.reply)
				require.NoError(t, inject(out))
				if out == &dynamic {
					require.Equal(t, accept, dynamic)
				}
				err := inject(out)
				if test.code != "" {
					var problem *runtime.OperationError
					require.ErrorAs(t, err, &problem)
					require.Equal(t, test.code, problem.Code)
					require.Equal(t, "native_reply_lost", problem.Reason)
				} else {
					require.NoError(t, err)
				}
			}
			assert.Equal(t, runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, typed)
			require.Equal(t, sparse, dynamic)
		})
	}
}

func TestClientInjectClearsReceiptOnTransportLoss(t *testing.T) {
	inject, _ := injectReceiptClient(t, nil)
	var receipt runtime.InjectReceipt
	require.NoError(t, inject(&receipt))
	require.Equal(t, runtime.Yes(), receipt.Acceptance.NativeAccepted)
	var problem *runtime.OperationError
	require.ErrorAs(t, inject(&receipt), &problem)
	require.Equal(t, "outcome_unknown", problem.Code)
	require.Equal(t, "operation_unconfirmed", problem.Reason)
	require.Equal(t, runtime.InjectReceipt{}, receipt)
}

func TestClientInjectDiscardsMalformedReceiptAndPreservesServiceError(t *testing.T) {
	inject, _ := injectReceiptClient(t, map[string]any{"error": map[string]any{
		"code": rpcwire.CodeServiceError, "message": "Operation failed",
		"data": map[string]any{
			"code": "outcome_unknown", "reason": "native_reply_lost", "message": "Native reply lost",
			"receipt": map[string]any{"acceptance": map[string]any{"sent": true, "nativeAccepted": "invalid"}},
		},
	}})
	var receipt runtime.InjectReceipt
	require.NoError(t, inject(&receipt))
	err := inject(&receipt)
	var problem *runtime.OperationError
	require.ErrorAs(t, err, &problem)
	require.Equal(t, "outcome_unknown", problem.Code)
	var decodeErr *json.UnmarshalTypeError
	require.ErrorAs(t, err, &decodeErr)
	require.Equal(t, runtime.InjectReceipt{}, receipt)
}

func injectReceiptClient(t *testing.T, reply map[string]any) (inject func(any) error, accepted map[string]any) {
	t.Helper()
	accepted = map[string]any{
		"acceptance": map[string]any{"sent": true, "nativeAccepted": true},
		"sequence":   json.Number("9007199254740993"),
	}
	server := newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		replies := []map[string]any{
			{"result": map[string]any{
				"protocolVersion": rpcwire.Version,
				"serverInfo":      map[string]string{"name": rpcwire.ServerName, "version": "test"},
			}},
			{"result": accepted},
			reply,
		}
		for _, response := range replies {
			var request struct {
				ID json.RawMessage `json:"id"`
			}
			if conn.ReadJSON(&request) != nil || response == nil {
				return
			}
			message := map[string]any{"jsonrpc": "2.0", "id": request.ID}
			maps.Copy(message, response)
			if conn.WriteJSON(message) != nil {
				return
			}
		}
	})
	wsClient := newTestClient(t, websocketURL(server), "")
	t.Cleanup(func() { require.NoError(t, wsClient.Close()) })
	ref := resource.SessionRef{SessionID: "native"}
	input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
	return func(out any) error {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		return wsClient.Inject(ctx, ref, input, out)
	}, accepted
}
