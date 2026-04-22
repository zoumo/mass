package acp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/zoumo/mass/internal/logging"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
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
	mgr    *Manager
	logger *slog.Logger
}

var _ acp.Client = (*acpClient)(nil)

// pickOption returns the first option whose Kind matches any of the given kinds,
// checking kinds in the order provided. Returns nil if no match is found.
func pickOption(options []acp.PermissionOption, kinds ...acp.PermissionOptionKind) *acp.PermissionOption {
	for _, kind := range kinds {
		for i := range options {
			if options[i].Kind == kind {
				return &options[i]
			}
		}
	}
	return nil
}

// allowedResponse builds an approved RequestPermissionResponse.
// Prefers opt when non-nil; falls back to options[0]; returns empty OptionId if both absent.
func allowedResponse(options []acp.PermissionOption, opt *acp.PermissionOption) acp.RequestPermissionResponse {
	chosen := opt
	if chosen == nil && len(options) > 0 {
		chosen = &options[0]
	}
	sel := &acp.RequestPermissionOutcomeSelected{Outcome: "selected"}
	if chosen != nil {
		sel.OptionId = chosen.OptionId
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{Selected: sel},
	}
}

// rejectedResponse builds a rejected RequestPermissionResponse using the given option,
// or a canceled outcome when opt is nil.
func rejectedResponse(opt *acp.PermissionOption) acp.RequestPermissionResponse {
	if opt == nil {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}, //nolint:misspell
			},
		}
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{
				OptionId: opt.OptionId,
				Outcome:  "selected",
			},
		},
	}
}

// inferToolKind returns the ToolKind for a permission request.
// Prefers req.ToolCall.Kind when set; falls back to keyword matching on
// req.ToolCall.Title. Returns empty string when kind cannot be determined.
func inferToolKind(req acp.RequestPermissionRequest) acp.ToolKind {
	if req.ToolCall.Kind != nil {
		return *req.ToolCall.Kind
	}
	if req.ToolCall.Title == nil {
		return ""
	}
	title := strings.ToLower(strings.TrimSpace(*req.ToolCall.Title))
	if i := strings.IndexByte(title, ':'); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	switch {
	case strings.Contains(title, "read") || strings.Contains(title, "cat"):
		return acp.ToolKindRead
	case strings.Contains(title, "search") || strings.Contains(title, "find") || strings.Contains(title, "grep"):
		return acp.ToolKindSearch
	case strings.Contains(title, "write") || strings.Contains(title, "edit") || strings.Contains(title, "patch"):
		return acp.ToolKindEdit
	case strings.Contains(title, "delete") || strings.Contains(title, "remove"):
		return acp.ToolKindDelete
	case strings.Contains(title, "run") || strings.Contains(title, "execute") || strings.Contains(title, "bash"):
		return acp.ToolKindExecute
	case strings.Contains(title, "fetch") || strings.Contains(title, "http"):
		return acp.ToolKindFetch
	case strings.Contains(title, "think"):
		return acp.ToolKindThink
	default:
		return ""
	}
}

// isReadKind reports whether the kind is safe to auto-approve under approve_reads.
func isReadKind(kind acp.ToolKind) bool {
	return kind == acp.ToolKindRead || kind == acp.ToolKindSearch || kind == acp.ToolKindThink
}

// SessionUpdate pushes the notification into the Manager's events channel.
// Drops the notification if the channel is full (non-blocking send).
func (c *acpClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	select {
	case c.mgr.events <- n:
		c.logger.Log(context.Background(), logging.LevelTrace, "notification received")
	default:
		c.logger.Log(context.Background(), logging.LevelTrace, "notification dropped, channel full")
	}
	return nil
}

// RequestPermission respects the permission policy.
// For approve_all, selects the first option from the request (auto-approve).
// The response must include a valid Outcome with Selected + OptionId;
// an empty Outcome is treated as denial by ACP agents.
func (c *acpClient) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.logger.Debug("permission request", "policy", c.mgr.cfg.Session.Permissions, "options", len(req.Options))
	switch c.mgr.cfg.Session.Permissions {
	case apiruntime.DenyAll:
		rejectOpt := pickOption(req.Options, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways)
		c.logger.Debug("permission denied", "policy", apiruntime.DenyAll)
		return rejectedResponse(rejectOpt), nil
	case apiruntime.ApproveReads:
		kind := inferToolKind(req)
		if isReadKind(kind) {
			allowOpt := pickOption(req.Options, acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways)
			resp := allowedResponse(req.Options, allowOpt)
			c.logger.Debug("permission approved (read)", "kind", kind)
			return resp, nil
		}
		rejectOpt := pickOption(req.Options, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways)
		c.logger.Debug("permission denied (non-read)", "kind", kind)
		return rejectedResponse(rejectOpt), nil
	case apiruntime.ApproveAll:
		allowOpt := pickOption(req.Options, acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways)
		resp := allowedResponse(req.Options, allowOpt)
		c.logger.Debug("permission approved", "optionId", resp.Outcome.Selected.OptionId)
		return resp, nil
	default:
		// Unknown permission policy: fail closed to prevent silent permission escalation.
		return acp.RequestPermissionResponse{}, fmt.Errorf("permission denied: unknown permission policy %q", c.mgr.cfg.Session.Permissions)
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
