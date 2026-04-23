// Package commands assembles the massctl cobra command tree.
package commands

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/agent"
	"github.com/zoumo/mass/cmd/massctl/commands/agentrun"
	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/commands/compose"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace"
	"github.com/zoumo/mass/pkg/agentd"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// NewRootCommand returns the massctl root cobra command.
func NewRootCommand() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:          "massctl",
		Short:        "CLI for mass daemon management",
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", filepath.Join(agentd.DefaultRoot(), "mass.sock"), "ARI socket path")

	getClient := func() (pkgariapi.Client, error) {
		if socketPath == "" {
			return nil, os.ErrInvalid
		}
		return ariclient.Dial(context.Background(), socketPath)
	}

	root.AddCommand(agentrun.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(agent.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(workspace.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(compose.NewCommand(cliutil.ClientFn(getClient)))
	return root
}
