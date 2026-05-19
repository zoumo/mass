package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// newEndSessionCmd implements “massctl agentrun end-session“.
//
// Releases runtime tracking of a session id. The agent process keeps its
// per-session state until cancelled or process exits — ACP has no
// explicit end-session RPC. Refuses to end an agent's initial session
// (the one created by the agentrun handshake); kill the whole agent
// instead via “stop“.
func newEndSessionCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws, sessionID string
	cmd := &cobra.Command{
		Use:   "end-session name",
		Short: "Release runtime tracking of an ACP session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.AgentRuns().EndSession(context.Background(),
				pkgariapi.ObjectKey{Workspace: ws, Name: args[0]}, sessionID); err != nil {
				return fmt.Errorf("ending session %s on %s/%s: %w", sessionID, ws, args[0], err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session %s ended on %s/%s\n", sessionID, ws, args[0])
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Session id to end (required) — must not be the initial session")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("session-id")
	return cmd
}
