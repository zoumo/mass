// Package ari contains the ARI protocol types and service interfaces.
// This file defines the service interfaces and Register functions for the ARI protocol.
package ari

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zoumo/oar/pkg/jsonrpc"
)

// ────────────────────────────────────────────────────────────────────────────
// Service Interfaces
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceService defines workspace management RPC methods.
type WorkspaceService interface {
	Create(ctx context.Context, req *WorkspaceCreateParams) (*WorkspaceCreateResult, error)
	Status(ctx context.Context, req *WorkspaceStatusParams) (*WorkspaceStatusResult, error)
	List(ctx context.Context) (*WorkspaceListResult, error)
	Delete(ctx context.Context, req *WorkspaceDeleteParams) error
	Send(ctx context.Context, req *WorkspaceSendParams) (*WorkspaceSendResult, error)
}

// AgentRunService defines agent run lifecycle RPC methods.
type AgentRunService interface {
	Create(ctx context.Context, req *AgentRunCreateParams) (*AgentRunCreateResult, error)
	Prompt(ctx context.Context, req *AgentRunPromptParams) (*AgentRunPromptResult, error)
	Cancel(ctx context.Context, req *AgentRunCancelParams) error
	Stop(ctx context.Context, req *AgentRunStopParams) error
	Delete(ctx context.Context, req *AgentRunDeleteParams) error
	Restart(ctx context.Context, req *AgentRunRestartParams) (*AgentRunRestartResult, error)
	List(ctx context.Context, req *AgentRunListParams) (*AgentRunListResult, error)
	Status(ctx context.Context, req *AgentRunStatusParams) (*AgentRunStatusResult, error)
	Attach(ctx context.Context, req *AgentRunAttachParams) (*AgentRunAttachResult, error)
}

// AgentService defines agent definition CRUD methods.
// All return types use the domain Agent type as wire format.
type AgentService interface {
	Set(ctx context.Context, req *AgentSetParams) (*AgentSetResult, error)
	Get(ctx context.Context, req *AgentGetParams) (*AgentGetResult, error)
	List(ctx context.Context) (*AgentListResult, error)
	Delete(ctx context.Context, req *AgentDeleteParams) error
}

// ────────────────────────────────────────────────────────────────────────────
// Register functions (参考 ttrpc RegisterXxxService pattern)
// ────────────────────────────────────────────────────────────────────────────

// RegisterWorkspaceService registers a WorkspaceService implementation with the server.
func RegisterWorkspaceService(s *jsonrpc.Server, svc WorkspaceService) {
	s.RegisterService("workspace", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req WorkspaceCreateParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &req)
			},
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req WorkspaceStatusParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Status(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.List(ctx)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req WorkspaceDeleteParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Delete(ctx, &req)
			},
			"send": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req WorkspaceSendParams
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
				var req AgentRunCreateParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &req)
			},
			"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunPromptParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Prompt(ctx, &req)
			},
			"cancel": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunCancelParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Cancel(ctx, &req)
			},
			"stop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunStopParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Stop(ctx, &req)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunDeleteParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return nil, svc.Delete(ctx, &req)
			},
			"restart": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunRestartParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Restart(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunListParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.List(ctx, &req)
			},
			"status": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunStatusParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Status(ctx, &req)
			},
			"attach": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentRunAttachParams
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
				var req AgentSetParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Set(ctx, &req)
			},
			"get": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentGetParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Get(ctx, &req)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return svc.List(ctx)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req AgentDeleteParams
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
		return &jsonrpc.RPCError{Code: CodeRecoveryBlocked, Message: err.Error()}
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
