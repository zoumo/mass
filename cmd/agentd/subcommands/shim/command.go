// Package shim implements the "agentd shim" subcommand.
package shim

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	apiruntime "github.com/zoumo/oar/api/runtime"
	apishim "github.com/zoumo/oar/api/shim"
	"github.com/zoumo/oar/internal/logging"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/pkg/jsonrpc"
	"github.com/zoumo/oar/pkg/runtime"
	shimserver "github.com/zoumo/oar/pkg/shim/server"
	"github.com/zoumo/oar/pkg/spec"
)

// NewCommand returns the "shim" cobra command.
func NewCommand() *cobra.Command {
	var (
		bundle      string
		permissions string
		id          string
		stateDir    string
	)

	cmd := &cobra.Command{
		Use:   "shim",
		Short: "OAR Runtime shim — manages ACP agent process lifecycle",
		Long: `shim implements the OAR Runtime Specification for ACP-speaking agent processes.

It reads a config.json from the bundle directory, fork/execs the agent process,
completes the ACP initialize + session/new handshake, and exposes a JSON-RPC
server over a Unix socket for upstream management (agentd or any orchestrator).

The socket is created at <state-dir>/<id>/agent-shim.sock so agentd can
discover all running shims by scanning /run/agentd/shim/*/agent-shim.sock.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, bundle, permissions, id, stateDir)
		},
	}

	cmd.Flags().StringVar(&bundle, "bundle", "", "path to the OAR bundle directory containing config.json (required)")
	cmd.Flags().StringVar(&permissions, "permissions", "approve_all", "fs/terminal permission policy: approve_all | approve_reads | deny_all")
	cmd.Flags().StringVar(&id, "id", "", "agent session ID (auto-generated if empty)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "/run/agentd/shim", "base directory for ephemeral state files")

	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}

func run(cmd *cobra.Command, bundle, permissions, id, stateDir string) error {
	// Initialize slog from env vars inherited from agentd (OAR_LOG_LEVEL / OAR_LOG_FORMAT).
	logLevel := os.Getenv("OAR_LOG_LEVEL")
	logFormat := os.Getenv("OAR_LOG_FORMAT")
	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		level = slog.LevelInfo // invalid or empty → default info
	}
	if logFormat == "" {
		logFormat = "pretty"
	}
	handler := logging.NewHandler(logFormat, level, os.Stderr)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	cfg, err := spec.ParseConfig(bundle)
	if err != nil {
		return err
	}
	if err := spec.ValidateConfig(cfg); err != nil {
		return err
	}
	if cmd.Flag("permissions").Changed {
		cfg.Permissions = apiruntime.PermissionPolicy(permissions)
		if !cfg.Permissions.IsValid() {
			return fmt.Errorf("invalid --permissions value %q: must be one of approve_all, approve_reads, deny_all", permissions)
		}
	}

	if id == "" {
		id = cfg.Metadata.Name
	}

	shimStateDir := spec.StateDir(stateDir, id)
	socketPath := spec.ShimSocketPath(shimStateDir)
	mgr := runtime.New(cfg, bundle, shimStateDir, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// Keep the bootstrap boundary unchanged: Create finishes before the external
	// notification pipeline is visible or durable.
	if err := mgr.Create(ctx); err != nil {
		return err
	}

	logPath := spec.EventLogPath(shimStateDir)
	evLog, err := events.OpenEventLog(logPath)
	if err != nil {
		return fmt.Errorf("agent-shim: open event log: %w", err)
	}
	defer evLog.Close()

	trans := events.NewTranslator(id, mgr.Events(), evLog)
	// Inject the ACP session ID now that Create() has completed the handshake.
	trans.SetSessionID(mgr.SessionID())
	mgr.SetStateChangeHook(func(change runtime.StateChange) {
		trans.NotifyStateChange(change.PreviousStatus.String(), change.Status.String(), change.PID, change.Reason)
	})
	trans.Start()
	defer trans.Stop()

	// Build service and register it with a new jsonrpc.Server.
	svc := shimserver.New(mgr, trans, logPath, logger)
	srv := jsonrpc.NewServer(logger)
	apishim.RegisterShimService(srv, svc)

	// Remove stale socket file from a previous crash (K014).
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("agent-shim: listen %s: %w", socketPath, err)
	}
	defer ln.Close()

	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Error("rpc server error", "error", err)
		}
		cancel()
	}()

	<-ctx.Done()
	_ = ln.Close() // ensure srv.Serve returns
	return srv.Shutdown(context.Background())
}
