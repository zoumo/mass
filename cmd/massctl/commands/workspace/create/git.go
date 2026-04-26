package create

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/pkg/workspace"
)

func newGitCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		name  string
		url   string
		ref   string
		depth int
	)
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Create a workspace by cloning a git repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			src := workspace.Source{
				Type: workspace.SourceTypeGit,
				Git:  workspace.GitSource{URL: url, Ref: ref, Depth: depth},
			}
			ws, err := cliutil.CreateWorkspace(context.Background(), client, name, src)
			if err != nil {
				return err
			}
			cliutil.OutputJSON(ws)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	cmd.Flags().StringVar(&url, "url", "", "Git repository URL (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("url")
	cmd.Flags().StringVar(&ref, "ref", "", "Git reference (branch, tag, or commit)")
	cmd.Flags().IntVar(&depth, "depth", 0, "Shallow clone depth (0 = full clone)")
	return cmd
}
