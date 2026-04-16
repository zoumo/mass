package server

import (
	"context"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Handler defines the Agent Run JSON-RPC protocol methods.
// These are the methods exposed by agent-run over a Unix socket.
type Handler interface {
	Prompt(ctx context.Context, req *runapi.SessionPromptParams) (*runapi.SessionPromptResult, error)
	Cancel(ctx context.Context) error
	Load(ctx context.Context, req *runapi.SessionLoadParams) error
	// WatchEvent implements K8s List-Watch style event subscription.
	// When FromSeq is nil, only live events are streamed.
	// When FromSeq is set, historical events are replayed via runtime/event_update
	// notifications first (two-phase lockless replay), then live events follow.
	//
	// The handler uses jsonrpc.PeerFromContext(ctx) to obtain the Peer:
	//   - peer.Notify(ctx, "runtime/event_update", event) for replay + live events
	//   - <-peer.DisconnectNotify() to detect client disconnect and unsubscribe
	//
	// Channel overflow: slow subscribers are evicted (channel closed + removed).
	// Clients reconnect with fromSeq=lastReceivedSeq+1 to resume.
	WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams) (*runapi.SessionWatchEventResult, error)
	SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error)
	Status(ctx context.Context) (*runapi.RuntimeStatusResult, error)
	Stop(ctx context.Context) error
}

// Register registers a Handler implementation with the server.
func Register(s *jsonrpc.Server, svc Handler) {
	s.RegisterService("session", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req runapi.SessionPromptParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Prompt(ctx, &req)
			},
			"cancel": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, svc.Cancel(ctx)
			},
			"load": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req runapi.SessionLoadParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Load(ctx, &req)
			},
			"set_model": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req runapi.SessionSetModelParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.SetModel(ctx, &req)
			},
		},
	})
	s.RegisterService("runtime", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"watch_event": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req runapi.SessionWatchEventParams
				// params are optional for watch_event
				if err := unmarshal(&req); err != nil {
					// ignore unmarshal errors for missing params
					req = runapi.SessionWatchEventParams{}
				}
				return svc.WatchEvent(ctx, &req)
			},
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.Status(ctx)
			},
			"stop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, svc.Stop(ctx)
			},
		},
	})
}
