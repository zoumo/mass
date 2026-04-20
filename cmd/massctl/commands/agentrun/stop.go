package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newStopCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:   "stop name [name ...]",
		Short: "Stop one or more agent runs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			for _, name := range args {
				if err := client.AgentRuns().Stop(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}); err != nil {
					return fmt.Errorf("stopping agentrun %s/%s: %w", ws, name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "agentrun %s/%s stopped\n", ws, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
