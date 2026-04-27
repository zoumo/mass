// Package workspace provides workspace management commands.
package workspace

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace/create"
)

// NewCommand returns the "workspace" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Workspace management",
		Long: `Manage workspaces – isolated working directories bound to agent runs.

A workspace must reach phase "ready" before agent runs can be created in it.
Poll with: massctl ws get <name>

Source types:
  local    mount an existing local directory (mass will not delete it)
  git      clone a git repository (mass manages the directory)
  empty    create an empty directory`,
		Example: `  # Create from local directory and wait until ready
  massctl ws create local --name my-ws --path /path/to/code --wait

  # Clone a git repo
  massctl ws create git --name my-ws --url https://github.com/org/repo.git --ref main --wait

  # List all workspaces
  massctl ws get

  # Get a specific workspace (check phase)
  massctl ws get my-ws

  # Delete (all agent runs must be deleted first)
  massctl ws delete my-ws`,
	}

	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(create.NewCommand(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newSendCmd(getClient))
	return cmd
}
