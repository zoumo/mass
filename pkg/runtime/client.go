package runtime

import (
	"context"
	"fmt"

	"github.com/coder/acp-go-sdk"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// acpClient implements acp.Client on behalf of Manager.
// It forwards SessionNotifications into Manager.events and handles
// RequestPermission according to the configured PermissionPolicy.
//
// Filesystem and terminal methods are not supported: the Initialize handshake
// does not advertise fs capabilities, so well-behaved ACP agents will never
// send those requests. The methods satisfy the interface and return an error if
// called unexpectedly.
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

// RequestPermission respects the permission policy.
// For approve-all, selects the first option from the request (auto-approve).
// The response must include a valid Outcome with Selected + OptionId;
// an empty Outcome is treated as denial by ACP agents.
func (c *acpClient) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	switch c.mgr.cfg.Permissions {
	case spec.DenyAll:
		return acp.RequestPermissionResponse{}, fmt.Errorf("permission denied: deny-all policy blocks all operations")
	case spec.ApproveReads:
		return acp.RequestPermissionResponse{}, fmt.Errorf("permission denied: approve-reads policy blocks write operations")
	default: // approve-all — select the first available option
		if len(req.Options) == 0 {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{
						Outcome: "selected",
					},
				},
			}, nil
		}
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{
					OptionId: req.Options[0].OptionId,
					Outcome:  "selected",
				},
			},
		}, nil
	}
}

// Filesystem and terminal methods — not supported.
// The Initialize handshake does not advertise these capabilities.

func (c *acpClient) ReadTextFile(_ context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) WriteTextFile(_ context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) KillTerminalCommand(_ context.Context, _ acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	return acp.KillTerminalCommandResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, fmt.Errorf("not supported")
}

func (c *acpClient) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, fmt.Errorf("not supported")
}
