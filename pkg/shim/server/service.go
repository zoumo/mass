package server

import (
	"context"
	"log/slog"

	acp "github.com/coder/acp-go-sdk"

	"github.com/zoumo/oar/api"
	apishim "github.com/zoumo/oar/api/shim"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/pkg/jsonrpc"
	"github.com/zoumo/oar/pkg/runtime"
)

// Service implements apishim.ShimService.
type Service struct {
	mgr     *runtime.Manager
	trans   *events.Translator
	logPath string
	logger  *slog.Logger
}

// New creates a new Service.
func New(mgr *runtime.Manager, trans *events.Translator, logPath string, logger *slog.Logger) *Service {
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
	// session/load is called by agentd during recovery to restore a prior ACP
	// session. The underlying runtime.Manager does not expose a Load method;
	// the shim handles session restoration internally via the ACP client.
	return nil
}

func (s *Service) Subscribe(ctx context.Context, req *apishim.SessionSubscribeParams) (*apishim.SessionSubscribeResult, error) {
	peer := jsonrpc.PeerFromContext(ctx)
	if peer == nil {
		return nil, jsonrpc.ErrInternal("no peer in context")
	}

	if req.AfterSeq != nil && *req.AfterSeq < 0 {
		return nil, jsonrpc.ErrInvalidParams("afterSeq must be >= 0")
	}
	if req.FromSeq != nil && *req.FromSeq < 0 {
		return nil, jsonrpc.ErrInvalidParams("fromSeq must be >= 0")
	}

	// Atomic path: read history + register live subscription under a single
	// lock hold to prevent gaps between the two operations.
	if req.FromSeq != nil {
		entries, ch, subID, nextSeq, err := s.trans.SubscribeFromSeq(s.logPath, *req.FromSeq)
		if err != nil {
			return nil, jsonrpc.ErrInternal(err.Error())
		}

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
					if err := peer.Notify(ctx, api.MethodShimEvent, ev); err != nil {
						return
					}
				}
			}
		}()

		return &apishim.SessionSubscribeResult{NextSeq: nextSeq, Entries: entries}, nil
	}

	// Legacy path: subscribe without atomic backfill; filter events by floor seq.
	ch, subID, nextSeq := s.trans.Subscribe()
	floor := nextSeq - 1
	if req.AfterSeq != nil {
		floor = *req.AfterSeq
	}

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
				if ev.Seq <= floor {
					continue
				}
				if err := peer.Notify(ctx, api.MethodShimEvent, ev); err != nil {
					return
				}
			}
		}
	}()

	return &apishim.SessionSubscribeResult{NextSeq: nextSeq}, nil
}

func (s *Service) Status(_ context.Context) (*apishim.RuntimeStatusResult, error) {
	st, err := s.mgr.GetState()
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	return &apishim.RuntimeStatusResult{
		State: st,
		Recovery: apishim.RuntimeStatusRecovery{
			LastSeq: s.trans.LastSeq(),
		},
	}, nil
}

func (s *Service) History(_ context.Context, req *apishim.RuntimeHistoryParams) (*apishim.RuntimeHistoryResult, error) {
	fromSeq := 0
	if req.FromSeq != nil {
		fromSeq = *req.FromSeq
	}
	if fromSeq < 0 {
		return nil, jsonrpc.ErrInvalidParams("fromSeq must be >= 0")
	}
	entries, err := events.ReadEventLog(s.logPath, fromSeq)
	if err != nil {
		return nil, jsonrpc.ErrInternal(err.Error())
	}
	if entries == nil {
		entries = []events.ShimEvent{}
	}
	return &apishim.RuntimeHistoryResult{Entries: entries}, nil
}

func (s *Service) Stop(_ context.Context) error {
	// Stop signals are handled by the transport layer (the caller detects the
	// reply and triggers Shutdown). The service layer itself has no lifecycle
	// state to clean up here.
	return nil
}
