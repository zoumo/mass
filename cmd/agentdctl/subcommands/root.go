// Package subcommands assembles the agentdctl cobra command tree.
package subcommands

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/agent"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/agentrun"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/daemon"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/shim"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/up"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/workspace"
	ariclient "github.com/open-agent-d/open-agent-d/pkg/ari"
)

// NewRootCommand returns the agentdctl root cobra command.
func NewRootCommand() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:   "agentdctl",
		Short: "CLI for agentd daemon management",
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", "/var/run/agentd/ari.sock", "ARI socket path")

	getClient := func() (*ariclient.Client, error) {
		if socketPath == "" {
			return nil, os.ErrInvalid
		}
		return ariclient.NewClient(socketPath)
	}

	root.AddCommand(agentrun.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(agent.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(workspace.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(daemon.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(shim.NewCommand())
	root.AddCommand(up.NewCommand(cliutil.ClientFn(getClient)))
	return root
}
