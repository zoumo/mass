// Package server implements the "mass server" subcommand.
package server

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/logging"
	"github.com/zoumo/mass/pkg/agentd"
	ariserver "github.com/zoumo/mass/pkg/ari/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/agentd/store"
	"github.com/zoumo/mass/pkg/workspace"
)

// NewCommand returns the "server" cobra command.
func NewCommand() *cobra.Command {
	var (
		rootPath  string
		logLevel  string
		logFormat string
	)

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the mass daemon",
		Long:  `Start the MASS agent daemon and listen for ARI connections on the configured Unix socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(rootPath, logLevel, logFormat)
		},
	}

	cmd.Flags().StringVar(&rootPath, "root", agentd.DefaultRoot, "root directory for mass data (socket, DB, bundles, workspaces)")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	cmd.Flags().StringVar(&logFormat, "log-format", "pretty", "log format (pretty, text, json)")
	return cmd
}

func run(rootPath, logLevel, logFormat string) error {
	// Configure logger before anything else.
	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	handler := logging.NewHandler(logFormat, level, os.Stderr)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	opts := agentd.Options{Root: rootPath}
	if err := opts.Validate(); err != nil {
		return err
	}

	// go-run detection: warn when the binary path looks like a temporary go build artifact.
	if self, err := os.Executable(); err == nil {
		if strings.Contains(self, "/tmp/go-build") {
			logger.Warn("running from go-run temp binary — os.Executable() path is ephemeral",
				"executable", self,
				"note", "D107: self-fork shim will use this path; use 'go build' for production")
		}
	}

	logger.Info("mass daemon starting",
		"root", opts.Root,
		"socket", opts.SocketPath(),
		"workspace_root", opts.WorkspaceRoot(),
		"bundle_root", opts.BundleRoot(),
		"meta_db", opts.MetaDBPath(),
	)

	// Create all required subdirectories.
	for _, dir := range []string{opts.Root, opts.WorkspaceRoot(), opts.BundleRoot()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Initialize metadata store.
	metaStore, err := store.NewStore(opts.MetaDBPath(), logger)
	if err != nil {
		return err
	}
	logger.Info("metadata store initialized", "path", opts.MetaDBPath())

	// Create WorkspaceManager.
	manager := workspace.NewWorkspaceManager()
	logger.Info("workspace manager initialized")

	// Create AgentRunManager.
	agents := agentd.NewAgentRunManager(metaStore, logger)
	logger.Info("agent run manager initialized")

	// Create ProcessManager (self-fork or MASS_SHIM_BINARY override).
	processes := agentd.NewProcessManager(agents, metaStore, opts.SocketPath(), opts.BundleRoot(), logger, logLevel, logFormat)
	logger.Info("process manager initialized",
		"socket_path", opts.SocketPath(),
		"bundle_root", opts.BundleRoot(),
	)

	// Run recovery pass: reconnect to shims that survived a daemon restart.
	{
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := processes.RecoverSessions(recoverCtx); err != nil {
			logger.Warn("session recovery failed (non-fatal)", "error", err)
		} else {
			logger.Info("session recovery complete")
		}
		recoverCancel()
	}

	// Initialize workspace manager refcounts from DB.
	if err := manager.InitRefCounts(metaStore); err != nil {
		logger.Warn("workspace refcount init failed (non-fatal)", "error", err)
	}

	// Create ARI service and wire it to a new jsonrpc.Server.
	svc := ariserver.New(manager, agents, processes, metaStore, opts.WorkspaceRoot(), logger)
	srv := jsonrpc.NewServer(logger)
	ariserver.Register(srv, svc)
	logger.Info("ARI server created")

	// Remove stale socket file from a previous crash (K014).
	_ = os.Remove(opts.SocketPath())

	ln, err := net.Listen("unix", opts.SocketPath())
	if err != nil {
		return err
	}
	defer ln.Close()

	// Start server in goroutine.
	go func() {
		logger.Info("starting ARI server", "socket", opts.SocketPath())
		if err := srv.Serve(ln); err != nil {
			logger.Error("server error", "error", err)
		}
	}()

	// Setup signal handler for SIGTERM/SIGINT.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	logger.Info("received signal, shutting down", "signal", sig)

	// Graceful shutdown with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = ln.Close() // close listener so srv.Serve returns
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	// Close metadata store.
	logger.Info("closing metadata store")
	if err := metaStore.Close(); err != nil {
		logger.Error("metadata store close error", "error", err)
	}

	logger.Info("shutdown complete")
	return nil
}
