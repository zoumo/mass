// Package run implements the "mass run" subcommand.
package run

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/logging"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	acpruntime "github.com/zoumo/mass/pkg/agentrun/runtime/acp"
	runserver "github.com/zoumo/mass/pkg/agentrun/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// NewCommand returns the "run" cobra command.
func NewCommand() *cobra.Command {
	var (
		bundle      string
		permissions string
		id          string
		stateDir    string
		logCfg      logging.LogConfig
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "MASS Agent Run — manages ACP agent process lifecycle",
		Long: `run implements the MASS Runtime Specification for ACP-speaking agent processes.

It reads a config.json from the bundle directory, fork/execs the agent process,
completes the ACP initialize + session/new handshake, and exposes a JSON-RPC
server over a Unix socket for upstream management (mass daemon or any orchestrator).

The socket is created at <state-dir>/<id>/agent-run.sock so mass can
discover all running agent-runs by scanning /run/mass/agentrun/*/agent-run.sock.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, bundle, permissions, id, stateDir, &logCfg)
		},
	}

	cmd.Flags().StringVar(&bundle, "bundle", "", "path to the MASS bundle directory containing config.json (required)")
	cmd.Flags().StringVar(&permissions, "permissions", "approve_all", "fs/terminal permission policy: approve_all | approve_reads | deny_all")
	cmd.Flags().StringVar(&id, "id", "", "agent session ID (auto-generated if empty)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "/run/mass/agentrun", "base directory for ephemeral state files")
	logCfg.AddFlags(cmd.Flags())

	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}

func run(cmd *cobra.Command, bundle, permissions, id, stateDir string, logCfg *logging.LogConfig) error {
	logger, logCleanup, err := logCfg.Build()
	if err != nil {
		return fmt.Errorf("agent-run: init logger: %w", err)
	}
	defer logCleanup()
	slog.SetDefault(logger)

	cfg, err := spec.ParseConfig(bundle)
	if err != nil {
		return err
	}
	if err := spec.ValidateConfig(cfg); err != nil {
		return err
	}
	if cmd.Flag("permissions").Changed {
		cfg.Session.Permissions = apiruntime.PermissionPolicy(permissions)
		if !cfg.Session.Permissions.IsValid() {
			return fmt.Errorf("invalid --permissions value %q: must be one of approve_all, approve_reads, deny_all", permissions)
		}
	}

	if id == "" {
		id = cfg.Metadata.Name
	}

	runStateDir := spec.StateDir(stateDir, id)
	socketPath := spec.RunSocketPath(runStateDir)
	mgr := acpruntime.New(cfg, bundle, runStateDir, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// Keep the bootstrap boundary unchanged: Create finishes before the external
	// notification pipeline is visible or durable.
	if err := mgr.Create(ctx); err != nil {
		return err
	}

	logPath := spec.EventLogPath(runStateDir)
	evLog, err := runserver.OpenEventLog(logPath)
	if err != nil {
		return fmt.Errorf("agent-run: open event log: %w", err)
	}
	defer evLog.Close()

	trans := runserver.NewTranslator(id, mgr.Events(), evLog, logger)
	// Inject the ACP session ID now that Create() has completed the handshake.
	trans.SetSessionID(mgr.SessionID())
	mgr.SetStateChangeHook(func(change acpruntime.StateChange) {
		trans.NotifyStateChange(change.PreviousStatus.String(), change.Status.String(), change.PID, change.Reason, change.SessionChanged)
	})
	// Wire session metadata hook: Translator → buildSessionUpdate → Manager.UpdateSessionMetadata.
	trans.SetSessionMetadataHook(func(ev runapi.Event) {
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
	svc := runserver.New(mgr, trans, logPath, logger)
	srv := jsonrpc.NewServer(logger)
	runserver.Register(srv, svc)

	// Remove stale socket file from a previous crash (K014).
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("agent-run: listen %s: %w", socketPath, err)
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
