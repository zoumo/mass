// Package server contains the ARI protocol service interfaces and Register functions.
package server

import (
	"context"
	"errors"

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
	TaskDo(ctx context.Context, params *pkgariapi.AgentRunTaskDoParams) (*pkgariapi.AgentTask, error)
	TaskGet(ctx context.Context, params *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error)
	TaskList(ctx context.Context, params *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error)
	TaskRetry(ctx context.Context, params *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentTask, error)
}

// AgentService defines agent definition CRUD methods.
type AgentService interface {
	Create(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error)
	Update(ctx context.Context, agent *pkgariapi.Agent) (*pkgariapi.Agent, error)
	Get(ctx context.Context, name string) (*pkgariapi.Agent, error)
	List(ctx context.Context, opts pkgariapi.ListOptions) (*pkgariapi.AgentList, error)
	Delete(ctx context.Context, name string) error
}

// SystemService defines system-level RPC methods (daemon info).
type SystemService interface {
	Info(ctx context.Context) (*pkgariapi.SystemInfoResult, error)
}

// ────────────────────────────────────────────────────────────────────────────
// Register functions
// ────────────────────────────────────────────────────────────────────────────

// RegisterWorkspaceService registers a WorkspaceService implementation with the server.
func RegisterWorkspaceService(s *jsonrpc.Server, svc WorkspaceService) {
	s.RegisterService("workspace", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": jsonrpc.UnaryMethod(svc.Create),
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
			"send": jsonrpc.UnaryMethod(svc.Send),
		},
	})
}

// RegisterAgentRunService registers an AgentRunService implementation with the server.
func RegisterAgentRunService(s *jsonrpc.Server, svc AgentRunService) {
	s.RegisterService("agentrun", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": jsonrpc.UnaryMethod(svc.Create),
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
			"prompt": jsonrpc.UnaryMethod(svc.Prompt),
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
			"task/do":    jsonrpc.UnaryMethod(svc.TaskDo),
			"task/get":   jsonrpc.UnaryMethod(svc.TaskGet),
			"task/list":  jsonrpc.UnaryMethod(svc.TaskList),
			"task/retry": jsonrpc.UnaryMethod(svc.TaskRetry),
		},
	})
}

// RegisterAgentService registers an AgentService implementation with the server.
func RegisterAgentService(s *jsonrpc.Server, svc AgentService) {
	s.RegisterService("agent", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"create": jsonrpc.UnaryMethod(svc.Create),
			"update": jsonrpc.UnaryMethod(svc.Update),
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

// RegisterSystemService registers a SystemService implementation with the server.
func RegisterSystemService(s *jsonrpc.Server, svc SystemService) {
	s.RegisterService("system", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"info": jsonrpc.NullaryMethod(svc.Info),
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
	var rpcErr *jsonrpc.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return jsonrpc.ErrInternal(err.Error())
}
