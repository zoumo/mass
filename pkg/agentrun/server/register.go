package server

import (
	"context"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// watchEventWire carries both transport-level (watchId) and business (fromSeq) fields.
type watchEventWire struct {
	WatchID string `json:"watchId"`
	FromSeq *int   `json:"fromSeq,omitempty"`
}

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
	WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams, watchID string) (*runapi.SessionWatchEventResult, error)
	SetModel(ctx context.Context, req *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error)
	Status(ctx context.Context) (*runapi.RuntimePhaseResult, error)
	Stop(ctx context.Context) error
}

// Register registers a Handler implementation with the server.
func Register(s *jsonrpc.Server, svc Handler) {
	s.RegisterService("session", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"prompt":    jsonrpc.UnaryMethod(svc.Prompt),
			"cancel":    jsonrpc.NullaryCommand(svc.Cancel),
			"load":      jsonrpc.UnaryCommand(svc.Load),
			"set_model": jsonrpc.UnaryMethod(svc.SetModel),
		},
	})
	s.RegisterService("runtime", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			// watch_event needs watchId extraction — keep hand-written.
			"watch_event": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var wire watchEventWire
				_ = unmarshal(&wire) // params optional — zero value is valid
				req := &runapi.SessionWatchEventParams{FromSeq: wire.FromSeq}
				return svc.WatchEvent(ctx, req, wire.WatchID)
			},
			"status": jsonrpc.NullaryMethod(svc.Status),
			"stop":   jsonrpc.NullaryCommand(svc.Stop),
		},
	})
}
