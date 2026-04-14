// Package client provides a Dial helper that returns a typed ShimClient
// backed by pkg/jsonrpc.
package client

import (
	"context"
	"encoding/json"

	apishim "github.com/zoumo/oar/pkg/shim/api"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/pkg/jsonrpc"
)

// NotificationHandler handles inbound shim/event notifications.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

// Dial connects to a shim socket and returns a typed ShimClient.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*apishim.ShimClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return apishim.NewShimClient(c), nil
}

// DialWithHandler connects to a shim socket and registers an event
// notification handler that is called for every inbound shim/event push.
func DialWithHandler(ctx context.Context, socketPath string, handler NotificationHandler) (*apishim.ShimClient, error) {
	return Dial(ctx, socketPath, jsonrpc.WithNotificationHandler(jsonrpc.NotificationHandler(handler)))
}

// ParseShimEvent unmarshals a raw shim/event notification params payload into
// a typed ShimEvent.
func ParseShimEvent(params json.RawMessage) (events.ShimEvent, error) {
	var ev events.ShimEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		return events.ShimEvent{}, err
	}
	return ev, nil
}
