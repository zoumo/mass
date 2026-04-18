package agentrun

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newRestartCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:   "restart name",
		Short: "Restart a stopped agent run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			name := args[0]
			result, err := client.AgentRuns().Restart(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name})
			if err != nil {
				return err
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
