package pipeline

import "github.com/spf13/cobra"

// NewCommand returns the "pipeline" cobra command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Pipeline file utilities",
	}
	cmd.AddCommand(newValidateCmd())
	cmd.AddCommand(newExampleCmd())
	return cmd
}
