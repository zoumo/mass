package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:   "delete name [name ...]",
		Short: "Delete one or more agent runs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			for _, name := range args {
				if err := client.Delete(ctx, pkgariapi.ObjectKey{Workspace: ws, Name: name}, &pkgariapi.AgentRun{}); err != nil {
					return fmt.Errorf("deleting agentrun %s/%s: %w", ws, name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "agentrun %s/%s deleted\n", ws, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
