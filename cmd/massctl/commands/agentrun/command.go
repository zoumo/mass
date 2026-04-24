// Package agentrun provides agentrun lifecycle management commands.
package agentrun

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// NewCommand returns the "agentrun" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentrun",
		Short: "Agent-run lifecycle management",
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
