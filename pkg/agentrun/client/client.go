// Package client provides a typed Client and Dial helpers for connecting
// to agent-run Unix sockets backed by pkg/jsonrpc.
package client

import (
	"context"
	"encoding/json"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/watch"
)

// Client is a typed client for the Agent Run JSON-RPC protocol.
type Client struct {
	c *jsonrpc.Client
}

// New creates a Client wrapping the given jsonrpc.Client.
func New(c *jsonrpc.Client) *Client {
	return &Client{c: c}
}

// RawClient returns the underlying jsonrpc.Client for advanced usage.
func (c *Client) RawClient() *jsonrpc.Client {
	return c.c
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.c.Close()
}

// DisconnectNotify returns a channel closed when the agent-run disconnects.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.c.DisconnectNotify()
}

func (c *Client) Prompt(ctx context.Context, req *runapi.SessionPromptParams) (*runapi.SessionPromptResult, error) {
	var result runapi.SessionPromptResult
	if err := c.c.Call(ctx, runapi.MethodSessionPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendPrompt sends a session/prompt request without waiting for the response.
// The caller should monitor the notification stream for turn progress and
// turn_end events. Use this for interactive/TUI scenarios where blocking
// until turn completion is not desired.
func (c *Client) SendPrompt(ctx context.Context, req *runapi.SessionPromptParams) error {
	return c.c.CallAsync(ctx, runapi.MethodSessionPrompt, req)
}

func (c *Client) Cancel(ctx context.Context) error {
	return c.c.Call(ctx, runapi.MethodSessionCancel, nil, nil)
}

func (c *Client) Load(ctx context.Context, req *runapi.SessionLoadParams) error {
	return c.c.Call(ctx, runapi.MethodSessionLoad, req, nil)
}

// WatchEvent starts a watch stream and returns a watch.Interface delivering
// typed AgentRunEvent values.
//
// The watchID is generated and injected by the jsonrpc transport layer
// (Client.Watch) — callers do not set it.
//
// IMPORTANT: this is a single-shot watch. If the consumer is slow, the jsonrpc
// layer evicts the stream (closes ResultChan). For zero-drop semantics with
// automatic reconnection, use NewWatchFunc with watch.NewRetryWatcher instead.
func (c *Client) WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams) (watch.Interface[runapi.AgentRunEvent], error) {
	if req == nil {
		req = &runapi.SessionWatchEventParams{}
	}
	var result runapi.SessionWatchEventResult
	ws, err := c.c.Watch(ctx, runapi.MethodRuntimeWatchEvent, req, &result, 256)
	if err != nil {
		return nil, err
	}
	return newTypedWatcher(ws), nil
}

func (c *Client) Status(ctx context.Context) (*runapi.RuntimeStatusResult, error) {
	var result runapi.RuntimeStatusResult
	if err := c.c.Call(ctx, runapi.MethodRuntimeStatus, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error) {
	var result runapi.SessionSetModelResult
	if err := c.c.Call(ctx, runapi.MethodSessionSetModel, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) Stop(ctx context.Context) error {
	return c.c.Call(ctx, runapi.MethodRuntimeStop, nil, nil)
}

// Dial connects to an agent-run socket and returns a typed Client.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*Client, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// ParseAgentRunEvent unmarshals a raw runtime/event_update notification params
// payload into a typed AgentRunEvent.
func ParseAgentRunEvent(params json.RawMessage) (runapi.AgentRunEvent, error) {
	var ev runapi.AgentRunEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		return runapi.AgentRunEvent{}, err
	}
	return ev, nil
}
