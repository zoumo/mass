package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/version"
)

// NewCommand returns the version subcommand.
func NewCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if jsonOutput {
				printJSON()
			} else {
				printText()
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func printText() {
	fmt.Println("version:    ", version.Version)
	fmt.Println("git commit: ", version.GitCommit)
	fmt.Println("go version: ", version.GoVersion)
	if version.BuildTime != "" {
		fmt.Println("build time: ", version.BuildTime)
	}
}

func printJSON() {
	fmt.Printf(`{"version":"%s","gitCommit":"%s","goVersion":"%s","buildTime":"%s"}
`, version.Version, version.GitCommit, version.GoVersion, version.BuildTime)
}
