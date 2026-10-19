// Package prototype contains explicit policies for the offline prototype.
package prototype

import "github.com/zoumo/mass/mass-go/pkg/runtime"

// AutoApprovePermissions handles permission options only. Questions require
// an explicit user answer through Service.AnswerInteraction.
func AutoApprovePermissions(p runtime.InteractionRequested) (map[string]any, bool) {
	if p.Request.Kind != "permission" {
		return nil, false
	}
	for _, option := range p.Request.Options {
		if option.Kind == "allow_once" {
			return map[string]any{"outcome": "selected", "optionId": option.OptionID}, true
		}
	}
	return nil, false
}
