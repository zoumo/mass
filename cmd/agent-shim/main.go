package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/rpc"
	"github.com/open-agent-d/open-agent-d/pkg/runtime"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/spf13/cobra"
)

// flags
var (
	flagBundle      string
	flagPermissions string
	flagID          string
	flagStateDir    string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-shim",
		Short: "OAR Runtime shim — manages ACP agent process lifecycle",
		Long: `agent-shim implements the OAR Runtime Specification for ACP-speaking agent processes.

It reads a config.json from the bundle directory, fork/execs the agent process,
completes the ACP initialize + session/new handshake, and exposes a JSON-RPC
server over a Unix socket for upstream management (agentd or any orchestrator).

The socket is created at <state-dir>/<id>/agent-shim.sock so agentd can
discover all running shims by scanning /run/agentd/shim/*/agent-shim.sock.`,
		SilenceUsage: true,
		RunE:         run,
	}

	cmd.Flags().StringVar(&flagBundle, "bundle", "", "path to the OAR bundle directory containing config.json (required)")
	cmd.Flags().StringVar(&flagPermissions, "permissions", "approve-all", "fs/terminal permission policy: approve-all | approve-reads | deny-all")
	cmd.Flags().StringVar(&flagID, "id", "", "agent session ID (auto-generated if empty)")
	cmd.Flags().StringVar(&flagStateDir, "state-dir", "/run/agentd/shim", "base directory for ephemeral state files")

	_ = cmd.MarkFlagRequired("bundle")

	return cmd
}

func run(cmd *cobra.Command, args []string) error {
	// 1. Parse config.json from the bundle directory.
	cfg, err := spec.ParseConfig(flagBundle)
	if err != nil {
		return err
	}

	// 2. Validate the parsed config.
	if err := spec.ValidateConfig(cfg); err != nil {
		return err
	}

	// 3. Apply --permissions only if the user explicitly supplied it.
	if cmd.Flag("permissions").Changed {
		cfg.Permissions = spec.PermissionPolicy(flagPermissions)
	}

	// 4. Resolve agent session ID (flag overrides metadata.name).
	id := flagID
	if id == "" {
		id = cfg.Metadata.Name
	}

	// 5. Derive the state directory and socket path for this session.
	// Both live under <state-dir>/<id>/ so agentd can discover all running
	// shims by scanning /run/agentd/shim/*/agent-shim.sock after a restart.
	stateDir := spec.StateDir(flagStateDir, id)
	socketPath := spec.ShimSocketPath(stateDir)

	// 6. Create the runtime Manager (does not start the agent yet).
	// bundleDir and stateDir are kept separate: bundleDir holds config.json and
	// agentRoot (prepared by agentd), stateDir holds ephemeral runtime state
	// (state.json, events.jsonl, agent-shim.sock).
	mgr := runtime.New(cfg, flagBundle, stateDir)

	// 7. Install signal context so SIGTERM/SIGINT triggers graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// 8. Start the agent process and complete the ACP handshake.
	if err := mgr.Create(ctx); err != nil {
		return err
	}

	// 9. Open the durable event log (stateDir/events.jsonl).
	// All events are appended here so callers can replay history after reconnect.
	logPath := spec.EventLogPath(stateDir)
	evLog, err := events.OpenEventLog(logPath)
	if err != nil {
		return fmt.Errorf("agent-shim: open event log: %w", err)
	}
	defer evLog.Close()

	// 10. Start the event translator (drains mgr.Events() and fans out to subscribers).
	trans := events.NewTranslator(mgr.Events(), evLog)
	trans.Start()
	defer trans.Stop()

	// 11. Create the JSON-RPC server.
	srv := rpc.New(mgr, trans, socketPath, logPath)

	// 12. Accept connections in the background.
	// When Serve() returns (due to Shutdown RPC or error), cancel the context
	// to trigger graceful shutdown of the main goroutine.
	go func() {
		if err := srv.Serve(); err != nil {
			log.Printf("agent-shim: rpc server error: %v", err)
		}
		// Cancel context to signal main goroutine to exit.
		// This handles the case where Shutdown is called via RPC.
		cancel()
	}()

	// 13. Block until SIGTERM/SIGINT or RPC server shutdown.
	<-ctx.Done()

	// 14. Graceful shutdown: kill agent and close listener.
	shutdownCtx := context.Background()
	return srv.Shutdown(shutdownCtx)
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
