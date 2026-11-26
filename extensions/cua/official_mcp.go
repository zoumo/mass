package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectOfficialMCP(ctx context.Context, cmd *exec.Cmd) (*mcp.ClientSession, error) {
	// The subprocess can include credentials in its diagnostics.
	cmd.Stderr = nil
	client := mcp.NewClient(&mcp.Implementation{Name: "cua-mcp"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		Logger:       mcpLogger,
	})
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return nil, errors.New("cannot initialize the official Cua MCP subprocess")
	}
	return session, nil
}

func callOfficialTool(ctx context.Context, session *mcp.ClientSession, target *computer, name string, args any) (*mcp.CallToolResult, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		var wireErr *jsonrpc.Error
		if errors.As(err, &wireErr) {
			if !safeWireError(wireErr, target.token) {
				return toolError(errors.New("received unsafe error data from Cua MCP")), nil
			}
			return nil, wireErr
		}
		message := ctx.Err()
		if message == nil {
			message = errors.New("call to Cua MCP failed before receiving a tool result")
		}
		return toolError(message), nil
	}
	if !safeResponse(result, target.token) {
		return toolError(errors.New("received an unsafe or unusable result from Cua MCP")), nil
	}
	return result, nil
}

func safeWireError(err *jsonrpc.Error, token string) bool {
	// Normalize escapes for the credential check and keep the original wire error.
	var data any
	if len(err.Data) > 0 && json.Unmarshal(err.Data, &data) != nil {
		return false
	}
	return safeResponse([]any{err.Message, data}, token)
}

func safeResponse(response any, token string) bool {
	encoded, err := json.Marshal(response)
	if err != nil {
		return false
	}
	quoted, err := json.Marshal(token)
	return err == nil && !bytes.Contains(encoded, quoted[1:len(quoted)-1])
}

func toolError(err error) *mcp.CallToolResult {
	var result mcp.CallToolResult
	result.SetError(err)
	return &result
}
