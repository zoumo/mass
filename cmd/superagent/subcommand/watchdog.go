package subcommand

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/pkg/watchdog"
)

const composeWatchdogShutdownTimeout = 5 * time.Second

func newWatchdog() *cobra.Command {
	var options watchdog.Options
	var interval time.Duration
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
			if cmd.Flags().Changed("compose-interval") {
				if interval <= 0 {
					return errors.New("watchdog interval must be positive")
				}
				logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil))
				return runWatchdogLoop(ctx, logger, json.NewEncoder(cmd.OutOrStdout()), options, interval)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(options.Run(ctx))
		},
	}
	cmd.Flags().StringVar(&options.Endpoint, "endpoint", watchdog.DefaultEndpoint, "Core HTTP endpoint")
	cmd.Flags().StringVar(&options.CodexBin, "codex-bin", watchdog.DefaultCodexBin, "Codex app-server executable")
	cmd.Flags().DurationVar(&options.Timeout, "timeout", watchdog.DefaultTimeout, "Time limit for a Watchdog check")
	cmd.Flags().DurationVar(&interval, "compose-interval", 0, "Wait between Compose-managed checks")
	_ = cmd.Flags().MarkHidden("compose-interval")
	return cmd
}

func runWatchdogLoop(ctx context.Context, logger *slog.Logger, output *json.Encoder, options watchdog.Options, interval time.Duration) error {
	for ctx.Err() == nil {
		result := options.Run(ctx)
		level := slog.LevelInfo
		if result.Status == "incomplete" {
			level = slog.LevelWarn
		}
		logger.Log(ctx, level, "Watchdog check", "component", "watchdog", "result", result)
		if err := output.Encode(result); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func startComposeWatchdog(executable, endpoint string, interval time.Duration, environment []string, diagnostics io.Writer, record func(core.WatchdogCheck)) (*childProcess, error) {
	command := exec.CommandContext(context.Background(), executable, "watchdog", "--endpoint", endpoint, "--compose-interval", interval.String())
	command.Env = watchdog.ProcessEnvironment(environment)
	command.Stderr = diagnostics
	child, err := startChild("watchdog", command)
	if err != nil {
		return nil, err
	}
	child.readDone = make(chan struct{})
	go func() {
		defer close(child.readDone)
		decoder := json.NewDecoder(child.stdout)
		for {
			var result watchdog.Result
			if err := decoder.Decode(&result); err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
					slog.New(slog.NewJSONHandler(diagnostics, nil)).Error("Cannot read Watchdog result", "component", "watchdog")
				}
				return
			}
			check := core.WatchdogCheck{
				ObservedAt: result.ObservedAt, Status: result.Status, Reason: result.Reason,
				Judgment: result.Judgment, Recommendation: result.Recommendation,
			}
			if result.Delivery != nil {
				check.TopicID, check.IssueID = result.TopicID, result.IssueID
			}
			record(check)
		}
	}()
	return child, nil
}
