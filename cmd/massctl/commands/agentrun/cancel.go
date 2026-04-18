package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newCancelCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:   "cancel name",
		Short: "Cancel current agent run prompt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			name := args[0]
			if err := client.AgentRuns().Cancel(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agentrun %s/%s cancel requested\n", ws, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
