package workspace

import (
	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// columns returns the table column definitions for Workspace resources.
func columns() []cliutil.Column {
	return []cliutil.Column{
		{Header: "NAME", Field: func(v any) string { return v.(pkgariapi.Workspace).Metadata.Name }},
		{Header: "PHASE", Field: func(v any) string { return string(v.(pkgariapi.Workspace).Status.Phase) }},
		{Header: "PATH", Field: func(v any) string { return v.(pkgariapi.Workspace).Status.Path }},
		{Header: "AGE", Field: func(v any) string { return cliutil.FormatAge(v.(pkgariapi.Workspace).Metadata.CreatedAt) }},
	}
}
