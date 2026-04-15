package server

import (
	"context"
	"log/slog"

	acp "github.com/coder/acp-go-sdk"

	acpruntime "github.com/zoumo/mass/pkg/shim/runtime/acp"
	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Service implements ShimService.
type Service struct {
	mgr     *acpruntime.Manager
	trans   *Translator
	logPath string
	logger  *slog.Logger
}

// New creates a new Service.
func New(mgr *acpruntime.Manager, trans *Translator, logPath string, logger *slog.Logger) *Service {
	return &Service{mgr: mgr, trans: trans, logPath: logPath, logger: logger}
}

func (s *Service) Prompt(ctx context.Context, req *apishim.SessionPromptParams) (*apishim.SessionPromptResult, error) {
	if req.Prompt == "" {
		return nil, jsonrpc.ErrInvalidParams("missing prompt")
	}
	s.trans.NotifyTurnStart()
	s.trans.NotifyUserPrompt(req.Prompt)
	resp, err := s.mgr.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(req.Prompt)})
	stopReason := "error"
	if err == nil {
		stopReason = string(resp.StopReason)
	}
	s.trans.NotifyTurnEnd(acp.StopReason(stopReason))
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	return &apishim.SessionPromptResult{StopReason: string(resp.StopReason)}, nil
}

func (s *Service) Cancel(ctx context.Context) error {
	if err := s.mgr.Cancel(ctx); err != nil {
		return jsonrpc.ErrInternal(err.Error())
	}
	return nil
}

func (s *Service) Load(_ context.Context, _ *apishim.SessionLoadParams) error {
	// session/load is called by mass during recovery to restore a prior ACP
	// session. The underlying acpruntime.Manager does not expose a Load method;
	// the shim handles session restoration internally via the ACP client.
	return nil
}

// WatchEvent implements session/watch_event (K8s List-Watch pattern).
// When FromSeq is nil, only live events are streamed.
// When FromSeq is set, historical events are replayed via shim/event
// notifications first, then live events follow — no large response payload.
func (s *Service) WatchEvent(ctx context.Context, req *apishim.SessionWatchEventParams) (*apishim.SessionWatchEventResult, error) {
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
func (s *Service) watchLiveOnly(ctx context.Context, peer *jsonrpc.Peer) (*apishim.SessionWatchEventResult, error) {
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
					return
				}
				if err := peer.Notify(ctx, apishim.MethodShimEvent, ev); err != nil {
					return
				}
			}
		}
	}()

	return &apishim.SessionWatchEventResult{NextSeq: nextSeq}, nil
}

// watchWithReplay implements two-phase lockless replay:
//
// Phase 1 (under Translator mutex, O(1)): register subscriber channel.
// Phase 2 (background goroutine, NO mutex): read event log, stream history
// events via shim/event notifications, then switch to live events.
//
// Dedup guarantee: history events with seq < nextSeq are sent from file,
// live events with seq < nextSeq are skipped. broadcast() does log-before-fanout
// under the same mutex, so the file contains all seq < nextSeq at subscribe time.
func (s *Service) watchWithReplay(ctx context.Context, peer *jsonrpc.Peer, fromSeq int) (*apishim.SessionWatchEventResult, error) {
	// Phase 1: register subscriber under mutex (O(1)).
	ch, subID, nextSeq := s.trans.Subscribe()

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
			if err := peer.Notify(ctx, apishim.MethodShimEvent, entry); err != nil {
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
					return
				}
				if ev.Seq < nextSeq {
					continue // dedup: already sent from file
				}
				if err := peer.Notify(ctx, apishim.MethodShimEvent, ev); err != nil {
					return
				}
			}
		}
	}()

	return &apishim.SessionWatchEventResult{NextSeq: nextSeq}, nil
}

func (s *Service) Status(_ context.Context) (*apishim.RuntimeStatusResult, error) {
	st, err := s.mgr.GetState()
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	// Overlay real-time in-memory event counts from the Translator onto the
	// state read from disk — the file value is stale between state writes.
	st.EventCounts = s.trans.EventCounts()
	return &apishim.RuntimeStatusResult{
		State: st,
		Recovery: apishim.RuntimeStatusRecovery{
			LastSeq: s.trans.LastSeq(),
		},
	}, nil
}


func (s *Service) Stop(_ context.Context) error {
	// Stop signals are handled by the transport layer (the caller detects the
	// reply and triggers Shutdown). The service layer itself has no lifecycle
	// state to clean up here.
	return nil
}
