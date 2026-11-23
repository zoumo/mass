package subcommand

import (
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/superagent/pkg/watchdog"
)

func newWatchdog() *cobra.Command {
	var options watchdog.Options
	cmd := &cobra.Command{
		Use:   "watchdog",
		Short: "Run one Superagent Watchdog check",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if options.Timeout <= 0 {
				return errors.New("watchdog timeout must be positive")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return json.NewEncoder(cmd.OutOrStdout()).Encode(options.Run(ctx))
		},
	}
	cmd.Flags().StringVar(&options.Endpoint, "endpoint", watchdog.DefaultEndpoint, "Core HTTP endpoint")
	cmd.Flags().StringVar(&options.CodexBin, "codex-bin", watchdog.DefaultCodexBin, "Codex app-server executable")
	cmd.Flags().DurationVar(&options.Timeout, "timeout", watchdog.DefaultTimeout, "Time limit for a Watchdog check")
	return cmd
}
