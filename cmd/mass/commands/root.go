// Package commands assembles the mass cobra command tree.
package commands

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/mass/commands/daemon"
	"github.com/zoumo/mass/cmd/mass/commands/run"
	"github.com/zoumo/mass/cmd/mass/commands/version"
	"github.com/zoumo/mass/cmd/mass/commands/workspacemcp"
)

// NewRootCommand returns the mass root cobra command.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "mass",
		Short:        "Multi-Agent Supervision System daemon",
		Long:         `mass is the Multi-Agent Supervision System daemon — it manages agent runtime via ARI.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(daemon.NewCommand())
	cmd.AddCommand(run.NewCommand())
	cmd.AddCommand(version.NewCommand())
	cmd.AddCommand(workspacemcp.NewCommand())
	return cmd
}
