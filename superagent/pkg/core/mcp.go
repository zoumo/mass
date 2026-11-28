package core

import (
	"encoding/json"
	"errors"

	rpc2 "github.com/sourcegraph/jsonrpc2"

	"github.com/zoumo/mass/internal/superagentapi"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": append([]string{}, required...), "additionalProperties": false}
}
func stringSchema() map[string]any { return map[string]any{"type": "string", "minLength": 1} }

const (
	mcpPath            = superagentapi.Prefix + "/mcp"
	mcpProtocolVersion = "2025-11-25"
	mcpBindingMeta     = "mass.dev/session-binding"
)

type mcpInitializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]map[string]bool `json:"capabilities"`
	ServerInfo      map[string]string          `json:"serverInfo"`
	Meta            map[string]any             `json:"_meta,omitempty"`
}

func supportedMCPVersion(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", mcpProtocolVersion:
		return true
	default:
		return false
	}
}

func mcpInitialize(params *json.RawMessage) (mcpInitializeResult, error) {
	var p struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      json.RawMessage `json:"clientInfo"`
		Meta            json.RawMessage `json:"_meta,omitempty"`
	}
	if params == nil || decodeStrict(*params, &p) != nil || p.ProtocolVersion == "" {
		return mcpInitializeResult{}, mcpInvalidParams()
	}
	version := p.ProtocolVersion
	if !supportedMCPVersion(version) {
		version = mcpProtocolVersion
	}
	return mcpInitializeResult{
		ProtocolVersion: version,
		Capabilities:    map[string]map[string]bool{"tools": {"listChanged": false}},
		ServerInfo:      map[string]string{"name": "superagent", "version": "v1"},
	}, nil
}

func mcpInvalidParams() *rpc2.Error {
	return &rpc2.Error{Code: rpc2.CodeInvalidParams, Message: "Invalid params"}
}

func mcpRPCError(err error) *rpc2.Error {
	var rpcErr *rpc2.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	var e *Error
	if !errors.As(err, &e) {
		e = problem("unavailable", "MCP operation failed")
	}
	rpcErr = &rpc2.Error{Code: -32000, Message: e.Message}
	rpcErr.SetError(e)
	return rpcErr
}
