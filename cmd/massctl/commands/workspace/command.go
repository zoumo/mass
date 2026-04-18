// Package workspace provides workspace management commands.
package workspace

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace/create"
)

// NewCommand returns the "workspace" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace management",
	}

	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(create.NewCommand(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newSendCmd(getClient))
	return cmd
}
