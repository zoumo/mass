// Package server contains the ARI protocol service interfaces and Register functions.
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
	Create(ctx context.Context, ws *pkgariapi.Workspace) (*pkgariapi.Workspace, error)
	Get(ctx context.Context, name string) (*pkgariapi.Workspace, error)
	List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.WorkspaceList, error)
	Delete(ctx context.Context, name string) error
	Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error)
}

// AgentRunService defines agent run lifecycle RPC methods.
type AgentRunService interface {
	Create(ctx context.Context, ar *pkgariapi.AgentRun) (*pkgariapi.AgentRun, error)
	Get(ctx context.Context, workspace, name string) (*pkgariapi.AgentRun, error)
	List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.AgentRunList, error)
	Delete(ctx context.Context, workspace, name string) error
	Prompt(ctx context.Context, req *pkgariapi.AgentRunPromptParams) (*pkgariapi.AgentRunPromptResult, error)
	Cancel(ctx context.Context, workspace, name string) error
	Stop(ctx context.Context, workspace, name string) error
	Restart(ctx context.Context, workspace, name string) (*pkgariapi.AgentRun, error)
}

// AgentService defines agent definition CRUD methods.
type AgentService interface {
	Create(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error)
	Update(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error)
	Get(ctx context.Context, name string) (*pkgariapi.Agent, error)
	List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.AgentList, error)
	Delete(ctx context.Context, name string) error
}

// ────────────────────────────────────────────────────────────────────────────
// Register functions
// ────────────────────────────────────────────────────────────────────────────

// RegisterWorkspaceService registers a WorkspaceService implementation with the server.
func RegisterWorkspaceService(s *jsonrpc.Server, svc WorkspaceService) {
	s.RegisterService("workspace", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var ws pkgariapi.Workspace
				if err := unmarshal(&ws); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &ws)
			},
			"get": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("name is required")
				}
				return svc.Get(ctx, key.Name)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var opts pkgariapi.ListOptions
				// list params are optional — ignore unmarshal errors for empty params
				_ = unmarshal(&opts)
				return svc.List(ctx, opts)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("name is required")
				}
				return nil, svc.Delete(ctx, key.Name)
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
				var ar pkgariapi.AgentRun
				if err := unmarshal(&ar); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &ar)
			},
			"get": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Workspace == "" || key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("workspace and name are required")
				}
				return svc.Get(ctx, key.Workspace, key.Name)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var opts pkgariapi.ListOptions
				_ = unmarshal(&opts)
				return svc.List(ctx, opts)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Workspace == "" || key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("workspace and name are required")
				}
				return nil, svc.Delete(ctx, key.Workspace, key.Name)
			},
			"prompt": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var req pkgariapi.AgentRunPromptParams
				if err := unmarshal(&req); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Prompt(ctx, &req)
			},
			"cancel": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Workspace == "" || key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("workspace and name are required")
				}
				return nil, svc.Cancel(ctx, key.Workspace, key.Name)
			},
			"stop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Workspace == "" || key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("workspace and name are required")
				}
				return nil, svc.Stop(ctx, key.Workspace, key.Name)
			},
			"restart": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Workspace == "" || key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("workspace and name are required")
				}
				return svc.Restart(ctx, key.Workspace, key.Name)
			},
		},
	})
}

// RegisterAgentService registers an AgentService implementation with the server.
func RegisterAgentService(s *jsonrpc.Server, svc AgentService) {
	s.RegisterService("agent", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var agent pkgariapi.Agent
				if err := unmarshal(&agent); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Create(ctx, &agent)
			},
			"update": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var agent pkgariapi.Agent
				if err := unmarshal(&agent); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return svc.Update(ctx, &agent)
			},
			"get": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("name is required")
				}
				return svc.Get(ctx, key.Name)
			},
			"list": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var opts pkgariapi.ListOptions
				_ = unmarshal(&opts)
				return svc.List(ctx, opts)
			},
			"delete": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var key pkgariapi.ObjectKey
				if err := unmarshal(&key); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				if key.Name == "" {
					return nil, jsonrpc.ErrInvalidParams("name is required")
				}
				return nil, svc.Delete(ctx, key.Name)
			},
		},
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Error helpers
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
	return jsonrpc.ErrInternal(err.Error())
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
