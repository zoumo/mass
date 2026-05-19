package jsonrpc

import (
	"context"
	"errors"
)

func UnaryMethod[Req, Res any](fn func(ctx context.Context, req *Req) (*Res, error)) Method {
	return func(ctx context.Context, unmarshal func(any) error) (any, error) {
		var req Req
		if err := unmarshal(&req); err != nil {
			return nil, ErrInvalidParams(err.Error())
		}
		return fn(ctx, &req)
	}
}

func NullaryMethod[Res any](fn func(ctx context.Context) (*Res, error)) Method {
	return func(ctx context.Context, _ func(any) error) (any, error) {
		return fn(ctx)
	}
}

func UnaryCommand[Req any](fn func(ctx context.Context, req *Req) error) Method {
	return func(ctx context.Context, unmarshal func(any) error) (any, error) {
		var req Req
		if err := unmarshal(&req); err != nil {
			return nil, ErrInvalidParams(err.Error())
		}
		return nil, fn(ctx, &req)
	}
}

func NullaryCommand(fn func(ctx context.Context) error) Method {
	return func(ctx context.Context, _ func(any) error) (any, error) {
		return nil, fn(ctx)
	}
}

// OptionalUnaryCommand is like UnaryCommand but tolerates absent params:
// callers that omit the JSON-RPC `params` field reach the handler with
// Req at zero value rather than getting InvalidParams. Use this for
// methods evolving from NullaryCommand to taking optional params —
// pre-existing callers that send no params keep working unchanged.
//
// Malformed-JSON unmarshal errors still surface as InvalidParams.
func OptionalUnaryCommand[Req any](fn func(ctx context.Context, req *Req) error) Method {
	return func(ctx context.Context, unmarshal func(any) error) (any, error) {
		var req Req
		if err := unmarshal(&req); err != nil && !errors.Is(err, ErrNoParams) {
			return nil, ErrInvalidParams(err.Error())
		}
		return nil, fn(ctx, &req)
	}
}
