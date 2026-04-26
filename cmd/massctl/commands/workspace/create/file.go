package create

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/pkg/workspace"
)

// workspaceSpec is the YAML shape for workspace create -f.
type workspaceSpec struct {
	Name   string          `yaml:"name"`
	Source workspaceSource `yaml:"source"`
}

type workspaceSource struct {
	Type  string `yaml:"type"`
	URL   string `yaml:"url,omitempty"`
	Ref   string `yaml:"ref,omitempty"`
	Depth int    `yaml:"depth,omitempty"`
	Path  string `yaml:"path,omitempty"`
}

func (s workspaceSource) toSource() (workspace.Source, error) {
	switch s.Type {
	case "git":
		if s.URL == "" {
			return workspace.Source{}, fmt.Errorf("git source requires 'source.url'")
		}
		return workspace.Source{
			Type: workspace.SourceTypeGit,
			Git:  workspace.GitSource{URL: s.URL, Ref: s.Ref, Depth: s.Depth},
		}, nil
	case "emptyDir", "empty":
		return workspace.Source{Type: workspace.SourceTypeEmptyDir}, nil
	case "local":
		if s.Path == "" {
			return workspace.Source{}, fmt.Errorf("local source requires 'source.path'")
		}
		return workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: s.Path},
		}, nil
	default:
		return workspace.Source{}, fmt.Errorf("unknown source type %q (valid: git, emptyDir, local)", s.Type)
	}
}

func newFileCmd(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "-f <file>",
		Short: "Create a workspace from a YAML spec file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading workspace spec %q: %w", file, err)
			}
			var s workspaceSpec
			if err := yaml.Unmarshal(data, &s); err != nil {
				return fmt.Errorf("parsing workspace spec %q: %w", file, err)
			}
			if s.Name == "" {
				return fmt.Errorf("workspace spec must have a non-empty 'name' field")
			}
			if s.Source.Type == "" {
				return fmt.Errorf("workspace spec must have a non-empty 'source.type' field")
			}

			src, err := s.Source.toSource()
			if err != nil {
				return err
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ws, err := cliutil.CreateWorkspace(context.Background(), client, s.Name, src)
			if err != nil {
				return err
			}
			cliutil.OutputJSON(ws)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace YAML spec file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
