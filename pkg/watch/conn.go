package watch

import "context"

// ClientConn represents a single watch connection to a server.
// Implementations wrap the transport (e.g. JSON-RPC, HTTP streaming).
type ClientConn[T any] interface {
	// Recv blocks until the next Event arrives or an error occurs.
	// On error (including EOF/disconnect), the caller must call Close.
	Recv() (Event[T], error)
	Close() error
}

// DialFunc establishes a new connection starting at fromSeq.
// fromSeq is the value passed directly to the server (cursor+1 of the last
// enqueued event, or 0 for full replay from the beginning).
type DialFunc[T any] func(ctx context.Context, fromSeq int) (ClientConn[T], error)
