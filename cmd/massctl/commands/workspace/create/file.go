package create

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
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

			var src workspace.Source
			switch s.Source.Type {
			case "git":
				if s.Source.URL == "" {
					return fmt.Errorf("git source requires 'source.url'")
				}
				src = workspace.Source{
					Type: workspace.SourceTypeGit,
					Git:  workspace.GitSource{URL: s.Source.URL, Ref: s.Source.Ref, Depth: s.Source.Depth},
				}
			case "emptyDir", "empty":
				src = workspace.Source{Type: workspace.SourceTypeEmptyDir}
			case "local":
				if s.Source.Path == "" {
					return fmt.Errorf("local source requires 'source.path'")
				}
				src = workspace.Source{Type: workspace.SourceTypeLocal, Local: workspace.LocalSource{Path: s.Source.Path}}
			default:
				return fmt.Errorf("unknown source type %q (valid: git, emptyDir, local)", s.Source.Type)
			}

			srcJSON, err := json.Marshal(src)
			if err != nil {
				return fmt.Errorf("marshal source: %w", err)
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ws := pkgariapi.Workspace{
				Metadata: pkgariapi.ObjectMeta{Name: s.Name},
				Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
			}
			if err := client.Create(context.Background(), &ws); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(ws)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace YAML spec file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
