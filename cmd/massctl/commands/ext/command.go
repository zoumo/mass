package ext

import (
	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/ext/pipeline"
)

// NewCommand returns the "ext" cobra command group.
// Extension commands are offline tools that do not require a daemon connection.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ext",
		Short: "Extension commands (no daemon required)",
	}
	cmd.AddCommand(pipeline.NewCommand())
	return cmd
}
