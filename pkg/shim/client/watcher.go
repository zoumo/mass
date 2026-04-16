// Package client — Watcher implements K8s-style watch semantics for shim events.
//
// Usage pattern (mirrors k8s.io/apimachinery/pkg/watch.Interface):
//
//	watcher, err := client.WatchEvent(ctx, &shimapi.SessionWatchEventParams{FromSeq: &fromSeq})
//	if err != nil { ... }
//	defer watcher.Stop()
//
//	for ev := range watcher.ResultChan() {
//	    // process ev
//	}
//	// Channel closed → connection lost or Stop() called
//
// Slow consumer lifecycle:
//  1. Translator.broadcast() detects subscriber channel full → close(ch) + evict
//  2. Service goroutine detects ch closed → peer.Close() (force disconnect)
//  3. jsonrpc.Conn closes → Client.notifWorker exits → closes all Subscribe channels
//  4. Watcher's filter goroutine detects notifCh closed → closes ResultChan
//  5. Consumer's range loop exits → reconnect with Dial() + WatchEvent(fromSeq=lastSeq+1)
package client

import (
	"encoding/json"
	"log/slog"
	"sync"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Watcher delivers events from a runtime/watch_event subscription.
// It filters raw JSON-RPC notifications by watchID and deserializes them into
// typed AgentRunEvent values. Modeled after k8s.io/apimachinery/pkg/watch.Interface.
type Watcher struct {
	// ch is the typed event channel exposed to consumers via ResultChan().
	ch chan apishim.AgentRunEvent

	// watchID is the server-assigned identifier for this watch stream.
	// Used to demux notifications when multiple watchers share one connection.
	watchID string

	// nextSeq is the sequence number boundary at subscription time (diagnostic).
	nextSeq int

	// stopOnce ensures Stop() is idempotent.
	stopOnce sync.Once

	// unsub is the jsonrpc.Client.Subscribe() unsubscribe function.
	// Calling it removes the per-method subscription and closes the notification
	// channel, which causes the filter goroutine to exit and close ResultChan.
	unsub func()
}

// ResultChan returns the channel that delivers typed AgentRunEvent values.
// The channel is closed when the connection drops, the watcher is stopped,
// or the server evicts this subscriber (slow consumer).
func (w *Watcher) ResultChan() <-chan apishim.AgentRunEvent {
	return w.ch
}

// NextSeq returns the sequence number boundary at subscription establishment.
// This is for diagnostics — reconnection should use the last received event's
// seq+1, not this value.
func (w *Watcher) NextSeq() int {
	return w.nextSeq
}

// WatchID returns the server-assigned watch stream identifier.
func (w *Watcher) WatchID() string {
	return w.watchID
}

// Stop terminates the watch stream and closes ResultChan.
// Safe to call multiple times (idempotent). After Stop(), ResultChan() will
// be closed and any pending events drained.
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		w.unsub() // unsubscribe from jsonrpc notifications → closes notifCh → filter goroutine exits → closes w.ch
	})
}

// newWatcher creates a Watcher that filters raw JSON-RPC notifications by
// watchID, deserializes them, and delivers typed events to ResultChan().
//
// A background goroutine reads from notifCh (the jsonrpc.Client.Subscribe channel),
// discards notifications that don't match this watcher's watchID, and forwards
// matching events to the result channel. When notifCh closes (connection lost
// or unsubscribe), the goroutine closes the result channel and exits.
func newWatcher(watchID string, nextSeq int, notifCh <-chan jsonrpc.NotificationMsg, unsub func()) *Watcher {
	// Result channel buffer: sized to absorb short bursts without blocking
	// the filter goroutine. If the consumer is truly slow, the upstream
	// Translator eviction (buffer=1024) fires first and tears down the connection.
	eventCh := make(chan apishim.AgentRunEvent, 256)

	w := &Watcher{
		ch:      eventCh,
		watchID: watchID,
		nextSeq: nextSeq,
		unsub:   unsub,
	}

	// Background goroutine: deserialize + watchID filter + deliver.
	go func() {
		defer close(eventCh)
		for msg := range notifCh {
			var ev apishim.AgentRunEvent
			if err := json.Unmarshal(msg.Params, &ev); err != nil {
				slog.Debug("watcher: unmarshal notification failed", "error", err)
				continue
			}
			// Filter by watchID: only deliver events from our watch stream.
			// This allows multiple watchers to coexist on a single connection.
			if ev.WatchID != watchID {
				continue
			}
			select {
			case eventCh <- ev:
			default:
				// Consumer is slow — drop event. The upstream Translator's
				// slow-subscriber eviction will close the connection soon,
				// triggering reconnection with fromSeq.
				slog.Debug("watcher: result channel full, event dropped",
					"watchID", watchID, "seq", ev.Seq)
			}
		}
	}()

	return w
}
