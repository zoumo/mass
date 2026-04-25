package jsonrpc

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

// WatchEvent is the standard envelope for watch notifications.
// jsonrpc routes by WatchID; business code unmarshals Payload.
type WatchEvent struct {
	WatchID string          `json:"watchId"`
	Seq     int             `json:"seq"`
	Payload json.RawMessage `json:"payload"`
}

// WatchStream delivers events for a single watch stream identified by watchID.
// Created by Client.Watch or Client.AddWatch, automatically receives notifications
// routed by the client's notifWorker.
//
// Stop is idempotent and concurrent-safe.
//
// Channel ownership: ws.ch is only closed by notifWorker (via eviction or
// connection shutdown). Stop() does NOT close ch — it closes done to signal
// the pump goroutine to exit. This avoids a send-to-closed-channel panic
// race between Stop() (consumer goroutine) and routeWatchEvent (notifWorker
// goroutine).
type WatchStream struct {
	watchID     string
	ch          chan WatchEvent
	done        chan struct{}
	client      *Client
	stopOnce    sync.Once
	closeChOnce sync.Once
}

func (ws *WatchStream) ResultChan() <-chan WatchEvent { return ws.ch }

func (ws *WatchStream) Done() <-chan struct{} { return ws.done }

func (ws *WatchStream) Stop() {
	ws.stopOnce.Do(func() {
		close(ws.done)
		ws.client.removeWatch(ws.watchID, ws)
	})
}

// closeCh closes the result channel exactly once.
// Only called by notifWorker (eviction or connection shutdown).
func (ws *WatchStream) closeCh() {
	ws.closeChOnce.Do(func() {
		close(ws.ch)
	})
}

// Watch sends an RPC and returns a WatchStream that receives server-push
// notifications routed by watchID.
//
// Protocol:
//  1. Generate a UUID watchID.
//  2. Register a WatchStream (listener ready BEFORE the RPC is sent).
//  3. Inject watchId into the RPC params JSON.
//  4. Send the RPC.
//  5. Return the WatchStream.
//
// Callers never see or set watchId — it is a transport-level concern.
func (c *Client) Watch(ctx context.Context, method string, params, result any, bufSize int) (*WatchStream, error) {
	watchID := uuid.New().String()

	// Register listener BEFORE sending RPC.
	ws := c.AddWatch(watchID, bufSize)

	// Inject watchId into params.
	enriched, err := injectWatchID(params, watchID)
	if err != nil {
		ws.Stop()
		return nil, err
	}

	if err := c.Call(ctx, method, enriched, result); err != nil {
		ws.Stop()
		return nil, err
	}
	return ws, nil
}

// injectWatchID merges {"watchId": id} into the JSON representation of params.
func injectWatchID(params any, watchID string) (any, error) {
	if params == nil {
		return map[string]string{"watchId": watchID}, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	wid, _ := json.Marshal(watchID)
	m["watchId"] = wid
	return m, nil
}
