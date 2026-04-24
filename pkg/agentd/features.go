package agentd

import pkgariapi "github.com/zoumo/mass/pkg/ari/api"

const (
	FeatureWorkspaceMesh = "workspaceMesh"
	FeatureAgentTask     = "agentTask"
)

var defaultFeatures = map[string]bool{
	FeatureWorkspaceMesh: true,
	FeatureAgentTask:     true,
}

// featureEnabled returns whether a feature is enabled for the given workspace.
// Explicit workspace.Spec.Features override takes precedence; missing key uses default.
func featureEnabled(ws *pkgariapi.Workspace, feature string) bool {
	if ws != nil && ws.Spec.Features != nil {
		if v, ok := ws.Spec.Features[feature]; ok {
			return v
		}
	}
	return defaultFeatures[feature]
}
