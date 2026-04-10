// Package main implements the agentd daemon entry point.
// agentd is the agent daemon that manages agent runtime via ARI.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "agentd",
		Short:        "OAR agent daemon",
		Long:         `agentd is the Open Agent Runtime daemon — it manages agent runtime via ARI.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newServerCmd())
	cmd.AddCommand(newShimCmd())
	cmd.AddCommand(newWorkspaceMcpCmd())
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
