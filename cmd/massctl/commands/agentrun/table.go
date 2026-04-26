package agentrun

import (
	"fmt"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// columns returns the table column definitions for AgentRun resources.
func columns() []cliutil.Column {
	return []cliutil.Column{
		{Header: "WORKSPACE", Field: func(v any) string { return v.(pkgariapi.AgentRun).Metadata.Workspace }},
		{Header: "NAME", Field: func(v any) string { return v.(pkgariapi.AgentRun).Metadata.Name }},
		{Header: "AGENT", Field: func(v any) string { return v.(pkgariapi.AgentRun).Spec.Agent }},
		{Header: "STATE", Field: func(v any) string { return string(v.(pkgariapi.AgentRun).Status.Phase) }},
		{Header: "AGE", Field: func(v any) string { return cliutil.FormatAge(v.(pkgariapi.AgentRun).Metadata.CreatedAt) }},
		{Header: "PID", Field: func(v any) string {
			r := v.(pkgariapi.AgentRun)
			if r.Status.PID > 0 {
				return fmt.Sprintf("%d", r.Status.PID)
			}
			return ""
		}, Wide: true},
		{Header: "ERROR", Field: func(v any) string { return v.(pkgariapi.AgentRun).Status.ErrorMessage }, Wide: true},
	}
}
