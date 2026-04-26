package create

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/pkg/workspace"
)

func newLocalCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		name string
		path string
	)
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Create a workspace from a local directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			src := workspace.Source{
				Type:  workspace.SourceTypeLocal,
				Local: workspace.LocalSource{Path: path},
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
	cmd.Flags().StringVar(&path, "path", "", "Local directory path (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}
