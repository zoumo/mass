package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	rpc2 "github.com/sourcegraph/jsonrpc2"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

var errInvalidRPCParams = errors.New("invalid JSON-RPC params")

type rpcCall func(context.Context) (any, error)

type runtimeCreateRPCParams struct {
	Input *runtimeCreateRPC `json:"input"`
}
type runtimeCreateRPC struct {
	Metadata *createMetadata       `json:"metadata"`
	Spec     *resource.RuntimeSpec `json:"spec"`
}
type runtimeRefRPCParams struct {
	RuntimeRef *string `json:"runtimeRef"`
}
type runtimeUpdateRPCParams struct {
	RuntimeRef *string               `json:"runtimeRef"`
	Spec       *resource.RuntimeSpec `json:"spec"`
}
type runtimeListRPCParams struct {
	Query json.RawMessage `json:"query"`
}

func (c *rpcConnection) prepareRuntimeCall(method string, raw *json.RawMessage) (rpcCall, bool, error) {
	switch method {
	case "runtime.create":
		var params runtimeCreateRPCParams
		if err := decodeRPCParams(raw, &params); err != nil || params.Input == nil || params.Input.Metadata == nil || params.Input.Spec == nil {
			return nil, true, errInvalidRPCParams
		}
		input := params.Input
		return func(context.Context) (any, error) {
			return c.api.CreateRuntime(resource.Runtime{
				Metadata: resource.Metadata{Name: input.Metadata.Name, Namespace: input.Metadata.Namespace},
				Spec:     *input.Spec,
			})
		}, true, nil

	case "runtime.get":
		ref, err := decodeRuntimeRef(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return c.api.GetRuntime(ref) }, true, nil

	case "runtime.list":
		query, err := decodeRuntimeQuery(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return c.api.ListRuntimes(query) }, true, nil

	case "runtime.update":
		var params runtimeUpdateRPCParams
		if err := decodeRPCParams(raw, &params); err != nil || params.RuntimeRef == nil || params.Spec == nil {
			return nil, true, errInvalidRPCParams
		}
		ref, spec := *params.RuntimeRef, *params.Spec
		return func(context.Context) (any, error) { return c.api.UpdateRuntime(ref, spec) }, true, nil

	case "runtime.start":
		ref, err := decodeRuntimeRef(raw)
		if err != nil {
			return nil, true, err
		}
		return func(ctx context.Context) (any, error) {
			return c.api.StartRuntime(ctx, ref)
		}, true, nil

	case "runtime.stop":
		ref, err := decodeRuntimeRef(raw)
		if err != nil {
			return nil, true, err
		}
		return func(ctx context.Context) (any, error) {
			return c.api.StopRuntime(ctx, ref)
		}, true, nil

	case "runtime.delete":
		ref, err := decodeRuntimeRef(raw)
		if err != nil {
			return nil, true, err
		}
		return func(context.Context) (any, error) { return nil, c.api.DeleteRuntime(ref) }, true, nil

	default:
		return nil, false, nil
	}
}

func decodeRPCParams(raw *json.RawMessage, out any) error {
	if raw == nil {
		return errInvalidRPCParams
	}
	data := bytes.TrimSpace(*raw)
	if len(data) == 0 || data[0] != '{' || rpcwire.DecodeStrict(data, out) != nil {
		return errInvalidRPCParams
	}
	return nil
}

func decodeRuntimeRef(raw *json.RawMessage) (string, error) {
	var params runtimeRefRPCParams
	if err := decodeRPCParams(raw, &params); err != nil || params.RuntimeRef == nil {
		return "", errInvalidRPCParams
	}
	return *params.RuntimeRef, nil
}

func decodeRuntimeQuery(raw *json.RawMessage) (resource.RuntimeQuery, error) {
	var params runtimeListRPCParams
	if raw != nil {
		if err := decodeRPCParams(raw, &params); err != nil {
			return resource.RuntimeQuery{}, err
		}
	}
	var query resource.RuntimeQuery
	if len(params.Query) == 0 {
		return query, nil
	}
	if bytes.Equal(bytes.TrimSpace(params.Query), []byte("null")) {
		return query, errInvalidRPCParams
	}
	if rpcwire.DecodeStrict(params.Query, &query) != nil {
		return query, errInvalidRPCParams
	}
	return query, nil
}

func (c *rpcConnection) dispatchCall(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request, key string, call rpcCall) {
	defer c.end(key)
	if c.api == nil {
		replyInternalError(ctx, conn, req.ID)
		return
	}
	result, err := call(context.WithoutCancel(ctx))
	if err != nil {
		replyServiceError(ctx, conn, req.ID, err)
		return
	}
	response := &rpc2.Response{ID: req.ID}
	if err := response.SetResult(result); err != nil {
		slog.Default().Debug("JSON-RPC result encoding failed", "method", req.Method)
		replyInternalError(ctx, conn, req.ID)
		return
	}
	_ = conn.SendResponse(ctx, response)
}

func replyServiceError(ctx context.Context, conn *rpc2.Conn, id rpc2.ID, err error) {
	public, ok := publicError(err)
	if !ok {
		replyInternalError(ctx, conn, id)
		return
	}
	data, encodeErr := json.Marshal(public)
	if encodeErr != nil {
		replyInternalError(ctx, conn, id)
		return
	}
	raw := json.RawMessage(data)
	_ = conn.SendResponse(ctx, &rpc2.Response{ID: id, Error: &rpc2.Error{
		Code:    rpcwire.CodeServiceError,
		Message: public.Message,
		Data:    &raw,
	}})
}

func replyInternalError(ctx context.Context, conn *rpc2.Conn, id rpc2.ID) {
	_ = conn.SendResponse(ctx, &rpc2.Response{ID: id, Error: &rpc2.Error{
		Code:    rpc2.CodeInternalError,
		Message: "Internal error",
	}})
}
