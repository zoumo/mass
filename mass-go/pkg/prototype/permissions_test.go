package prototype

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

func TestAutoApprovePermissionsKeepsOneTimeToolCallScope(t *testing.T) {
	once := events.PermissionOption{OptionID: "once", Kind: events.PermissionAllow, Scope: events.PermissionScopeOnce}
	always := events.PermissionOption{OptionID: "persist", Kind: events.PermissionAllow, Scope: events.PermissionScopeAlways}
	cancel := events.PermissionOption{OptionID: "cancel", Kind: events.PermissionCancel}
	req := events.InteractionRequested{Request: events.InteractionRequest{
		Kind: events.InteractionPermission, ToolCallID: "tool",
		Options: []events.PermissionOption{always, cancel, once},
	}}
	answer, handled := AutoApprovePermissions(req)
	require.True(t, handled)
	require.Equal(t, map[string]any{"outcome": "selected", "optionId": "once"}, answer)

	req.Request.ToolCallID = ""
	_, handled = AutoApprovePermissions(req)
	require.False(t, handled)

	req.Request.ToolCallID = "tool"
	req.Request.Options = []events.PermissionOption{always, cancel}
	_, handled = AutoApprovePermissions(req)
	require.False(t, handled)
}
