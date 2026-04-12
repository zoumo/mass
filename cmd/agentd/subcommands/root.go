// Package subcommands assembles the agentd cobra command tree.
package subcommands

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/oar/cmd/agentd/subcommands/server"
	"github.com/zoumo/oar/cmd/agentd/subcommands/shim"
	"github.com/zoumo/oar/cmd/agentd/subcommands/workspacemcp"
)

// NewRootCommand returns the agentd root cobra command.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "agentd",
		Short:        "OAR agent daemon",
		Long:         `agentd is the Open Agent Runtime daemon — it manages agent runtime via ARI.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(server.NewCommand())
	cmd.AddCommand(shim.NewCommand())
	cmd.AddCommand(workspacemcp.NewCommand())
	return cmd
}
