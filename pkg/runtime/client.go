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
// Terminal operations are delegated to TerminalManager.
type acpClient struct {
	mgr *Manager
	terminalMgr *TerminalManager
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

// Terminal operations are delegated to TerminalManager.

func (c *acpClient) CreateTerminal(_ context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if c.terminalMgr == nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal not supported: terminal manager not initialized")
	}

	terminalID, err := c.terminalMgr.Create(
		context.Background(), // Use background context for process lifecycle
		params.Command,
		params.Args,
		params.Cwd,
		convertEnvVariables(params.Env),
		params.OutputByteLimit,
	)
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("CreateTerminal: %w", err)
	}

	return acp.CreateTerminalResponse{TerminalId: terminalID}, nil
}

func (c *acpClient) KillTerminalCommand(_ context.Context, params acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	if c.terminalMgr == nil {
		return acp.KillTerminalCommandResponse{}, fmt.Errorf("terminal not supported: terminal manager not initialized")
	}

	if err := c.terminalMgr.Kill(params.TerminalId); err != nil {
		return acp.KillTerminalCommandResponse{}, fmt.Errorf("KillTerminalCommand: %w", err)
	}

	return acp.KillTerminalCommandResponse{}, nil
}

func (c *acpClient) TerminalOutput(_ context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	if c.terminalMgr == nil {
		return acp.TerminalOutputResponse{}, fmt.Errorf("terminal not supported: terminal manager not initialized")
	}

	output, truncated, exitCode, signal, err := c.terminalMgr.Output(params.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, fmt.Errorf("TerminalOutput: %w", err)
	}

	// Build exit status if available
	var exitStatus *acp.TerminalExitStatus
	if exitCode != nil || signal != nil {
		exitStatus = &acp.TerminalExitStatus{
			ExitCode: exitCode,
			Signal:   signal,
		}
	}

	return acp.TerminalOutputResponse{
		Output:    output,
		Truncated: truncated,
		ExitStatus: exitStatus,
	}, nil
}

func (c *acpClient) ReleaseTerminal(_ context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	if c.terminalMgr == nil {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("terminal not supported: terminal manager not initialized")
	}

	if err := c.terminalMgr.Release(params.TerminalId); err != nil {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("ReleaseTerminal: %w", err)
	}

	return acp.ReleaseTerminalResponse{}, nil
}

func (c *acpClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	if c.terminalMgr == nil {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("terminal not supported: terminal manager not initialized")
	}

	exitCode, signal, err := c.terminalMgr.WaitForExit(ctx, params.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("WaitForTerminalExit: %w", err)
	}

	return acp.WaitForTerminalExitResponse{
		ExitCode: exitCode,
		Signal:   signal,
	}, nil
}

// convertEnvVariables converts acp.EnvVariable slice to []string format.
func convertEnvVariables(envVars []acp.EnvVariable) []string {
	if envVars == nil {
		return nil
	}
	result := make([]string, 0, len(envVars))
	for _, v := range envVars {
		result = append(result, v.Name+"="+v.Value)
	}
	return result
}
