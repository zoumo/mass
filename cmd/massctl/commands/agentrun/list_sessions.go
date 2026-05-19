package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// newListSessionsCmd implements “massctl agentrun list-sessions“.
//
// Prints active session ids on an agent-run, one per line. Useful for
// scripts that drive multi-session pools and need to enumerate or
// clean up sessions.
func newListSessionsCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws string
	cmd := &cobra.Command{
		Use:     "list-sessions name",
		Aliases: []string{"ls-sessions"},
		Short:   "List active ACP session ids on an agent-run",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			out, err := client.AgentRuns().ListSessions(context.Background(),
				pkgariapi.ObjectKey{Workspace: ws, Name: args[0]})
			if err != nil {
				return fmt.Errorf("listing sessions on %s/%s: %w", ws, args[0], err)
			}
			for _, id := range out.SessionIDs {
				fmt.Fprintln(cmd.OutOrStdout(), id)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
