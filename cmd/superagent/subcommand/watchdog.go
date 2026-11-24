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
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/pkg/watchdog"
)

const composeWatchdogShutdownTimeout = 5 * time.Second

func newWatchdogAgentGuard() *cobra.Command {
	return &cobra.Command{
		Use: watchdog.AgentGuardCommand + " <executable>", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return watchdog.RunAgentGuard(args[0]) },
	}
}

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
				output, ok := cmd.OutOrStdout().(*os.File)
				if !ok || options.WorkDir == "" {
					return errors.New("managed Watchdog requires a result pipe and workspace root")
				}
				options.ProcessOutput = output
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
	cmd.Flags().StringVar(&options.WorkDir, "compose-work-dir", "", "Private Compose workspace root")
	_ = cmd.Flags().MarkHidden("compose-interval")
	_ = cmd.Flags().MarkHidden("compose-work-dir")
	return cmd
}

func runWatchdogLoop(ctx context.Context, logger *slog.Logger, output *json.Encoder, options watchdog.Options, interval time.Duration) error {
	for ctx.Err() == nil {
		result := options.Run(ctx)
		level := slog.LevelInfo
		if result.Status == "incomplete" {
			level = slog.LevelWarn
		}
		logResult := struct {
			ObservedAt         time.Time `json:"observedAt"`
			Status             string    `json:"status"`
			Reason             string    `json:"reason"`
			ReleaseUnconfirmed bool      `json:"releaseUnconfirmed,omitempty"`
			*watchdog.Delivery
		}{result.ObservedAt, result.Status, result.Reason, result.ReleaseUnconfirmed, result.Delivery}
		logger.Log(ctx, level, "Watchdog check", "component", "watchdog", "result", logResult)
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

type composeWatchdog struct {
	*childProcess
	workDir            string
	guardPID           int // Published by closing readDone.
	pending            map[int]struct{}
	readFailed         bool
	finishOnce         sync.Once
	releaseUnconfirmed bool
}

func startComposeWatchdog(executable, endpoint string, interval time.Duration, workDir string, environment []string, diagnostics io.Writer, record func(core.WatchdogCheck)) (*composeWatchdog, error) {
	roundRoot, err := os.MkdirTemp(workDir, "watchdog-")
	if err != nil {
		return nil, errors.New("cannot create Compose Watchdog workspace root")
	}
	command := exec.CommandContext(context.Background(), executable, "watchdog", "--endpoint", endpoint, "--compose-interval", interval.String(), "--compose-work-dir", roundRoot)
	command.Env = watchdog.ProcessEnvironment(environment)
	command.Stderr = diagnostics
	child, err := startChild("watchdog", command)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(roundRoot))
	}
	child.readDone = make(chan struct{})
	managed := &composeWatchdog{childProcess: child, workDir: roundRoot, pending: make(map[int]struct{})}
	go func() {
		defer close(child.readDone)
		decoder := json.NewDecoder(child.stdout)
		for {
			var entry struct {
				watchdog.AgentGuardEvent
				watchdog.Result
			}
			if err := decoder.Decode(&entry); err != nil {
				managed.readFailed = !errors.Is(err, io.EOF)
				if managed.readFailed {
					slog.New(slog.NewJSONHandler(diagnostics, nil)).Error("Cannot read Watchdog result", "component", "watchdog")
				}
				return
			}
			if entry.GuardPID > 0 {
				managed.guardPID = entry.GuardPID
				if entry.Released {
					delete(managed.pending, entry.GuardPID)
				} else {
					managed.pending[entry.GuardPID] = struct{}{}
				}
				continue
			}
			result := entry.Result
			if result.Reason == "canceled" {
				continue
			}
			check := core.WatchdogCheck{
				ObservedAt: result.ObservedAt, Status: result.Status, Reason: result.Reason,
				Judgment: result.Judgment, Recommendation: result.Recommendation, ReleaseUnconfirmed: result.ReleaseUnconfirmed,
			}
			if result.Delivery != nil {
				check.TopicID, check.IssueID = result.TopicID, result.IssueID
			}
			record(check)
		}
	}()
	return managed, nil
}

func (w *composeWatchdog) finish() bool {
	w.finishOnce.Do(func() {
		defer w.stdout.Close()
		ctx, cancel := context.WithTimeout(context.Background(), watchdog.AgentGuardTimeout)
		defer cancel()
		select {
		case <-w.readDone:
			w.releaseUnconfirmed = w.readFailed || len(w.pending) > 0
			if w.guardPID > 0 && watchdog.ConfirmAgentGuardExit(ctx, w.guardPID) != nil {
				w.releaseUnconfirmed = true
			}
		case <-ctx.Done():
			w.releaseUnconfirmed = true
		}
		if os.RemoveAll(w.workDir) != nil {
			w.releaseUnconfirmed = true
		}
	})
	return w.releaseUnconfirmed
}

func (w *composeWatchdog) stop() error {
	err := w.stopProcess(composeWatchdogShutdownTimeout)
	if w.finish() {
		return errors.Join(err, errors.New("watchdog agent process release unconfirmed"))
	}
	return err
}
