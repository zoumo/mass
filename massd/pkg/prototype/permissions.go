// Package prototype contains explicit policies for the offline prototype.
package prototype

import (
	"github.com/zoumo/mass/massd/pkg/events"
)

// AutoApprovePermissions selects one-time approval for requests correlated to
// a tool call. Standalone permissions and questions require an explicit answer.
func AutoApprovePermissions(p events.InteractionRequested) (map[string]any, bool) {
	if p.Request.Kind != events.InteractionPermission || p.Request.ToolCallID == "" {
		return nil, false
	}
	for _, option := range p.Request.Options {
		if option.Kind == events.PermissionAllowOnce ||
			(option.Kind == events.PermissionAllow && option.Scope == events.PermissionScopeOnce) {
			return map[string]any{"outcome": "selected", "optionId": option.OptionID}, true
		}
	}
	return nil, false
}
