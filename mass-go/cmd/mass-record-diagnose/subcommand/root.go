package subcommand

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/internal/recordingtest"
)

func NewCommand() *cobra.Command {
	var options recordingtest.DiagnosticOptions
	command := &cobra.Command{
		Use: "mass-record-diagnose", Short: "Check recording prerequisites and optionally run one bounded native smoke",
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := recordingtest.Diagnose(command.Context(), options)
			fmt.Fprintf(command.OutOrStdout(), "provider=%s status=%s report=%s/diagnosis.json\n", options.Provider, report.Status, options.Output)
			return err
		},
	}
	command.Flags().StringVar(&options.Provider, "provider", "", "codex or claude")
	command.Flags().StringVar(&options.Executable, "executable", "", "Explicit native CLI executable")
	command.Flags().StringVar(&options.Model, "model", "", "Explicit model; otherwise MASS_RECORD_MODEL")
	command.Flags().StringVar(&options.Output, "output", "", "Absolute evidence directory that must not exist")
	command.Flags().BoolVar(&options.Smoke, "smoke", false, "Opt in to one real no-tool native model request (60 seconds)")
	return command
}
