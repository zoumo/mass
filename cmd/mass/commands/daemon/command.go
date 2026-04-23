// Package daemon implements the "mass daemon" subcommand group.
package daemon

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/logging"
	"github.com/zoumo/mass/pkg/agentd"
)

// NewCommand returns the "daemon" cobra command with start/restart/status subcommands.
func NewCommand() *cobra.Command {
	var (
		rootPath string
		logCfg   logging.LogConfig
	)

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the mass daemon",
	}

	cmd.PersistentFlags().StringVar(&rootPath, "root", agentd.DefaultRoot(), "root directory for mass data")

	startCmd := newStartCmd(&rootPath, &logCfg)
	logCfg.AddFlags(startCmd.Flags())
	cmd.AddCommand(startCmd)
	cmd.AddCommand(newRestartCmd(&rootPath))
	cmd.AddCommand(newStatusCmd(&rootPath))
	return cmd
}
