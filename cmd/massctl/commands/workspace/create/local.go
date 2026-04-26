package create

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/workspace"
)

func newLocalCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		name string
		path string
		wait bool
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

			ctx := context.Background()
			src := workspace.Source{
				Type:  workspace.SourceTypeLocal,
				Local: workspace.LocalSource{Path: path},
			}
			ws, err := cliutil.CreateWorkspace(ctx, client, name, src)
			if err != nil {
				return err
			}
			if wait {
				if err := cliutil.WaitWorkspaceReady(ctx, client, name); err != nil {
					return err
				}
				if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, ws); err != nil {
					return err
				}
			}
			cliutil.OutputJSON(ws)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	cmd.Flags().StringVar(&path, "path", "", "Local directory path (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("path")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for workspace to become ready")
	return cmd
}
