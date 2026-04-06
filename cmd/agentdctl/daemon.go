// Package main provides daemon management commands for the agentdctl CLI.
// Daemon commands allow checking daemon health and status.
package main

import (
	"fmt"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/spf13/cobra"
)

// daemonCmd is the root command for daemon management operations.
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Daemon management commands",
}

// daemonStatusCmd checks if the agentd daemon is running and healthy.
var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check daemon health",
	RunE:  runDaemonStatus,
}

func init() {
	// Add subcommands to daemonCmd
	daemonCmd.AddCommand(daemonStatusCmd)
}

// runDaemonStatus checks daemon health by calling session/list.
// If the call succeeds, the daemon is running; if it fails, the daemon is not running.
func runDaemonStatus(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		// Connection error means daemon is not running
		fmt.Println("daemon: not running")
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
		return nil
	}
	defer client.Close()

	// Call session/list to verify daemon is responsive
	params := ari.SessionListParams{}
	var result ari.SessionListResult
	if err := client.Call("session/list", params, &result); err != nil {
		// RPC error means daemon is not healthy
		fmt.Println("daemon: not running")
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
		return nil
	}

	// Success means daemon is running
	fmt.Println("daemon: running")
	return nil
}