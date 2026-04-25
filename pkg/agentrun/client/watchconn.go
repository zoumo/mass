package client

import (
	"context"
	"io"
	"sync"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/watch"
)

// agentRunConn wraps a *Watcher and implements watch.ClientConn[runapi.AgentRunEvent].
// Recv reads from Watcher.ResultChan() and wraps each event in watch.Event{Seq, Payload}.
// Close calls Watcher.Stop() exactly once.
type agentRunConn struct {
	w         *Watcher
	closeOnce sync.Once
}

// Recv blocks until the next Event arrives or the channel closes.
// Returns (zero, io.EOF) when the watcher channel is closed (connection lost or stopped).
func (c *agentRunConn) Recv() (watch.Event[runapi.AgentRunEvent], error) {
	ev, ok := <-c.w.ResultChan()
	if !ok {
		return watch.Event[runapi.AgentRunEvent]{}, io.EOF
	}
	return watch.Event[runapi.AgentRunEvent]{
		Seq:     ev.Seq,
		Payload: ev,
	}, nil
}

// Close stops the underlying Watcher exactly once (idempotent).
func (c *agentRunConn) Close() error {
	c.closeOnce.Do(func() { c.w.Stop() })
	return nil
}

// WatchEventConn returns a watch.ClientConn[AgentRunEvent] starting at fromSeq.
//
// Subscribe to runtime/event_update BEFORE sending the runtime/watch_event RPC
// to avoid a race where the server sends replay notifications between the RPC
// response and the Subscribe call, causing those notifications to be dropped.
func (c *Client) WatchEventConn(ctx context.Context, fromSeq int) (watch.ClientConn[runapi.AgentRunEvent], error) {
	// Step 1: subscribe first to ensure no replay notifications are missed.
	notifCh, unsub := c.c.Subscribe(runapi.MethodRuntimeEventUpdate, 1024)

	// Step 2: call runtime/watch_event RPC.
	fromSeqCopy := fromSeq
	var result runapi.SessionWatchEventResult
	if err := c.c.Call(ctx, runapi.MethodRuntimeWatchEvent, &runapi.SessionWatchEventParams{FromSeq: &fromSeqCopy}, &result); err != nil {
		unsub()
		return nil, err
	}

	// Step 3: build watcher using the pre-registered notifCh.
	w := newWatcher(result.WatchID, result.NextSeq, notifCh, unsub)
	return &agentRunConn{w: w, closeOnce: sync.Once{}}, nil
}

// ownedConn wraps a watch.ClientConn and an io.Closer (the underlying *Client).
// Close closes both the ClientConn and the owner exactly once, preventing socket leaks.
type ownedConn struct {
	watch.ClientConn[runapi.AgentRunEvent]
	owner io.Closer
	once  sync.Once
}

func (o *ownedConn) Close() error {
	o.once.Do(func() {
		_ = o.ClientConn.Close()
		_ = o.owner.Close()
	})
	return nil
}

// NewDialFunc returns a watch.DialFunc[AgentRunEvent] that dials the given socket
// and calls WatchEventConn(ctx, fromSeq). The returned ClientConn owns the underlying
// *Client and closes it when Close is called, preventing socket leaks.
func NewDialFunc(socketPath string) watch.DialFunc[runapi.AgentRunEvent] {
	return func(ctx context.Context, fromSeq int) (watch.ClientConn[runapi.AgentRunEvent], error) {
		c, err := Dial(ctx, socketPath)
		if err != nil {
			return nil, err
		}
		conn, err := c.WatchEventConn(ctx, fromSeq)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		return &ownedConn{ClientConn: conn, owner: c}, nil
	}
}
