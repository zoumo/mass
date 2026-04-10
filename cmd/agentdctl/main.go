// Command agentdctl is a CLI for managing the agentd daemon.
// It provides commands for workspace management, agent lifecycle,
// and daemon health checks.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// socketPath is the path to the ARI Unix socket.
// Set by the root command's persistent --socket flag.
var socketPath string

// rootCmd is the base command for agentdctl.
var rootCmd = &cobra.Command{
	Use:   "agentdctl",
	Short: "CLI for agentd daemon management",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&socketPath, "socket", "/var/run/agentd/ari.sock", "ARI socket path")
}

func main() {
	// Add agent commands (defined in agent.go)
	rootCmd.AddCommand(agentCmd)

	// Add workspace commands (defined in workspace.go)
	rootCmd.AddCommand(workspaceCmd)

	// Add daemon commands (defined in daemon.go)
	rootCmd.AddCommand(daemonCmd)

	// Add shim commands (defined in shim.go)
	rootCmd.AddCommand(shimCmd)

	// Add agentrun commands (defined in agentrun.go)
	rootCmd.AddCommand(agentrunCmd)

	// Add runtime commands (defined in runtime.go)
	rootCmd.AddCommand(runtimeCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
