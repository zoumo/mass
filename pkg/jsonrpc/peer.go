package jsonrpc

import (
	"context"

	"github.com/sourcegraph/jsonrpc2"
)

type contextKey int

const peerKey contextKey = 0

// Peer represents the remote side of a JSON-RPC connection.
// Injected into handler context by the framework for each request.
// Enables server-initiated notifications (e.g., agent-run event streaming).
type Peer struct {
	conn *jsonrpc2.Conn
}

// PeerFromContext extracts the Peer from a handler's context.
// Returns nil if no Peer is present (e.g., not in a handler context).
func PeerFromContext(ctx context.Context) *Peer {
	p, _ := ctx.Value(peerKey).(*Peer)
	return p
}

// Notify sends a notification to the remote peer.
// Writes are serialized by the underlying jsonrpc2.Conn.
func (p *Peer) Notify(ctx context.Context, method string, params any) error {
	return p.conn.Notify(ctx, method, params)
}

// DisconnectNotify returns a channel closed when the peer disconnects.
func (p *Peer) DisconnectNotify() <-chan struct{} {
	return p.conn.DisconnectNotify()
}

// Close closes the peer's underlying connection. This is used by server-side
// watch goroutines to force-disconnect a slow consumer (K8s-style eviction):
// the Translator closes the subscriber channel when its buffer is full, the
// Service goroutine detects this and calls peer.Close() to propagate the
// disconnect to the client, triggering reconnection with fromSeq.
func (p *Peer) Close() error {
	return p.conn.Close()
}

func newPeer(conn *jsonrpc2.Conn) *Peer {
	return &Peer{conn: conn}
}

func contextWithPeer(ctx context.Context, p *Peer) context.Context {
	return context.WithValue(ctx, peerKey, p)
}
