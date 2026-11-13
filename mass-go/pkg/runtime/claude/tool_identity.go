package claude

import (
	"strings"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

type unifiedToolIdentity struct {
	kind     events.ToolKind
	toolName string
	provider events.ToolProvider
}

func claudeToolIdentityFor(toolName string) unifiedToolIdentity {
	identity := unifiedToolIdentity{
		kind:     events.ToolKindOther,
		toolName: toolName,
		provider: events.ToolProviderAgent,
	}
	if strings.HasPrefix(toolName, "mcp__") {
		identity.provider = events.ToolProviderMCP
		remainder := strings.TrimPrefix(toolName, "mcp__")
		if server, _, ok := strings.Cut(remainder, "__"); ok && server != "" {
			identity.provider = events.ToolProvider(string(agent.ToolProviderMCP) + ":" + server)
		}
		return identity
	}
	switch toolName {
	case "Read":
		identity.kind = events.ToolKindRead
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		identity.kind = events.ToolKindEdit
	case "Bash":
		identity.kind = events.ToolKindExecute
	case "Glob", "Grep", "WebSearch":
		identity.kind = events.ToolKindSearch
	case "WebFetch":
		identity.kind = events.ToolKindFetch
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		identity.kind = events.ToolKindThink
	case "ExitPlanMode":
		identity.kind = events.ToolKindSwitchMode
	}
	return identity
}
