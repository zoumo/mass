package events

import "github.com/zoumo/agent-sdk-go/agent"

type (
	InteractionRef       = agent.InteractionRef
	InteractionKind      = agent.InteractionKind
	PermissionOption     = agent.PermissionOption
	PermissionOptionKind = agent.PermissionOptionKind
	InteractionRequest   = agent.InteractionRequest
	InteractionRequested = agent.InteractionRequestedPayload
	InteractionClosed    = agent.InteractionClosedPayload
	InteractionOutcome   = agent.InteractionOutcome
)

const (
	InteractionPermission = agent.InteractionPermission
	InteractionForm       = agent.InteractionForm
	InteractionURL        = agent.InteractionURL

	PermissionAllowOnce    = agent.PermissionAllowOnce
	PermissionAllowAlways  = agent.PermissionAllowAlways
	PermissionRejectOnce   = agent.PermissionRejectOnce
	PermissionRejectAlways = agent.PermissionRejectAlways

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
			{OptionID: "allow-once", Name: "Allow once", Kind: agent.PermissionAllowOnce},
			{OptionID: "reject-once", Name: "Reject once", Kind: agent.PermissionRejectOnce},
		},
	}
}
