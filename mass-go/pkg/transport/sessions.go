package transport

import (
	"context"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"reflect"
)

type HistoryQuery struct {
	After string   `query:"after"`
	Limit int      `query:"limit" minimum:"1" default:"100"`
	Types []string `query:"types,explode" nullable:"false" minItems:"1" enum:"user.message,agent.message,agent.thinking,agent.tool_call,agent.tool_result,agent.message_chunk,agent.thinking_chunk,agent.tool_call_chunk,agent.plan,session.state_update,session.error,session.info_update,session.config_option_update,session.available_commands_update,session.usage_update,session.interaction_requested,session.interaction_closed"`
}
type historyByID struct {
	SessionIDPath
	HistoryQuery
}
type historyByNameOrID struct {
	SessionNameOrIDPath
	HistoryQuery
}
type historyOutput struct{ Body store.HistoryPage }
type streamByID struct {
	SessionIDPath
	LastEventID string `header:"Last-Event-ID"`
}
type streamByNameOrID struct {
	SessionNameOrIDPath
	LastEventID string `header:"Last-Event-ID"`
}

func (q HistoryQuery) query() (store.HistoryQuery, error) {
	for _, kind := range q.Types {
		if !events.ValidType(kind) {
			return store.HistoryQuery{}, Invalid()
		}
	}
	return store.HistoryQuery{After: q.After, Limit: q.Limit, Types: q.Types}, nil
}
func (s *Server) registerSessions(api huma.API) {
	s.registerSessionIdentity(api)
	s.registerSessionWork(api)
	s.registerSessionRecovery(api)
	registerEventSchemas(api.OpenAPI().Components.Schemas)
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/sessions/{sessionId}/events", OperationID: "listHistoryByID"}, func(_ context.Context, in *historyByID) (*historyOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.history(ref, in.HistoryQuery)
	})
	register(api, huma.Operation{Method: "GET", Path: "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/events", OperationID: "listHistoryByNameOrID"}, func(_ context.Context, in *historyByNameOrID) (*historyOutput, error) {
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.history(ref, in.HistoryQuery)
	})
	register(api, streamOperation(api.OpenAPI().Components.Schemas, "/apis/v1/sessions/{sessionId}/events/stream", "observeSessionByID"), func(_ context.Context, in *streamByID) (*huma.StreamResponse, error) {
		if in.LastEventID != "" {
			return nil, Invalid()
		}
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.stream(ref)
	})
	register(api, streamOperation(api.OpenAPI().Components.Schemas, "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}/events/stream", "observeSessionByNameOrID"), func(_ context.Context, in *streamByNameOrID) (*huma.StreamResponse, error) {
		if in.LastEventID != "" {
			return nil, Invalid()
		}
		ref, err := in.resolve(s)
		if err != nil {
			return nil, err
		}
		return s.stream(ref)
	})
}
func (s *Server) history(ref resource.SessionRef, q HistoryQuery) (*historyOutput, error) {
	query, err := q.query()
	if err != nil {
		return nil, err
	}
	page, err := s.api.ListHistoryEvents(ref, query)
	return &historyOutput{Body: page}, err
}
func (s *Server) stream(ref resource.SessionRef) (*huma.StreamResponse, error) {
	sub, err := s.api.SubscribeEvents(ref)
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(ctx huma.Context) { r, w := humachi.Unwrap(ctx); s.observe(w, r, sub) }}, nil
}
func streamOperation(r huma.Registry, path, id string) huma.Operation {
	return huma.Operation{Method: "GET", Path: path, OperationID: id, Responses: map[string]*huma.Response{
		"200": {Description: "Live SSE only. Frames: subscription.ready {}, session.event SessionEvent, subscription.closed {}, subscription.error Error; comment heartbeat at least every 15 seconds. No id, retry, replay or automatic reconnect. EOF without a terminal frame is abnormal.", Content: map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}, Extensions: map[string]any{"x-sse-events": map[string]any{
			"subscription.ready":  map[string]any{"type": "object", "additionalProperties": false},
			"session.event":       r.Schema(reflect.TypeFor[events.SessionEvent](), true, ""),
			"subscription.closed": map[string]any{"type": "object", "additionalProperties": false},
			"subscription.error":  r.Schema(reflect.TypeFor[Error](), true, ""),
		}}}}}}}
}
