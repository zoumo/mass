package server

import (
	"context"
	"log/slog"

	acp "github.com/coder/acp-go-sdk"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	acpruntime "github.com/zoumo/mass/pkg/agentrun/runtime/acp"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Service implements Handler.
type Service struct {
	mgr    *acpruntime.Manager
	trans  *Translator
	logger *slog.Logger
}

// New creates a new Service.
func New(mgr *acpruntime.Manager, trans *Translator, logger *slog.Logger) *Service {
	return &Service{mgr: mgr, trans: trans, logger: logger.With("subsystem", "service")}
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

func (s *Service) Cancel(ctx context.Context) (retErr error) {
	s.logger.Debug("cancel")
	defer func() {
		s.trans.NotifyOperationAudit("cancel", nil, retErr)
	}()
	if err := s.mgr.Cancel(ctx); err != nil {
		retErr = jsonrpc.ErrInternal(err.Error())
		return retErr
	}
	return nil
}

func (s *Service) Load(_ context.Context, req *runapi.SessionLoadParams) error {
	// session/load is called by agentd during recovery to restore a prior ACP
	// session. Agent-run checks the ACP loadSession capability and auto-fallbacks.
	// Always returns nil (non-fatal) — agentd does not need to distinguish
	// "restored" vs "fresh session".
	if req.SessionID == "" {
		s.logger.Info("session/load: no sessionId provided, skipping")
		return nil
	}

	st, err := s.mgr.GetState()
	if err != nil {
		s.logger.Info("session/load: could not read state, skipping", "error", err)
		return nil
	}

	if st.Session == nil || st.Session.Capabilities == nil || !st.Session.Capabilities.LoadSession {
		s.logger.Info("session/load: ACP agent does not support loadSession, skipping",
			"sessionId", req.SessionID)
		return nil
	}

	// ACP agent supports loadSession but the SDK does not yet expose the method.
	// Log and return success — session recovery will work once the SDK catches up.
	s.logger.Info("session/load: ACP loadSession capability detected but SDK method not yet available",
		"sessionId", req.SessionID)
	return nil
}

// WatchEvent implements runtime/watch_event (K8s List-Watch pattern).
// When FromSeq is nil, only live events are streamed.
// When FromSeq is set, historical events are replayed via runtime/event_update
// notifications first, then live events follow — no large response payload.
func (s *Service) WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams, watchID string) (*runapi.SessionWatchEventResult, error) {
	s.logger.Debug("watch_event", "watchID", watchID, "fromSeq", req.FromSeq)
	peer := jsonrpc.PeerFromContext(ctx)
	if peer == nil {
		return nil, jsonrpc.ErrInternal("no peer in context")
	}

	if watchID == "" {
		return nil, jsonrpc.ErrInvalidParams("watchId is required")
	}

	if req.FromSeq != nil && *req.FromSeq < 0 {
		return nil, jsonrpc.ErrInvalidParams("fromSeq must be >= 0")
	}

	if req.FromSeq != nil {
		return s.watchWithReplay(ctx, peer, watchID, *req.FromSeq)
	}
	return s.watchLiveOnly(ctx, peer, watchID)
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
func (s *Service) watchLiveOnly(ctx context.Context, peer *jsonrpc.Peer, watchID string) (*runapi.SessionWatchEventResult, error) {
	ch, subID, nextSeq := s.trans.Subscribe()

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
func (s *Service) watchWithReplay(ctx context.Context, peer *jsonrpc.Peer, watchID string, fromSeq int) (*runapi.SessionWatchEventResult, error) {
	// Phase 1: register subscriber under mutex (O(1)).
	ch, subID, nextSeq := s.trans.Subscribe()

	// Phase 2: replay + live in background goroutine.
	go func() {
		defer s.trans.Unsubscribe(subID)
		disconnect := peer.DisconnectNotify()

		// Replay historical events from the current session's event log (no mutex held).
		entries, err := s.trans.ReadCurrentSessionLog(fromSeq)
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

func (s *Service) SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (_ *runapi.SessionSetModelResult, retErr error) {
	s.logger.Debug("set_model", "modelID", req.ModelID)
	defer func() {
		s.trans.NotifyOperationAudit("set_model", map[string]string{"modelId": req.ModelID}, retErr)
	}()
	if req.ModelID == "" {
		retErr = jsonrpc.ErrInvalidParams("missing modelId")
		return nil, retErr
	}
	if err := s.mgr.SetModel(ctx, req.ModelID); err != nil {
		retErr = jsonrpc.ErrInternal(err.Error())
		return nil, retErr
	}
	return &runapi.SessionSetModelResult{}, nil
}

func (s *Service) Stop(_ context.Context) error {
	s.logger.Debug("stop")
	s.trans.NotifyOperationAudit("stop", nil, nil)
	return nil
}
