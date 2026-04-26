package jsonrpc

import "context"

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
