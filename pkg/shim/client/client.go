// Package client provides a typed ShimClient and Dial helpers for connecting
// to agent-shim Unix sockets backed by pkg/jsonrpc.
package client

import (
	"context"
	"encoding/json"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ShimClient is a typed client for the Shim JSON-RPC protocol.
type ShimClient struct {
	c *jsonrpc.Client
}

// NewShimClient creates a ShimClient wrapping the given jsonrpc.Client.
func NewShimClient(c *jsonrpc.Client) *ShimClient {
	return &ShimClient{c: c}
}

// RawClient returns the underlying jsonrpc.Client for advanced usage.
func (c *ShimClient) RawClient() *jsonrpc.Client {
	return c.c
}

// Close closes the underlying connection.
func (c *ShimClient) Close() error {
	return c.c.Close()
}

// DisconnectNotify returns a channel closed when the shim disconnects.
func (c *ShimClient) DisconnectNotify() <-chan struct{} {
	return c.c.DisconnectNotify()
}

func (c *ShimClient) Prompt(ctx context.Context, req *apishim.SessionPromptParams) (*apishim.SessionPromptResult, error) {
	var result apishim.SessionPromptResult
	if err := c.c.Call(ctx, apishim.MethodSessionPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendPrompt sends a session/prompt request without waiting for the response.
// The caller should monitor the notification stream for turn progress and
// turn_end events. Use this for interactive/TUI scenarios where blocking
// until turn completion is not desired.
func (c *ShimClient) SendPrompt(ctx context.Context, req *apishim.SessionPromptParams) error {
	return c.c.CallAsync(ctx, apishim.MethodSessionPrompt, req)
}

func (c *ShimClient) Cancel(ctx context.Context) error {
	return c.c.Call(ctx, apishim.MethodSessionCancel, nil, nil)
}

func (c *ShimClient) Load(ctx context.Context, req *apishim.SessionLoadParams) error {
	return c.c.Call(ctx, apishim.MethodSessionLoad, req, nil)
}

// WatchEvent starts a K8s List-Watch style event subscription and returns a
// Watcher that delivers typed AgentRunEvent values through ResultChan().
//
// This replaces the old pattern of registering a global notification handler
// at Dial time. Each WatchEvent call creates an independent watch stream with
// its own watchID, allowing multiple concurrent watchers on a single connection.
//
// Usage:
//
//	watcher, err := client.WatchEvent(ctx, &shimapi.SessionWatchEventParams{FromSeq: &fromSeq})
//	if err != nil { ... }
//	defer watcher.Stop()
//	for ev := range watcher.ResultChan() { ... }
//
// When the connection drops (server evicts slow consumer, network failure, etc.),
// ResultChan() is closed. The consumer should reconnect:
//
//	client, _ = shimclient.Dial(ctx, socketPath)
//	watcher, _ = client.WatchEvent(ctx, &shimapi.SessionWatchEventParams{FromSeq: &lastSeq})
func (c *ShimClient) WatchEvent(ctx context.Context, req *apishim.SessionWatchEventParams) (*Watcher, error) {
	var result apishim.SessionWatchEventResult
	if err := c.c.Call(ctx, apishim.MethodRuntimeWatchEvent, req, &result); err != nil {
		return nil, err
	}

	// Subscribe to runtime/event_update notifications at the jsonrpc transport layer.
	// The Subscribe channel receives ALL runtime/event_update notifications on this
	// connection; the Watcher's filter goroutine demuxes by watchID.
	notifCh, unsub := c.c.Subscribe(apishim.MethodRuntimeEventUpdate, 1024)

	return newWatcher(result.WatchID, result.NextSeq, notifCh, unsub), nil
}

func (c *ShimClient) Status(ctx context.Context) (*apishim.RuntimeStatusResult, error) {
	var result apishim.RuntimeStatusResult
	if err := c.c.Call(ctx, apishim.MethodRuntimeStatus, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) SetModel(ctx context.Context, req *apishim.SessionSetModelParams) (*apishim.SessionSetModelResult, error) {
	var result apishim.SessionSetModelResult
	if err := c.c.Call(ctx, apishim.MethodSessionSetModel, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) Stop(ctx context.Context) error {
	return c.c.Call(ctx, apishim.MethodRuntimeStop, nil, nil)
}

// Dial connects to a shim socket and returns a typed ShimClient.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*ShimClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return NewShimClient(c), nil
}

// ParseAgentRunEvent unmarshals a raw runtime/event_update notification params
// payload into a typed AgentRunEvent.
func ParseAgentRunEvent(params json.RawMessage) (apishim.AgentRunEvent, error) {
	var ev apishim.AgentRunEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		return apishim.AgentRunEvent{}, err
	}
	return ev, nil
}
