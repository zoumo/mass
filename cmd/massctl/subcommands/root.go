// Package subcommands assembles the massctl cobra command tree.
package subcommands

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/subcommands/agent"
	"github.com/zoumo/mass/cmd/massctl/subcommands/agentrun"
	"github.com/zoumo/mass/cmd/massctl/subcommands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/subcommands/daemon"
	"github.com/zoumo/mass/cmd/massctl/subcommands/shim"
	"github.com/zoumo/mass/cmd/massctl/subcommands/up"
	"github.com/zoumo/mass/cmd/massctl/subcommands/workspace"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// NewRootCommand returns the massctl root cobra command.
func NewRootCommand() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:   "massctl",
		Short: "CLI for mass daemon management",
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", "/var/run/mass/ari.sock", "ARI socket path")

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
