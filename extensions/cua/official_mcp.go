package cua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An officialMCPSession owns one subprocess and its isolated Cua state.
type officialMCPSession struct {
	mu      sync.Mutex
	session *mcp.ClientSession
	dir     string
	closed  bool
}

func (s *officialMCPSession) get(ctx context.Context, cfg *config, target *computer,
	connect func(context.Context, *config, *computer, string) (*mcp.ClientSession, error),
) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return nil, errors.New("call to Cua MCP canceled")
	}
	if s.session != nil {
		return s.session, nil
	}

	dir, err := os.MkdirTemp("", "cua-mcp-*")
	if err != nil {
		return nil, errors.New("cannot create private Cua state directory")
	}
	session, err := connect(ctx, cfg, target, dir)
	if err != nil {
		return nil, errors.Join(err, removeCuaState(dir))
	}
	s.dir, s.session = dir, session
	return session, nil
}

func closeOfficialSessions(sessions map[string]*officialMCPSession) error {
	var err error
	for _, session := range sessions {
		err = errors.Join(err, session.close())
	}
	return err
}

func (s *officialMCPSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.session == nil {
		return nil
	}
	err := s.session.Close()
	if err != nil {
		err = errors.New("official Cua MCP subprocess did not close cleanly")
	}
	return errors.Join(err, removeCuaState(s.dir))
}

func removeCuaState(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("cannot remove private Cua state directory")
	}
	return nil
}

func cuaCommandEnv(cfg *config, dir string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"CUA_HOME=" + dir,
		"CUA_CREDENTIAL_STORE=file",
		"CUA_MESH=off",
		"CUA_TELEMETRY=off",
	}
	if cfg.caFile != "" {
		env = append(env, "SSL_CERT_FILE="+cfg.caFile)
	}
	return env
}

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
