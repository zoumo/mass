package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// newNewSessionCmd implements “massctl agentrun new-session“.
//
// Opens an additional ACP session on a running agent-run — agent process
// is reused (no fork+exec), but the session has its own cwd and state.
// Returns the sessionId to stdout; pass it via subsequent “prompt
// --session-id“ etc.
func newNewSessionCmd(getClient cliutil.ClientFn) *cobra.Command {
	var ws, cwd string
	cmd := &cobra.Command{
		Use:   "new-session name",
		Short: "Open an additional ACP session on a running agent-run",
		Long: `Opens an additional ACP session on the running agent process.

The session is multiplexed onto the existing process — no new fork/exec
— but has its own cwd, model state, and message history. Returns the
agent-issued sessionId on stdout; pass it to subsequent prompt / cancel
/ end-session via --session-id.`,
		Example: `  # Open a fresh session scoped to /tmp/case-7
  sid=$(massctl ar new-session worker -w my-ws --cwd /tmp/case-7)
  massctl ar prompt worker -w my-ws --session-id "$sid" --text "Fix the bug"
  massctl ar end-session worker -w my-ws --session-id "$sid"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			out, err := client.AgentRuns().NewSession(context.Background(), &pkgariapi.AgentRunNewSessionParams{
				Workspace: ws,
				Name:      args[0],
				Cwd:       cwd,
			})
			if err != nil {
				return fmt.Errorf("opening session for %s/%s: %w", ws, args[0], err)
			}
			// Print sessionId only (script-friendly).
			fmt.Fprintln(cmd.OutOrStdout(), out.SessionID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Session cwd (required) — the working directory the agent uses for this session")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("cwd")
	return cmd
}
