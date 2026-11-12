package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type sessionRefRPCParams struct {
	Session json.RawMessage `json:"session"`
}
type sessionCreateRPCParams struct {
	Input json.RawMessage `json:"input"`
}
type sessionCreateRPCInput struct {
	Metadata     json.RawMessage       `json:"metadata"`
	Spec         *resource.SessionSpec `json:"spec"`
	InitialInput json.RawMessage       `json:"initialInput"`
}
type sessionMetadataRPC struct {
	Namespace json.RawMessage `json:"namespace"`
	Name      json.RawMessage `json:"name"`
}
type sessionResumeRPCParams struct {
	Session json.RawMessage `json:"session"`
	Input   json.RawMessage `json:"input"`
}
type sessionResumeRPCInput struct {
	InitialInput json.RawMessage `json:"initialInput"`
}
type sessionListRPCParams struct {
	Query json.RawMessage `json:"query"`
}
type sessionQueryRPC struct {
	Namespace  json.RawMessage `json:"namespace"`
	RuntimeRef json.RawMessage `json:"runtimeRef"`
	State      json.RawMessage `json:"state"`
	Page       json.RawMessage `json:"page"`
}
type pageRPC struct {
	After json.RawMessage `json:"after"`
	Limit json.RawMessage `json:"limit"`
}
type sessionLoadRPCParams struct {
	Input json.RawMessage `json:"input"`
}
type sessionLoadRPCInput struct {
	Metadata   json.RawMessage `json:"metadata"`
	RuntimeRef json.RawMessage `json:"runtimeRef"`
	Cwd        json.RawMessage `json:"cwd"`
	SessionID  json.RawMessage `json:"sessionId"`
	Features   json.RawMessage `json:"features"`
	MCPServers json.RawMessage `json:"mcpServers"`
}
type sessionCancelLoadRPCParams struct {
	Session json.RawMessage `json:"session"`
	LoadID  json.RawMessage `json:"loadId"`
}
type sessionHistoryRPCParams struct {
	Session json.RawMessage `json:"session"`
	Query   json.RawMessage `json:"query"`
}
type historyQueryRPC struct {
	Types json.RawMessage `json:"types"`
	After json.RawMessage `json:"after"`
	Limit json.RawMessage `json:"limit"`
}

func (c *rpcConnection) prepareSessionCall(method string, raw *json.RawMessage) (rpcCall, bool, error) {
	switch method {
	case "session.create":
		input, err := decodeSessionCreate(raw)
		if err != nil {
			return nil, true, err
		}
		if input.InitialInput == nil {
			return func(ctx context.Context) (any, error) {
				return c.api.CreateSession(ctx, resource.Session{Metadata: input.Metadata, Spec: input.Spec})
			}, true, nil
		}
		return func(ctx context.Context) (any, error) {
			return c.api.CreateSessionWithInput(ctx, input)
		}, true, nil

	case "session.get":
		ref, err := decodeSessionRefParams(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return c.api.GetSession(ref) }, true, nil

	case "session.list":
		query, err := decodeSessionQuery(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return c.api.ListSessions(query) }, true, nil

	case "session.resume":
		ref, input, err := decodeSessionResume(raw)
		if err != nil {
			return nil, true, err
		}
		if input == nil || input.InitialInput == nil {
			return func(ctx context.Context) (any, error) {
				session, err := c.api.ResumeSession(ctx, ref)
				return resource.SessionResumed{Session: session}, err
			}, true, nil
		}
		return func(ctx context.Context) (any, error) {
			return c.api.ResumeSessionWithInput(ctx, ref, *input.InitialInput)
		}, true, nil

	case "session.close":
		ref, err := decodeSessionRefParams(raw)
		if err != nil {
			return nil, true, err
		}
		return func(ctx context.Context) (any, error) { return c.api.CloseSession(ctx, ref) }, true, nil

	case "session.load":
		input, err := decodeSessionLoad(raw)
		if err != nil {
			return nil, true, err
		}
		return func(ctx context.Context) (any, error) { return c.api.LoadSession(ctx, input) }, true, nil

	case "session.cancelLoad":
		ref, loadID, err := decodeSessionCancelLoad(raw)
		if err != nil {
			return nil, true, err
		}
		return func(ctx context.Context) (any, error) {
			return c.api.CancelHistoryLoad(ctx, ref, loadID)
		}, true, nil

	case "session.history":
		ref, query, err := decodeSessionHistory(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return c.api.ListHistoryEvents(ref, query) }, true, nil

	default:
		return nil, false, nil
	}
}

func decodeSessionCreate(raw *json.RawMessage) (resource.SessionCreateInput, error) {
	var params sessionCreateRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Input) == 0 {
		return resource.SessionCreateInput{}, errInvalidRPCParams
	}
	var wire sessionCreateRPCInput
	if decodeObject(params.Input, &wire) != nil || len(wire.Metadata) == 0 || wire.Spec == nil {
		return resource.SessionCreateInput{}, errInvalidRPCParams
	}
	metadata, err := decodeSessionMetadata(wire.Metadata)
	if err != nil {
		return resource.SessionCreateInput{}, err
	}
	initial, err := decodeOptionalValue[runtime.Input](wire.InitialInput)
	if err != nil {
		return resource.SessionCreateInput{}, err
	}
	return resource.SessionCreateInput{Metadata: metadata, Spec: *wire.Spec, InitialInput: initial}, nil
}

func decodeSessionMetadata(raw json.RawMessage) (resource.Metadata, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return resource.Metadata{}, errInvalidRPCParams
	}
	var wire sessionMetadataRPC
	if decodeObject(raw, &wire) != nil || len(wire.Name) == 0 {
		return resource.Metadata{}, errInvalidRPCParams
	}
	name, err := decodeJSONString(wire.Name)
	if err != nil {
		return resource.Metadata{}, err
	}
	namespace := ""
	if len(wire.Namespace) != 0 {
		namespace, err = decodeJSONString(wire.Namespace)
		if err != nil {
			return resource.Metadata{}, err
		}
	}
	return resource.Metadata{Namespace: namespace, Name: name}, nil
}

func decodeSessionRefParams(raw *json.RawMessage) (resource.SessionRef, error) {
	var params sessionRefRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Session) == 0 {
		return resource.SessionRef{}, errInvalidRPCParams
	}
	return decodeSessionRef(params.Session)
}

func decodeSessionRef(raw json.RawMessage) (resource.SessionRef, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return resource.SessionRef{}, errInvalidRPCParams
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return resource.SessionRef{}, errInvalidRPCParams
	}
	if idRaw, ok := fields["sessionId"]; ok {
		if len(fields) != 1 {
			return resource.SessionRef{}, errInvalidRPCParams
		}
		id, err := decodeNonemptyString(idRaw)
		if err != nil {
			return resource.SessionRef{}, err
		}
		return resource.SessionRef{SessionID: id}, nil
	}
	namespaceRaw, hasNamespace := fields["namespace"]
	nameRaw, hasName := fields["name"]
	if !hasNamespace || !hasName || len(fields) != 2 {
		return resource.SessionRef{}, errInvalidRPCParams
	}
	namespace, err := decodeNonemptyString(namespaceRaw)
	if err != nil {
		return resource.SessionRef{}, err
	}
	name, err := decodeNonemptyString(nameRaw)
	if err != nil {
		return resource.SessionRef{}, err
	}
	return resource.SessionRef{Namespace: namespace, Name: name}, nil
}

func decodeSessionResume(raw *json.RawMessage) (resource.SessionRef, *resource.SessionResume, error) {
	var params sessionResumeRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Session) == 0 {
		return resource.SessionRef{}, nil, errInvalidRPCParams
	}
	ref, err := decodeSessionRef(params.Session)
	if err != nil {
		return resource.SessionRef{}, nil, err
	}
	if len(params.Input) == 0 {
		return ref, nil, nil
	}
	var wire sessionResumeRPCInput
	if decodeObject(params.Input, &wire) != nil {
		return resource.SessionRef{}, nil, errInvalidRPCParams
	}
	initial, err := decodeOptionalValue[runtime.Input](wire.InitialInput)
	if err != nil {
		return resource.SessionRef{}, nil, err
	}
	return ref, &resource.SessionResume{InitialInput: initial}, nil
}

func decodeSessionQuery(raw *json.RawMessage) (resource.SessionQuery, error) {
	var query resource.SessionQuery
	if raw == nil {
		return query, nil
	}
	var params sessionListRPCParams
	if decodeRPCParams(raw, &params) != nil {
		return resource.SessionQuery{}, errInvalidRPCParams
	}
	if len(params.Query) == 0 {
		return query, nil
	}
	var wire sessionQueryRPC
	if decodeObject(params.Query, &wire) != nil {
		return resource.SessionQuery{}, errInvalidRPCParams
	}
	namespace, err := decodeOptionalString(wire.Namespace, false)
	if err != nil {
		return resource.SessionQuery{}, err
	}
	runtimeRef, err := decodeOptionalString(wire.RuntimeRef, false)
	if err != nil {
		return resource.SessionQuery{}, err
	}
	state, err := decodeOptionalValue[events.SessionState](wire.State)
	if err != nil || state != nil && resource.ValidateSessionState(*state) != nil {
		return resource.SessionQuery{}, errInvalidRPCParams
	}
	query.Namespace = namespace
	query.RuntimeRef = runtimeRef
	if state != nil {
		query.State = *state
	}
	if len(wire.Page) == 0 {
		return query, nil
	}
	var pageWire pageRPC
	if decodeObject(wire.Page, &pageWire) != nil {
		return resource.SessionQuery{}, errInvalidRPCParams
	}
	after, err := decodeOptionalString(pageWire.After, true)
	if err != nil {
		return resource.SessionQuery{}, err
	}
	limit, err := decodeOptionalValue[int](pageWire.Limit)
	if err != nil || limit != nil && *limit < 1 {
		return resource.SessionQuery{}, errInvalidRPCParams
	}
	query.After = after
	if limit != nil {
		query.Limit = *limit
	}
	return query, nil
}

func decodeSessionLoad(raw *json.RawMessage) (resource.SessionLoad, error) {
	var params sessionLoadRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Input) == 0 {
		return resource.SessionLoad{}, errInvalidRPCParams
	}
	var wire sessionLoadRPCInput
	if decodeObject(params.Input, &wire) != nil ||
		len(wire.Metadata) == 0 || len(wire.RuntimeRef) == 0 || len(wire.Cwd) == 0 || len(wire.SessionID) == 0 {
		return resource.SessionLoad{}, errInvalidRPCParams
	}
	metadata, err := decodeSessionMetadata(wire.Metadata)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	runtimeRef, err := decodeJSONString(wire.RuntimeRef)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	cwd, err := decodeJSONString(wire.Cwd)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	sessionID, err := decodeJSONString(wire.SessionID)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	features, err := decodeOptionalValue[runtime.SessionFeatures](wire.Features)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	servers, err := decodeOptionalValue[[]runtime.MCPServer](wire.MCPServers)
	if err != nil {
		return resource.SessionLoad{}, err
	}
	return resource.SessionLoad{
		Metadata: metadata, RuntimeRef: runtimeRef, Cwd: cwd, SessionID: sessionID,
		Features: features, MCPServers: servers,
	}, nil
}

func decodeSessionCancelLoad(raw *json.RawMessage) (resource.SessionRef, string, error) {
	var params sessionCancelLoadRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Session) == 0 || len(params.LoadID) == 0 {
		return resource.SessionRef{}, "", errInvalidRPCParams
	}
	ref, err := decodeSessionRef(params.Session)
	if err != nil {
		return resource.SessionRef{}, "", err
	}
	loadID, err := decodeJSONString(params.LoadID)
	if err != nil {
		return resource.SessionRef{}, "", err
	}
	return ref, loadID, nil
}

func decodeSessionHistory(raw *json.RawMessage) (resource.SessionRef, store.HistoryQuery, error) {
	var params sessionHistoryRPCParams
	if decodeRPCParams(raw, &params) != nil || len(params.Session) == 0 {
		return resource.SessionRef{}, store.HistoryQuery{}, errInvalidRPCParams
	}
	ref, err := decodeSessionRef(params.Session)
	if err != nil {
		return resource.SessionRef{}, store.HistoryQuery{}, err
	}
	if len(params.Query) == 0 {
		return ref, store.HistoryQuery{}, nil
	}
	var wire historyQueryRPC
	if decodeObject(params.Query, &wire) != nil {
		return resource.SessionRef{}, store.HistoryQuery{}, errInvalidRPCParams
	}
	types, err := decodeOptionalValue[[]string](wire.Types)
	if err != nil || types != nil && len(*types) == 0 {
		return resource.SessionRef{}, store.HistoryQuery{}, errInvalidRPCParams
	}
	after, err := decodeOptionalString(wire.After, false)
	if err != nil {
		return resource.SessionRef{}, store.HistoryQuery{}, err
	}
	limit, err := decodeOptionalValue[int](wire.Limit)
	if err != nil || limit != nil && *limit < 1 {
		return resource.SessionRef{}, store.HistoryQuery{}, errInvalidRPCParams
	}
	query := store.HistoryQuery{After: after}
	if types != nil {
		for _, kind := range *types {
			if !events.ValidType(kind) {
				return resource.SessionRef{}, store.HistoryQuery{}, errInvalidRPCParams
			}
		}
		query.Types = *types
	}
	if limit != nil {
		query.Limit = *limit
	}
	return ref, query, nil
}

func decodeObject(raw json.RawMessage, out any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errInvalidRPCParams
	}
	data := bytes.TrimSpace(raw)
	if len(data) == 0 || data[0] != '{' || rpcwire.DecodeStrict(data, out) != nil {
		return errInvalidRPCParams
	}
	return nil
}

func decodeOptionalValue[T any](raw json.RawMessage) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errInvalidRPCParams
	}
	var value T
	if rpcwire.DecodeStrict(bytes.TrimSpace(raw), &value) != nil {
		return nil, errInvalidRPCParams
	}
	return &value, nil
}

func decodeOptionalString(raw json.RawMessage, allowEmpty bool) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	return decodeStringValue(raw, allowEmpty)
}

func decodeNonemptyString(raw json.RawMessage) (string, error) {
	return decodeStringValue(raw, false)
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	return decodeStringValue(raw, true)
}

func decodeStringValue(raw json.RawMessage, allowEmpty bool) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errInvalidRPCParams
	}
	var value string
	if rpcwire.DecodeStrict(bytes.TrimSpace(raw), &value) != nil || !allowEmpty && value == "" || !utf8.ValidString(value) {
		return "", errInvalidRPCParams
	}
	return value, nil
}
