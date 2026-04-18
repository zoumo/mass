package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	acpruntime "github.com/zoumo/mass/pkg/agentrun/runtime/acp"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// watchIDCounter is a process-global monotonic counter for generating unique
// watch stream identifiers. Each session/watch_event call gets a unique watchID
// so that a single connection can carry multiple independent watch streams.
var watchIDCounter atomic.Int64

// nextWatchID returns a unique watch stream identifier (e.g. "w1", "w2", ...).
func nextWatchID() string {
	return fmt.Sprintf("w%d", watchIDCounter.Add(1))
}

// Service implements Handler.
type Service struct {
	mgr     *acpruntime.Manager
	trans   *Translator
	logPath string
	logger  *slog.Logger
}

// New creates a new Service.
func New(mgr *acpruntime.Manager, trans *Translator, logPath string, logger *slog.Logger) *Service {
	return &Service{mgr: mgr, trans: trans, logPath: logPath, logger: logger.With("subsystem", "service")}
}

func (s *Service) Prompt(ctx context.Context, req *runapi.SessionPromptParams) (*runapi.SessionPromptResult, error) {
	if len(req.Prompt) == 0 {
		return nil, jsonrpc.ErrInvalidParams("missing prompt")
	}
	s.logger.Debug("prompt", "blocks", len(req.Prompt))
	s.trans.NotifyTurnStart()
	s.trans.NotifyUserPrompt(req.Prompt)
	resp, err := s.mgr.Prompt(ctx, req.Prompt)
	stopReason := "error"
	if err == nil {
		stopReason = string(resp.StopReason)
	}
	if err != nil {
		s.trans.NotifyError(err.Error())
	}
	s.trans.NotifyTurnEnd(acp.StopReason(stopReason))
	s.logger.Debug("prompt done", "stopReason", stopReason)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	return &runapi.SessionPromptResult{StopReason: string(resp.StopReason)}, nil
}

func (s *Service) Cancel(ctx context.Context) error {
	s.logger.Debug("cancel")
	if err := s.mgr.Cancel(ctx); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

func (s *Service) Load(_ context.Context, _ *runapi.SessionLoadParams) error {
	// session/load is called by mass during recovery to restore a prior ACP
	// session. The underlying acpruntime.Manager does not expose a Load method;
	// the agent-run handles session restoration internally via the ACP client.
	return nil
}

// WatchEvent implements runtime/watch_event (K8s List-Watch pattern).
// When FromSeq is nil, only live events are streamed.
// When FromSeq is set, historical events are replayed via runtime/event_update
// notifications first, then live events follow — no large response payload.
func (s *Service) WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams) (*runapi.SessionWatchEventResult, error) {
	s.logger.Debug("watch_event", "fromSeq", req.FromSeq)
	peer := jsonrpc.PeerFromContext(ctx)
	if peer == nil {
		return nil, jsonrpc.ErrInternal("no peer in context")
	}

	if req.FromSeq != nil && *req.FromSeq < 0 {
		return nil, jsonrpc.ErrInvalidParams("fromSeq must be >= 0")
	}

	if req.FromSeq != nil {
		return s.watchWithReplay(ctx, peer, *req.FromSeq)
	}
	return s.watchLiveOnly(ctx, peer)
}

// watchLiveOnly subscribes to live events only (no replay).
// Each watch stream gets a unique watchID set on every notification, allowing
// the client to demux when multiple watch streams share one connection.
//
// Slow consumer handling (K8s-style eviction):
// When the Translator's subscriber channel buffer fills up, Translator.broadcast()
// closes the channel and removes the subscriber. This goroutine detects the
// channel close (ok=false), calls peer.Close() to force-disconnect the client,
// which triggers the client to reconnect with fromSeq=lastReceivedSeq+1.
func (s *Service) watchLiveOnly(ctx context.Context, peer *jsonrpc.Peer) (*runapi.SessionWatchEventResult, error) {
	ch, subID, nextSeq := s.trans.Subscribe()
	watchID := nextWatchID()

	go func() {
		defer s.trans.Unsubscribe(subID)
		disconnect := peer.DisconnectNotify()
		for {
			select {
			case <-disconnect:
				return
			case ev, ok := <-ch:
				if !ok {
					// Subscriber evicted by Translator (channel full, K8s-style).
					// Close the peer connection to propagate disconnect to client,
					// triggering re-dial + WatchEvent(fromSeq=lastSeq+1).
					s.logger.Warn("subscriber evicted by translator, closing peer",
						"watchID", watchID, "reason", "channel_full")
					peer.Close()
					return
				}
				// Stamp watchID on each event for client-side demux.
				ev.WatchID = watchID
				if err := peer.Notify(ctx, runapi.MethodRuntimeEventUpdate, ev); err != nil {
					return
				}
			}
		}
	}()

	return &runapi.SessionWatchEventResult{WatchID: watchID, NextSeq: nextSeq}, nil
}

// watchWithReplay implements two-phase lockless replay:
//
// Phase 1 (under Translator mutex, O(1)): register subscriber channel.
// Phase 2 (background goroutine, NO mutex): read event log, stream history
// events via runtime/event_update notifications, then switch to live events.
//
// Dedup guarantee: history events with seq < nextSeq are sent from file,
// live events with seq < nextSeq are skipped. broadcast() does log-before-fanout
// under the same mutex, so the file contains all seq < nextSeq at subscribe time.
//
// Each event is stamped with watchID for client-side demux (same as watchLiveOnly).
// Replay events also carry watchID so the client can filter consistently.
//
// Slow consumer handling: same as watchLiveOnly — Translator eviction triggers
// peer.Close() to propagate disconnect for client-side reconnection.
func (s *Service) watchWithReplay(ctx context.Context, peer *jsonrpc.Peer, fromSeq int) (*runapi.SessionWatchEventResult, error) {
	// Phase 1: register subscriber under mutex (O(1)).
	ch, subID, nextSeq := s.trans.Subscribe()
	watchID := nextWatchID()

	// Phase 2: replay + live in background goroutine.
	go func() {
		defer s.trans.Unsubscribe(subID)
		disconnect := peer.DisconnectNotify()

		// Replay historical events from event log (no mutex held).
		entries, err := ReadEventLog(s.logPath, fromSeq)
		if err != nil {
			s.logger.Error("watch_event: replay read failed", "fromSeq", fromSeq, "error", err)
			return
		}
		for _, entry := range entries {
			if entry.Seq >= nextSeq {
				break // remaining entries will come from live channel
			}
			select {
			case <-disconnect:
				return
			default:
			}
			// Stamp watchID on replay events for consistent client-side filtering.
			entry.WatchID = watchID
			if err := peer.Notify(ctx, runapi.MethodRuntimeEventUpdate, entry); err != nil {
				return
			}
		}

		// Switch to live events, dedup overlap.
		for {
			select {
			case <-disconnect:
				return
			case ev, ok := <-ch:
				if !ok {
					// Subscriber evicted by Translator (channel full, K8s-style).
					s.logger.Warn("subscriber evicted by translator, closing peer",
						"watchID", watchID, "reason", "channel_full")
					peer.Close()
					return
				}
				if ev.Seq < nextSeq {
					continue // dedup: already sent from file
				}
				// Stamp watchID on each live event for client-side demux.
				ev.WatchID = watchID
				if err := peer.Notify(ctx, runapi.MethodRuntimeEventUpdate, ev); err != nil {
					return
				}
			}
		}
	}()

	return &runapi.SessionWatchEventResult{WatchID: watchID, NextSeq: nextSeq}, nil
}

func (s *Service) Status(_ context.Context) (*runapi.RuntimeStatusResult, error) {
	st, err := s.mgr.GetState()
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	// Overlay real-time in-memory event counts from the Translator onto the
	// state read from disk — the file value is stale between state writes.
	st.EventCounts = s.trans.EventCounts()
	return &runapi.RuntimeStatusResult{
		State: st,
		Recovery: runapi.RuntimeStatusRecovery{
			LastSeq: s.trans.LastSeq(),
		},
	}, nil
}

func (s *Service) SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error) {
	s.logger.Debug("set_model", "modelID", req.ModelID)
	if req.ModelID == "" {
		return nil, jsonrpc.ErrInvalidParams("missing modelId")
	}
	if err := s.mgr.SetModel(ctx, req.ModelID); err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	return &runapi.SessionSetModelResult{}, nil
}

func (s *Service) Stop(_ context.Context) error {
	s.logger.Debug("stop")
	// Stop signals are handled by the transport layer (the caller detects the
	// reply and triggers Shutdown). The service layer itself has no lifecycle
	// state to clean up here.
	return nil
}
