package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/acp-go-sdk"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// readFile reads the entire content of a file at path as a string.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from agent request
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// writeFile writes content to path, creating or truncating the file.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644) //nolint:gosec // path from agent
}

// acpClient implements acp.Client on behalf of Manager.
// It enforces the configured PermissionPolicy for fs/* operations and
// forwards SessionNotifications into Manager.events.
type acpClient struct {
	mgr *Manager
}

var _ acp.Client = (*acpClient)(nil)

// SessionUpdate pushes the notification into the Manager's events channel.
// Drops the notification if the channel is full (non-blocking send).
func (c *acpClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	select {
	case c.mgr.events <- n:
	default:
	}
	return nil
}

// ReadTextFile respects the permission policy: approved for approve-all and
// approve-reads, denied for deny-all.
func (c *acpClient) ReadTextFile(_ context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	switch c.mgr.cfg.Permissions {
	case spec.DenyAll:
		return acp.ReadTextFileResponse{}, fmt.Errorf("permission denied: deny-all policy blocks ReadTextFile")
	default: // approve-all, approve-reads, and zero value
		data, err := readFile(params.Path)
		if err != nil {
			return acp.ReadTextFileResponse{}, fmt.Errorf("ReadTextFile %s: %w", params.Path, err)
		}
		return acp.ReadTextFileResponse{Content: data}, nil
	}
}

// WriteTextFile respects the permission policy: approved for approve-all,
// denied for approve-reads and deny-all.
func (c *acpClient) WriteTextFile(_ context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	switch c.mgr.cfg.Permissions {
	case spec.ApproveAll, "": // empty string defaults to approve-all behaviour
		if err := writeFile(params.Path, params.Content); err != nil {
			return acp.WriteTextFileResponse{}, fmt.Errorf("WriteTextFile %s: %w", params.Path, err)
		}
		return acp.WriteTextFileResponse{}, nil
	default: // approve-reads, deny-all
		return acp.WriteTextFileResponse{}, fmt.Errorf("permission denied: %s policy blocks WriteTextFile", c.mgr.cfg.Permissions)
	}
}

// RequestPermission respects the permission policy.
func (c *acpClient) RequestPermission(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	switch c.mgr.cfg.Permissions {
	case spec.DenyAll:
		return acp.RequestPermissionResponse{}, fmt.Errorf("permission denied: deny-all policy blocks all operations")
	case spec.ApproveReads:
		return acp.RequestPermissionResponse{}, fmt.Errorf("permission denied: approve-reads policy blocks write operations")
	default: // approve-all
		return acp.RequestPermissionResponse{}, nil
	}
}

// Terminal operations are not supported; all return a stub error.

func (c *acpClient) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, fmt.Errorf("terminal not supported")
}

func (c *acpClient) KillTerminalCommand(_ context.Context, _ acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	return acp.KillTerminalCommandResponse{}, fmt.Errorf("terminal not supported")
}

func (c *acpClient) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, fmt.Errorf("terminal not supported")
}

func (c *acpClient) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, fmt.Errorf("terminal not supported")
}

func (c *acpClient) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, fmt.Errorf("terminal not supported")
}
