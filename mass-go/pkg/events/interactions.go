package events

import "github.com/zoumo/agent-sdk-go/agent"

type (
	InteractionRef       = agent.InteractionRef
	InteractionKind      = agent.InteractionKind
	PermissionOption     = agent.PermissionOption
	PermissionOptionKind = agent.PermissionOptionKind
	PermissionScope      = agent.PermissionScope
	InteractionRequest   = agent.InteractionRequest
	InteractionRequested = agent.InteractionRequestedPayload
	InteractionClosed    = agent.InteractionClosedPayload
	InteractionOutcome   = agent.InteractionOutcome
)

const (
	InteractionPermission = agent.InteractionPermission
	InteractionForm       = agent.InteractionForm
	InteractionURL        = agent.InteractionURL

	PermissionAllow  = agent.PermissionAllow
	PermissionReject = agent.PermissionReject
	PermissionCancel = agent.PermissionCancel

	PermissionScopeOnce    = agent.PermissionScopeOnce
	PermissionScopeTurn    = agent.PermissionScopeTurn
	PermissionScopeSession = agent.PermissionScopeSession
	PermissionScopeAlways  = agent.PermissionScopeAlways

	InteractionAnswered  = agent.InteractionAnswered
	InteractionCompleted = agent.InteractionCompleted
	InteractionCancelled = agent.InteractionCancelled
	InteractionExpired   = agent.InteractionExpired
	InteractionFailed    = agent.InteractionFailed
)

func PermissionRequest(tool, title string) InteractionRequest {
	return agent.InteractionRequest{
		Kind:       agent.InteractionPermission,
		ToolCallID: tool,
		Title:      title,
		Options: []agent.PermissionOption{
			{OptionID: "allow-once", Name: "Allow once", Kind: PermissionAllow, Scope: PermissionScopeOnce},
			{OptionID: "reject-once", Name: "Reject once", Kind: PermissionReject, Scope: PermissionScopeOnce},
		},
	}
}

// Historical permission kinds remain available to callers reading older events.
//
//nolint:staticcheck // Keep aliases for the original SDK wire values.
const (
	PermissionAllowOnce    = agent.PermissionAllowOnce
	PermissionAllowAlways  = agent.PermissionAllowAlways
	PermissionRejectOnce   = agent.PermissionRejectOnce
	PermissionRejectAlways = agent.PermissionRejectAlways
)
