package compose

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// newRunCmd returns the "compose run" subcommand that quick-starts a single
// agent run using the current directory as a local workspace.
func newRunCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		wsName       string
		agent        string
		name         string
		systemPrompt string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Quick-start a single agent run from current directory",
		Long: `run creates a local workspace from the current directory and starts a single agent run.
If the workspace already exists and is ready, it is reused.

  # Single agent (run name defaults to agent name):
  massctl compose run -w my-ws --agent claude

  # Custom run name:
  massctl compose run -w my-ws --agent claude --name my-claude

  # With system prompt:
  massctl compose run -w my-ws --agent claude --system-prompt "You are a reviewer"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}

			runName := agent
			if name != "" {
				runName = name
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()

			// Ensure workspace exists (reuse if ready, create if not).
			src := SourceConfig{Type: "local", Path: cwd}
			if err := ensureWorkspace(ctx, client, wsName, src); err != nil {
				return err
			}

			// Create agent run.
			entry := AgentRunEntry{
				Name:         runName,
				Agent:        agent,
				SystemPrompt: systemPrompt,
			}
			if err := createAgentRun(ctx, client, wsName, entry); err != nil {
				return err
			}
			if err := waitAgentIdle(ctx, client, wsName, runName); err != nil {
				return err
			}

			fmt.Println("\nAgent is ready. Socket info:")
			printSocketInfo(ctx, client, wsName, runName)
			return nil
		},
	}

	cmd.Flags().StringVarP(&wsName, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent definition name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (default: same as agent name)")
	cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt for the agent run")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("agent")

	return cmd
}
