// Package shim implements the "agentd shim" subcommand.
package shim

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	apispec "github.com/open-agent-d/open-agent-d/api/spec"
	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/rpc"
	"github.com/open-agent-d/open-agent-d/pkg/runtime"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
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
	cmd.Flags().StringVar(&permissions, "permissions", "approve-all", "fs/terminal permission policy: approve-all | approve-reads | deny-all")
	cmd.Flags().StringVar(&id, "id", "", "agent session ID (auto-generated if empty)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "/run/agentd/shim", "base directory for ephemeral state files")

	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}

func run(cmd *cobra.Command, bundle, permissions, id, stateDir string) error {
	cfg, err := spec.ParseConfig(bundle)
	if err != nil {
		return err
	}
	if err := spec.ValidateConfig(cfg); err != nil {
		return err
	}
	if cmd.Flag("permissions").Changed {
		cfg.Permissions = apispec.PermissionPolicy(permissions)
	}

	if id == "" {
		id = cfg.Metadata.Name
	}

	shimStateDir := spec.StateDir(stateDir, id)
	socketPath := spec.ShimSocketPath(shimStateDir)
	mgr := runtime.New(cfg, bundle, shimStateDir)

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
	mgr.SetStateChangeHook(func(change runtime.StateChange) {
		trans.NotifyStateChange(change.PreviousStatus.String(), change.Status.String(), change.PID, change.Reason)
	})
	trans.Start()
	defer trans.Stop()

	srv := rpc.New(mgr, trans, socketPath, logPath)
	go func() {
		if err := srv.Serve(); err != nil {
			log.Printf("agent-shim: rpc server error: %v", err)
		}
		cancel()
	}()

	<-ctx.Done()
	return srv.Shutdown(context.Background())
}
