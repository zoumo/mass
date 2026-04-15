// Package server contains the ARI protocol service interfaces and Register functions.
// This file defines the service interfaces and Register functions for the ARI protocol.
package server

import (
	"context"
	"encoding/json"
	"fmt"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ────────────────────────────────────────────────────────────────────────────
// Service Interfaces
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceService defines workspace management RPC methods.
type WorkspaceService interface {
	Create(ctx context.Context, req *pkgariapi.WorkspaceCreateParams) (*pkgariapi.WorkspaceCreateResult, error)
	Status(ctx context.Context, req *pkgariapi.WorkspaceStatusParams) (*pkgariapi.WorkspaceStatusResult, error)
	List(ctx context.Context) (*pkgariapi.WorkspaceListResult, error)
	Delete(ctx context.Context, req *pkgariapi.WorkspaceDeleteParams) error
	Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error)
}

// AgentRunService defines agent run lifecycle RPC methods.
type AgentRunService interface {
	Create(ctx context.Context, req *pkgariapi.AgentRunCreateParams) (*pkgariapi.AgentRunCreateResult, error)
	Prompt(ctx context.Context, req *pkgariapi.AgentRunPromptParams) (*pkgariapi.AgentRunPromptResult, error)
	Cancel(ctx context.Context, req *pkgariapi.AgentRunCancelParams) error
	Stop(ctx context.Context, req *pkgariapi.AgentRunStopParams) error
	Delete(ctx context.Context, req *pkgariapi.AgentRunDeleteParams) error
	Restart(ctx context.Context, req *pkgariapi.AgentRunRestartParams) (*pkgariapi.AgentRunRestartResult, error)
	List(ctx context.Context, req *pkgariapi.AgentRunListParams) (*pkgariapi.AgentRunListResult, error)
	Status(ctx context.Context, req *pkgariapi.AgentRunStatusParams) (*pkgariapi.AgentRunStatusResult, error)
	Attach(ctx context.Context, req *pkgariapi.AgentRunAttachParams) (*pkgariapi.AgentRunAttachResult, error)
}

// AgentService defines agent definition CRUD methods.
// All return types use the domain Agent type as wire format.
type AgentService interface {
	Set(ctx context.Context, req *pkgariapi.AgentSetParams) (*pkgariapi.AgentSetResult, error)
	Get(ctx context.Context, req *pkgariapi.AgentGetParams) (*pkgariapi.AgentGetResult, error)
	List(ctx context.Context) (*pkgariapi.AgentListResult, error)
	Delete(ctx context.Context, req *pkgariapi.AgentDeleteParams) error
}

// ────────────────────────────────────────────────────────────────────────────
// Register functions (参考 ttrpc RegisterXxxService pattern)
// ────────────────────────────────────────────────────────────────────────────

// RegisterWorkspaceService registers a WorkspaceService implementation with the server.
func RegisterWorkspaceService(s *jsonrpc.Server, svc WorkspaceService) {
	s.RegisterService("workspace", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.WorkspaceCreateParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &req)
			},
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.WorkspaceStatusParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Status(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.List(ctx)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.WorkspaceDeleteParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Delete(ctx, &req)
			},
			"send": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.WorkspaceSendParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Send(ctx, &req)
			},
		},
	})
}

// RegisterAgentRunService registers an AgentRunService implementation with the server.
func RegisterAgentRunService(s *jsonrpc.Server, svc AgentRunService) {
	s.RegisterService("agentrun", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunCreateParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &req)
			},
			"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunPromptParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Prompt(ctx, &req)
			},
			"cancel": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunCancelParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Cancel(ctx, &req)
			},
			"stop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunStopParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Stop(ctx, &req)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunDeleteParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Delete(ctx, &req)
			},
			"restart": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunRestartParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Restart(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunListParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.List(ctx, &req)
			},
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunStatusParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Status(ctx, &req)
			},
			"attach": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunAttachParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Attach(ctx, &req)
			},
		},
	})
}

// RegisterAgentService registers an AgentService implementation with the server.
func RegisterAgentService(s *jsonrpc.Server, svc AgentService) {
	s.RegisterService("agent", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"set": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentSetParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Set(ctx, &req)
			},
			"get": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentGetParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Get(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.List(ctx)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentDeleteParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Delete(ctx, &req)
			},
		},
	})
}

// ────────────────────────────────────────────────────────────────────────────
// mapRPCError converts domain errors to *jsonrpc.RPCError for use in handlers.
// ────────────────────────────────────────────────────────────────────────────

// MapRPCError converts an error to a *jsonrpc.RPCError.
// CodeRecoveryBlocked errors preserve the -32001 code.
// Other errors become InternalError (-32603).
func MapRPCError(err error) error {
	if err == nil {
		return nil
	}
	if rpcErr, ok := err.(*jsonrpc.RPCError); ok {
		return rpcErr
	}
	// Check for recovery blocked sentinel pattern.
	if isRecoveryBlocked(err) {
		return &jsonrpc.RPCError{Code: pkgariapi.CodeRecoveryBlocked, Message: err.Error()}
	}
	return jsonrpc.ErrInternal(err.Error())
}

// isRecoveryBlocked is a placeholder for domain-specific error detection.
// Implementations can use errors.As or message matching.
func isRecoveryBlocked(err error) bool {
	_ = err
	return false
}

// callRPC is a helper for typed ARI client call patterns.
func callRPC[T any](c *jsonrpc.Client, ctx context.Context, method string, params any) (*T, error) {
	var result T
	if err := c.Call(ctx, method, params, &result); err != nil {
		return nil, fmt.Errorf("ari: %s: %w", method, err)
	}
	return &result, nil
}

// callRPCRaw performs an RPC call with JSON result decoded into dst.
func callRPCRaw(c *jsonrpc.Client, ctx context.Context, method string, params any, dst any) error {
	if dst == nil {
		var raw json.RawMessage
		return c.Call(ctx, method, params, &raw)
	}
	return c.Call(ctx, method, params, dst)
}
