package client

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/watch"
)

// typedWatcher wraps a jsonrpc.WatchStream and delivers typed AgentRunEvent values.
// Implements watch.Interface[runapi.AgentRunEvent].
type typedWatcher struct {
	ws   *jsonrpc.WatchStream
	ch   chan runapi.AgentRunEvent
	once sync.Once
}

func newTypedWatcher(ws *jsonrpc.WatchStream) *typedWatcher {
	tw := &typedWatcher{
		ws: ws,
		ch: make(chan runapi.AgentRunEvent, cap(ws.ResultChan())),
	}
	go tw.pump()
	return tw
}

func (tw *typedWatcher) pump() {
	defer close(tw.ch)
	for {
		select {
		case wev, ok := <-tw.ws.ResultChan():
			if !ok {
				return // channel closed by notifWorker (evict or disconnect)
			}
			var ev runapi.AgentRunEvent
			if err := json.Unmarshal(wev.Payload, &ev); err != nil {
				slog.Debug("typedWatcher: unmarshal payload failed", "error", err)
				continue
			}
			select {
			case tw.ch <- ev:
			case <-tw.ws.Done():
				return
			}
		case <-tw.ws.Done():
			return // Stop() was called
		}
	}
}

func (tw *typedWatcher) ResultChan() <-chan runapi.AgentRunEvent { return tw.ch }

func (tw *typedWatcher) Stop() {
	tw.once.Do(func() { tw.ws.Stop() })
}

// ownedWatcher wraps a watch.Interface and an io.Closer (the underlying Client).
// Stop closes both the Interface and the owner.
type ownedWatcher struct {
	watch.Interface[runapi.AgentRunEvent]
	owner io.Closer
	once  sync.Once
}

func (o *ownedWatcher) Stop() {
	o.once.Do(func() {
		o.Interface.Stop()
		_ = o.owner.Close()
	})
}

// NewWatchFunc returns a WatchFunc for use with watch.NewRetryWatcher.
// Each call Dials a new Client, calls WatchEvent, and wraps the result so
// that Stop closes both the watcher and the underlying Client connection.
func NewWatchFunc(socketPath string) watch.WatchFunc[runapi.AgentRunEvent] {
	return func(ctx context.Context, fromSeq int) (watch.Interface[runapi.AgentRunEvent], error) {
		c, err := Dial(ctx, socketPath)
		if err != nil {
			return nil, err
		}
		req := &runapi.SessionWatchEventParams{}
		if fromSeq >= 0 {
			req.FromSeq = &fromSeq
		}
		w, err := c.WatchEvent(ctx, req)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		return &ownedWatcher{Interface: w, owner: c}, nil
	}
}
