// Package daemon provides daemon management commands.
package daemon

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/oar/api"
	"github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
)

// NewCommand returns the "daemon" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon management commands",
	}
	cmd.AddCommand(newStatusCmd(getClient))
	return cmd
}

func newStatusCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check daemon health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				fmt.Println("daemon: not running")
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return nil
			}
			defer client.Close()

			var result ari.AgentRunListResult
			if err := client.Call(api.MethodAgentList, ari.AgentRunListParams{}, &result); err != nil {
				fmt.Println("daemon: not running")
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return nil
			}
			fmt.Println("daemon: running")
			return nil
		},
	}
}
