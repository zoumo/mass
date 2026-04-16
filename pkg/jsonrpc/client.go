package jsonrpc

import (
	"context"
	"encoding/json"
	"net"
	"sync"

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

// subscription is a per-method notification subscription created by Subscribe().
// Each subscription has a dedicated channel that receives only notifications
// matching the subscribed method.
type subscription struct {
	method string
	ch     chan NotificationMsg
}

// Client is a JSON-RPC 2.0 client wrapping sourcegraph/jsonrpc2.
// It uses a bounded FIFO notification worker to ensure notifications
// are delivered in order without blocking response dispatch.
//
// Notification routing (in priority order):
//  1. Per-method subscribers (Subscribe) — matched notifications go here
//  2. Global handler (WithNotificationHandler) — fallback for unmatched
//  3. Global channel (WithNotificationChannel) — fallback for unmatched
//  4. Silent discard — if none of the above is configured
type Client struct {
	conn         *jsonrpc2.Conn
	notifHandler NotificationHandler
	notifChanOut chan<- NotificationMsg // caller-provided channel (WithNotificationChannel)
	notifCh      chan notificationMsg
	workerDone   chan struct{}

	// Per-method notification subscriptions (K8s Watch pattern).
	// Subscribe() creates entries; the returned unsubscribe func removes them.
	// notifWorker routes notifications to matching subscribers before falling
	// back to the global handler/channel.
	subMu   sync.Mutex
	subs    map[int]*subscription
	nextSub int
}

// NewClient wraps an existing net.Conn and returns a Client.
// Panics if both WithNotificationHandler and WithNotificationChannel are set.
func NewClient(nc net.Conn, opts ...ClientOption) *Client {
	c := &Client{
		notifCh:    make(chan notificationMsg, 256),
		workerDone: make(chan struct{}),
		subs:       make(map[int]*subscription),
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

// Subscribe creates a per-method notification channel. Only notifications
// matching the given method are delivered to the returned channel.
//
// This implements the K8s Watch pattern at the JSON-RPC transport layer:
// callers like WatchEvent() subscribe to "shim/event" notifications and
// receive a dedicated channel, instead of relying on a single global handler
// registered at Dial time.
//
// Routing priority in notifWorker:
//  1. Per-method subscribers (this method) — notification delivered to ALL
//     matching subscribers (fan-out, supports multiple watchers per method)
//  2. Global handler/channel — only if NO subscriber matched
//
// The returned unsubscribe function removes the subscription and closes the
// channel. It is safe to call multiple times (idempotent). When the connection
// closes, notifWorker closes all remaining subscriber channels automatically.
//
// Buffer sizing: bufSize should be large enough to absorb bursts. If a
// subscriber's channel is full, the notification is dropped (non-blocking
// send) and a warning is logged. This mirrors the Translator's slow-subscriber
// eviction at the application layer.
func (c *Client) Subscribe(method string, bufSize int) (<-chan NotificationMsg, func()) {
	c.subMu.Lock()
	defer c.subMu.Unlock()

	id := c.nextSub
	c.nextSub++
	ch := make(chan NotificationMsg, bufSize)
	c.subs[id] = &subscription{method: method, ch: ch}

	// Unsubscribe: remove from map + close channel (idempotent).
	return ch, func() {
		c.subMu.Lock()
		defer c.subMu.Unlock()
		if sub, ok := c.subs[id]; ok {
			close(sub.ch)
			delete(c.subs, id)
		}
	}
}

// notifWorker drains notifCh and delivers notifications to subscribers.
//
// Routing order:
//  1. Per-method subscribers — fan-out to ALL subscribers whose method matches.
//     Non-blocking send: if a subscriber's channel is full, the notification
//     is silently dropped for that subscriber (log warning would be noisy here;
//     the application-layer Translator already logs slow-subscriber eviction).
//  2. If NO subscriber matched, fall back to the global handler or channel
//     (backwards compatible with WithNotificationHandler/WithNotificationChannel).
//
// On connection close (notifCh drained), ALL subscriber channels are closed
// so that range loops on them terminate naturally. This propagates disconnect
// up to Watcher.ResultChan() consumers.
func (c *Client) notifWorker() {
	defer close(c.workerDone)
	for msg := range c.notifCh {
		nm := NotificationMsg{Method: msg.method, Params: msg.params}

		// Step 1: try per-method subscribers.
		matched := c.routeToSubscribers(nm)

		// Step 2: fallback to global handler/channel only if no subscriber matched.
		if !matched {
			switch {
			case c.notifChanOut != nil:
				c.notifChanOut <- nm
			case c.notifHandler != nil:
				c.notifHandler(msg.ctx, msg.method, msg.params)
			}
		}
	}

	// Connection closed — close all remaining subscriber channels so consumers
	// detect disconnect (range loop exits, Watcher.ResultChan() returns zero value).
	c.subMu.Lock()
	for id, sub := range c.subs {
		close(sub.ch)
		delete(c.subs, id)
	}
	c.subMu.Unlock()
}

// routeToSubscribers delivers a notification to all per-method subscribers
// whose method matches. Returns true if at least one subscriber was found.
func (c *Client) routeToSubscribers(msg NotificationMsg) bool {
	c.subMu.Lock()
	defer c.subMu.Unlock()

	matched := false
	for _, sub := range c.subs {
		if sub.method != msg.Method {
			continue
		}
		matched = true
		// Non-blocking send: drop if subscriber is slow.
		// The application layer (Translator) handles slow-consumer eviction
		// and connection teardown; transport-layer drops are best-effort.
		select {
		case sub.ch <- msg:
		default:
		}
	}
	return matched
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
