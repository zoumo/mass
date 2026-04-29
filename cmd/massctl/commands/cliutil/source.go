package cliutil

import (
	"fmt"

	"github.com/zoumo/mass/pkg/workspace"
)

// SourceConfig describes a workspace source in YAML/JSON configuration files.
// Shared by compose and workspace create -f commands.
type SourceConfig struct {
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	URL   string `json:"url,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Depth int    `json:"depth,omitempty"`
}

// BuildSource converts a SourceConfig into the internal workspace.Source type.
func BuildSource(s SourceConfig) (workspace.Source, error) {
	switch s.Type {
	case "local":
		if s.Path == "" {
			return workspace.Source{}, fmt.Errorf("local source requires path")
		}
		return workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: s.Path},
		}, nil
	case "git":
		if s.URL == "" {
			return workspace.Source{}, fmt.Errorf("git source requires url")
		}
		return workspace.Source{
			Type: workspace.SourceTypeGit,
			Git:  workspace.GitSource{URL: s.URL, Ref: s.Ref, Depth: s.Depth},
		}, nil
	case "emptyDir", "empty":
		return workspace.Source{Type: workspace.SourceTypeEmptyDir}, nil
	default:
		return workspace.Source{}, fmt.Errorf("unknown source type %q (valid: local, git, emptyDir)", s.Type)
	}
}
