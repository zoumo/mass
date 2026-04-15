package server

import (
	"context"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ShimService defines the Shim JSON-RPC protocol methods.
// These are the methods exposed by agent-shim over a Unix socket.
type ShimService interface {
	Prompt(ctx context.Context, req *apishim.SessionPromptParams) (*apishim.SessionPromptResult, error)
	Cancel(ctx context.Context) error
	Load(ctx context.Context, req *apishim.SessionLoadParams) error
	// Subscribe registers for live event streaming. The handler uses
	// jsonrpc.PeerFromContext(ctx) to obtain the Peer and send notifications:
	//   - peer.Notify(ctx, api.MethodShimEvent, event) for each live event
	//   - <-peer.DisconnectNotify() to detect client disconnect and unsubscribe
	//
	// Implementation constraints:
	// 1. Atomic backfill: SubscribeFromSeq reads history + registers live subscription
	//    under a single lock to prevent gaps.
	// 2. Legacy afterSeq: subscription registered first, then events with seq <= afterSeq
	//    are filtered.
	// 3. Event push: via peer.Notify(ctx, "shim/event", shimEvent) — serialized with responses.
	// 4. Disconnect unsubscribe: <-peer.DisconnectNotify() triggers cleanup + goroutine exit.
	// 5. Slow client: peer.Notify returning error → unsubscribe + exit.
	Subscribe(ctx context.Context, req *apishim.SessionSubscribeParams) (*apishim.SessionSubscribeResult, error)
	Status(ctx context.Context) (*apishim.RuntimeStatusResult, error)
	History(ctx context.Context, req *apishim.RuntimeHistoryParams) (*apishim.RuntimeHistoryResult, error)
	Stop(ctx context.Context) error
}

// RegisterShimService registers a ShimService implementation with the server.
func RegisterShimService(s *jsonrpc.Server, svc ShimService) {
	s.RegisterService("session", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req apishim.SessionPromptParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Prompt(ctx, &req)
			},
			"cancel": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, svc.Cancel(ctx)
			},
			"load": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req apishim.SessionLoadParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Load(ctx, &req)
			},
			"subscribe": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req apishim.SessionSubscribeParams
				// params are optional for subscribe
				if err := unmarshal(&req); err != nil {
					// ignore unmarshal errors for missing params
					req = apishim.SessionSubscribeParams{}
				}
				return svc.Subscribe(ctx, &req)
			},
		},
	})
	s.RegisterService("runtime", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.Status(ctx)
			},
			"history": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req apishim.RuntimeHistoryParams
				if err := unmarshal(&req); err != nil {
					req = apishim.RuntimeHistoryParams{}
				}
				return svc.History(ctx, &req)
			},
			"stop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, svc.Stop(ctx)
			},
		},
	})
}
