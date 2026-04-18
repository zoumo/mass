package agent

import (
	"strings"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// columns returns the table column definitions for Agent resources.
func columns() []cliutil.Column {
	return []cliutil.Column{
		{Header: "NAME", Field: func(v any) string { return v.(pkgariapi.Agent).Metadata.Name }},
		{Header: "COMMAND", Field: func(v any) string { return v.(pkgariapi.Agent).Spec.Command }},
		{Header: "AGE", Field: func(v any) string { return cliutil.FormatAge(v.(pkgariapi.Agent).Metadata.CreatedAt) }},
		{Header: "ARGS", Field: func(v any) string { return strings.Join(v.(pkgariapi.Agent).Spec.Args, " ") }, Wide: true},
		{Header: "LABELS", Field: func(v any) string { return formatLabels(v.(pkgariapi.Agent).Metadata.Labels) }, Wide: true},
	}
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "<none>"
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, ",")
}
