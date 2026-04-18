// Package agent provides agent definition management commands.
package agent

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// NewCommand returns the "agent" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent definition management",
	}

	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(newApplyCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	return cmd
}
