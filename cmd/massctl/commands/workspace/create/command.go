// Package create implements the "workspace create" subcommand group.
package create

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// NewCommand returns the "workspace create" cobra command group.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a workspace",
		Long: `Create a workspace from a source.

Subcommands:
  local  --name <name> --path <path>
  git    --name <name> --url <url> [--ref <ref>] [--depth <n>]
  empty  --name <name>
  -f     <file>   (full spec YAML)`,
	}
	cmd.AddCommand(newLocalCmd(getClient))
	cmd.AddCommand(newGitCmd(getClient))
	cmd.AddCommand(newEmptyCmd(getClient))
	cmd.AddCommand(newFileCmd(getClient))
	return cmd
}
