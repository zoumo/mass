// Package agentrun provides agentrun lifecycle management commands.
package agentrun

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// NewCommand returns the "agentrun" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agentrun",
		Aliases: []string{"ar"},
		Short:   "Agent-run lifecycle management",
		Long: `Manage agent-run lifecycle: creating → idle → running → stopped.

State machine:
  creating ──> idle ──> running ──> stopped
                │          │
                └──> error <┘

An agent run must be idle before you can send a prompt.
Poll with: massctl ar get <name> -w <workspace>`,
		Example: `  # Create and wait until idle
  massctl ar create -w my-ws --name worker --agent claude \
    --system-prompt "You are a senior engineer." --wait

  # List all agent runs in a workspace
  massctl ar get -w my-ws

  # Filter by phase
  massctl ar get -w my-ws --phase idle

  # Send a prompt and wait for full response
  massctl ar prompt worker -w my-ws --text "Fix the nil pointer in pkg/auth" --wait

  # Send a prompt fire-and-forget
  massctl ar prompt worker -w my-ws --text "Review the PR"

  # Structured task (auto-prompts agent, returns task-id)
  massctl ar task do -w my-ws --run worker --prompt "Fix the auth bug"

  # Poll task status
  massctl ar task get -w my-ws --run worker <task-id>

  # Interactive chat session
  massctl ar chat worker -w my-ws

  # Lifecycle operations
  massctl ar stop worker -w my-ws          # → stopped
  massctl ar restart worker -w my-ws       # stopped/error → idle
  massctl ar cancel worker -w my-ws        # abort current turn (running → idle)
  massctl ar delete worker -w my-ws        # remove record (must be stopped/error)`,
	}

	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(newCreateCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newStopCmd(getClient))
	cmd.AddCommand(newCancelCmd(getClient))
	cmd.AddCommand(newRestartCmd(getClient))
	cmd.AddCommand(newPromptCmd(getClient))
	cmd.AddCommand(newTaskCmd(getClient))
	cmd.AddCommand(newChatCmd(getClient))
	cmd.AddCommand(newDebugCmd())
	return cmd
}
