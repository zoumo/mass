// Package server implements the "agentd server" subcommand.
package server

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// NewCommand returns the "server" cobra command.
func NewCommand() *cobra.Command {
	var rootPath string

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the agentd daemon",
		Long:  `Start the OAR agent daemon and listen for ARI connections on the configured Unix socket.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(rootPath)
		},
	}

	cmd.Flags().StringVar(&rootPath, "root", agentd.DefaultRoot, "root directory for agentd data (socket, DB, bundles, workspaces)")
	return cmd
}

func run(rootPath string) error {
	opts := agentd.Options{Root: rootPath}
	if err := opts.Validate(); err != nil {
		return err
	}

	// go-run detection: warn when the binary path looks like a temporary go build artifact.
	if self, err := os.Executable(); err == nil {
		if strings.Contains(self, "/tmp/go-build") {
			slog.Warn("agentd: running from go-run temp binary — os.Executable() path is ephemeral",
				"executable", self,
				"note", "D107: self-fork shim will use this path; use 'go build' for production")
		}
	}

	slog.Info("agentd: starting",
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
	store, err := meta.NewStore(opts.MetaDBPath())
	if err != nil {
		return err
	}
	slog.Info("agentd: metadata store initialized", "path", opts.MetaDBPath())

	// Create WorkspaceManager.
	manager := workspace.NewWorkspaceManager()
	slog.Info("agentd: workspace manager initialized")

	// Create Registry.
	registry := ari.NewRegistry()
	slog.Info("agentd: registry initialized")

	// Create AgentManager.
	agents := agentd.NewAgentManager(store)
	slog.Info("agentd: agent manager initialized")

	// Create ProcessManager (self-fork or OAR_SHIM_BINARY override).
	processes := agentd.NewProcessManager(agents, store, opts.SocketPath(), opts.BundleRoot())
	slog.Info("agentd: process manager initialized",
		"socket_path", opts.SocketPath(),
		"bundle_root", opts.BundleRoot(),
	)

	// Run recovery pass: reconnect to shims that survived a daemon restart.
	{
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := processes.RecoverSessions(recoverCtx); err != nil {
			log.Printf("agentd: session recovery failed (non-fatal): %v", err)
		} else {
			slog.Info("agentd: session recovery complete")
		}
		recoverCancel()
	}

	// Rebuild registry from DB after recovery.
	if err := registry.RebuildFromDB(store); err != nil {
		log.Printf("agentd: registry rebuild failed (non-fatal): %v", err)
	} else {
		slog.Info("agentd: registry rebuilt from database")
	}

	// Initialize workspace manager refcounts from DB.
	if err := manager.InitRefCounts(store); err != nil {
		log.Printf("agentd: workspace refcount init failed (non-fatal): %v", err)
	}

	// Create ARI Server.
	srv := ari.New(manager, registry, agents, processes, store, opts.SocketPath(), opts.WorkspaceRoot())
	slog.Info("agentd: ARI server created")

	// Start server in goroutine.
	go func() {
		slog.Info("agentd: starting ARI server", "socket", opts.SocketPath())
		if err := srv.Serve(); err != nil {
			log.Printf("agentd: server error: %v", err)
		}
	}()

	// Setup signal handler for SIGTERM/SIGINT.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	slog.Info("agentd: received signal, shutting down", "signal", sig)

	// Graceful shutdown with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("agentd: shutdown error: %v", err)
	}

	// Close metadata store.
	slog.Info("agentd: closing metadata store")
	if err := store.Close(); err != nil {
		log.Printf("agentd: metadata store close error: %v", err)
	}

	slog.Info("agentd: shutdown complete")
	return nil
}
