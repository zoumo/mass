package jsonrpc

import (
	"context"
	"encoding/json"
	"net"

	"github.com/sourcegraph/jsonrpc2"
)

// NotificationHandler handles inbound server-side notifications.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

// NotificationMsg is a channel-friendly notification envelope.
type NotificationMsg struct {
	Method string
	Params json.RawMessage
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithNotificationHandler registers a handler for inbound notifications.
// Mutually exclusive with WithNotificationChannel.
func WithNotificationHandler(h NotificationHandler) ClientOption {
	return func(c *Client) {
		c.notifHandler = h
	}
}

// WithNotificationChannel registers a channel for inbound notifications.
// The notification worker goroutine writes to this channel; if the channel
// is full, the write blocks (backpressure).
// Mutually exclusive with WithNotificationHandler.
func WithNotificationChannel(ch chan<- NotificationMsg) ClientOption {
	return func(c *Client) {
		c.notifChanOut = ch
	}
}

// DialOption configures a Dial call.
type DialOption = ClientOption

// notificationMsg is a queued inbound notification.
type notificationMsg struct {
	ctx    context.Context
	method string
	params json.RawMessage
}

// Client is a JSON-RPC 2.0 client wrapping sourcegraph/jsonrpc2.
// It uses a bounded FIFO notification worker to ensure notifications
// are delivered in order without blocking response dispatch.
type Client struct {
	conn         *jsonrpc2.Conn
	notifHandler NotificationHandler
	notifChanOut chan<- NotificationMsg // caller-provided channel (WithNotificationChannel)
	notifCh      chan notificationMsg
	workerDone   chan struct{}
}

// NewClient wraps an existing net.Conn and returns a Client.
// Panics if both WithNotificationHandler and WithNotificationChannel are set.
func NewClient(nc net.Conn, opts ...ClientOption) *Client {
	c := &Client{
		notifCh:    make(chan notificationMsg, 256),
		workerDone: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.notifHandler != nil && c.notifChanOut != nil {
		panic("jsonrpc: WithNotificationHandler and WithNotificationChannel are mutually exclusive")
	}

	ctx := context.Background()
	stream := jsonrpc2.NewPlainObjectStream(nc)
	c.conn = jsonrpc2.NewConn(ctx, stream, &clientHandler{client: c})

	go c.notifWorker()
	return c
}

// Dial connects to network/address and returns a Client.
func Dial(ctx context.Context, network, address string, opts ...DialOption) (*Client, error) {
	nc, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return NewClient(nc, opts...), nil
}

// Call invokes a JSON-RPC method and decodes the result.
// Context cancellation causes Call to return ctx.Err() immediately;
// the pending response entry is cleaned up when the response arrives or the connection closes.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.conn.Call(ctx, method, params, result)
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return c.conn.Notify(ctx, method, params)
}

// CallAsync sends a JSON-RPC request with an ID but does not wait for
// the response. The response is silently discarded when it arrives.
// Use this for long-running RPC methods where the caller monitors
// progress through notifications instead of waiting for the response.
func (c *Client) CallAsync(ctx context.Context, method string, params any) error {
	go func() { _ = c.conn.Call(ctx, method, params, nil) }()
	return nil
}

// Close closes the underlying connection and drains the notification worker.
func (c *Client) Close() error {
	err := c.conn.Close()
	// Close notifCh so the worker drains and exits.
	// Guard against double-close with a recover.
	func() {
		defer func() { recover() }() //nolint:revive
		close(c.notifCh)
	}()
	<-c.workerDone
	return err
}

// DisconnectNotify returns a channel closed when the connection is lost.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.conn.DisconnectNotify()
}

// notifWorker drains notifCh and delivers notifications via either
// the registered handler callback or the caller-provided channel (FIFO).
func (c *Client) notifWorker() {
	defer close(c.workerDone)
	for msg := range c.notifCh {
		switch {
		case c.notifChanOut != nil:
			c.notifChanOut <- NotificationMsg{
				Method: msg.method,
				Params: msg.params,
			}
		case c.notifHandler != nil:
			c.notifHandler(msg.ctx, msg.method, msg.params)
		}
	}
}

// enqueueNotification puts a notification on the bounded channel.
// Blocks (backpressure) if the channel is full — this pauses the read loop
// until the worker catches up.
func (c *Client) enqueueNotification(ctx context.Context, method string, params json.RawMessage) {
	c.notifCh <- notificationMsg{ctx: ctx, method: method, params: params}
}

// clientHandler implements jsonrpc2.Handler for the Client.
type clientHandler struct {
	client *Client
}

func (h *clientHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Only handle inbound notifications (server-push); responses are handled
	// internally by jsonrpc2.Conn's pending map.
	if !req.Notif {
		return
	}
	var params json.RawMessage
	if req.Params != nil {
		params = *req.Params
	}
	// Enqueue — may block if buffer is full (backpressure).
	h.client.enqueueNotification(ctx, req.Method, params)
}
