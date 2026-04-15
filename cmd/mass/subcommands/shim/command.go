// Package shim implements the "mass shim" subcommand.
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

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/internal/logging"
	"github.com/zoumo/mass/pkg/jsonrpc"
	acpruntime "github.com/zoumo/mass/pkg/shim/runtime/acp"
	shimserver "github.com/zoumo/mass/pkg/shim/server"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
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
		Short: "MASS Runtime shim — manages ACP agent process lifecycle",
		Long: `shim implements the MASS Runtime Specification for ACP-speaking agent processes.

It reads a config.json from the bundle directory, fork/execs the agent process,
completes the ACP initialize + session/new handshake, and exposes a JSON-RPC
server over a Unix socket for upstream management (mass daemon or any orchestrator).

The socket is created at <state-dir>/<id>/agent-shim.sock so mass can
discover all running shims by scanning /run/mass/shim/*/agent-shim.sock.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, bundle, permissions, id, stateDir)
		},
	}

	cmd.Flags().StringVar(&bundle, "bundle", "", "path to the MASS bundle directory containing config.json (required)")
	cmd.Flags().StringVar(&permissions, "permissions", "approve_all", "fs/terminal permission policy: approve_all | approve_reads | deny_all")
	cmd.Flags().StringVar(&id, "id", "", "agent session ID (auto-generated if empty)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "/run/mass/shim", "base directory for ephemeral state files")

	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}

func run(cmd *cobra.Command, bundle, permissions, id, stateDir string) error {
	// Initialize slog from env vars inherited from mass daemon (MASS_LOG_LEVEL / MASS_LOG_FORMAT).
	logLevel := os.Getenv("MASS_LOG_LEVEL")
	logFormat := os.Getenv("MASS_LOG_FORMAT")
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
	mgr := acpruntime.New(cfg, bundle, shimStateDir, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// Keep the bootstrap boundary unchanged: Create finishes before the external
	// notification pipeline is visible or durable.
	if err := mgr.Create(ctx); err != nil {
		return err
	}

	logPath := spec.EventLogPath(shimStateDir)
	evLog, err := shimserver.OpenEventLog(logPath)
	if err != nil {
		return fmt.Errorf("agent-shim: open event log: %w", err)
	}
	defer evLog.Close()

	trans := shimserver.NewTranslator(id, mgr.Events(), evLog)
	// Inject the ACP session ID now that Create() has completed the handshake.
	trans.SetSessionID(mgr.SessionID())
	mgr.SetStateChangeHook(func(change acpruntime.StateChange) {
		trans.NotifyStateChange(change.PreviousStatus.String(), change.Status.String(), change.PID, change.Reason, change.SessionChanged)
	})
	// Wire session metadata hook: Translator → buildSessionUpdate → Manager.UpdateSessionMetadata.
	trans.SetSessionMetadataHook(func(ev apishim.Event) {
		changed, reason, apply := buildSessionUpdate(ev)
		if changed == nil {
			return
		}
		if err := mgr.UpdateSessionMetadata(changed, reason, apply); err != nil {
			logger.Error("session metadata update failed", "changed", changed, "error", err)
		}
	})
	mgr.SetEventCountsFn(trans.EventCounts)
	trans.Start()
	defer trans.Stop()

	// Emit synthetic bootstrap-metadata so subscribers discover agent identity
	// and capabilities via history backfill (D124).
	{
		st, _ := mgr.GetState()
		trans.NotifyStateChange("idle", "idle", st.PID, "bootstrap-metadata", []string{"agentInfo", "capabilities"})
	}

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
