package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newRestartCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:   "restart name [name ...]",
		Short: "Restart one or more stopped agent runs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			for _, name := range args {
				result, err := client.AgentRuns().Restart(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name})
				if err != nil {
					return fmt.Errorf("restarting agentrun %s/%s: %w", ws, name, err)
				}
				if err := cliutil.PrintJSON(cmd.OutOrStdout(), result); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
