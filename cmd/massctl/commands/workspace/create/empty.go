package create

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/workspace"
)

func newEmptyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		name string
		wait bool
	)
	cmd := &cobra.Command{
		Use:   "empty",
		Short: "Create an empty directory workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			src := workspace.Source{Type: workspace.SourceTypeEmptyDir}
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
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for workspace to become ready")
	return cmd
}
