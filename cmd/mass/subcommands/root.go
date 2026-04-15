// Package subcommands assembles the mass cobra command tree.
package subcommands

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/mass/subcommands/server"
	"github.com/zoumo/mass/cmd/mass/subcommands/shim"
	"github.com/zoumo/mass/cmd/mass/subcommands/workspacemcp"
)

// NewRootCommand returns the mass root cobra command.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "mass",
		Short:        "Multi-Agent Supervision System daemon",
		Long:         `mass is the Multi-Agent Supervision System daemon — it manages agent runtime via ARI.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(server.NewCommand())
	cmd.AddCommand(shim.NewCommand())
	cmd.AddCommand(workspacemcp.NewCommand())
	return cmd
}
