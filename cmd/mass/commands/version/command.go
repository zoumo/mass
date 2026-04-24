package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/version"
)

// NewCommand returns the version subcommand.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("mass version:", version.Full())
			if version.BuildTime != "" {
				fmt.Println("build time:", version.BuildTime)
			}
		},
	}
}
