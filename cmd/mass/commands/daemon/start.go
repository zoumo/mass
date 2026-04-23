package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/internal/logging"
	"github.com/zoumo/mass/pkg/agentd"
	"github.com/zoumo/mass/pkg/agentd/store"
	ariserver "github.com/zoumo/mass/pkg/ari/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/workspace"
)

func newStartCmd(rootPath *string, logCfg *logging.LogConfig) *cobra.Command {
	return &cobra.Command{
		Use:          "start",
		Short:        "Start the mass daemon",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(*rootPath, logCfg)
		},
	}
}

func runStart(rootPath string, logCfg *logging.LogConfig) error {
	logCfg.Filename = "mass-server.log"
	logCfg.SetDefaultPath(filepath.Join(rootPath, "logs"))

	logger, logCleanup, err := logCfg.Build()
	if err != nil {
		return err
	}
	defer logCleanup()
	slog.SetDefault(logger)

	opts := agentd.Options{Root: rootPath}
	if err := opts.Validate(); err != nil {
		return err
	}

	// go-run detection.
	if self, err := os.Executable(); err == nil {
		if strings.Contains(self, "/tmp/go-build") {
			logger.Warn("running from go-run temp binary — os.Executable() path is ephemeral",
				"executable", self,
				"note", "D107: self-fork agent-run will use this path; use 'go build' for production")
		}
	}

	logger.Info("mass daemon starting",
		"root", opts.Root,
		"socket", opts.SocketPath(),
		"workspace_root", opts.WorkspaceRoot(),
		"agentrun_root", opts.AgentRunRoot(),
		"meta_db", opts.MetaDBPath(),
	)

	// Create all required subdirectories.
	for _, dir := range []string{opts.Root, opts.WorkspaceRoot(), opts.AgentRunRoot(), filepath.Join(opts.Root, "logs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Write PID file.
	pidPath := opts.PidFilePath()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	defer os.Remove(pidPath)

	// Initialize metadata store.
	metaStore, err := store.NewStore(opts.MetaDBPath(), logger)
	if err != nil {
		return err
	}
	logger.Info("metadata store initialized", "path", opts.MetaDBPath())

	if err := agentd.EnsureBuiltinAgents(context.Background(), metaStore, logger); err != nil {
		return fmt.Errorf("seed builtin agents: %w", err)
	}

	manager := workspace.NewWorkspaceManager()
	agents := agentd.NewAgentRunManager(metaStore, logger)
	processes := agentd.NewProcessManager(agents, metaStore, opts.SocketPath(), opts.AgentRunRoot(), logger, logCfg.Level, logCfg.Format)

	// Recovery pass.
	{
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := processes.RecoverSessions(recoverCtx); err != nil {
			logger.Warn("session recovery failed (non-fatal)", "error", err)
		} else {
			logger.Info("session recovery complete")
		}
		recoverCancel()
	}

	if err := manager.InitRefCounts(metaStore); err != nil {
		logger.Warn("workspace refcount init failed (non-fatal)", "error", err)
	}

	svc := ariserver.New(manager, agents, processes, metaStore, opts.WorkspaceRoot(), logger)
	srv := jsonrpc.NewServer(logger)
	ariserver.Register(srv, svc)

	_ = os.Remove(opts.SocketPath())

	ln, err := net.Listen("unix", opts.SocketPath())
	if err != nil {
		return err
	}
	defer ln.Close()

	go func() {
		logger.Info("starting ARI server", "socket", opts.SocketPath())
		if err := srv.Serve(ln); err != nil {
			logger.Error("server error", "error", err)
		}
	}()

	// Signal handling: SIGTERM/SIGINT → shutdown, SIGHUP → restart (re-exec).
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	sig := <-sigChan
	logger.Info("received signal", "signal", sig)

	// Graceful shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
	_ = ln.Close()

	logger.Info("closing metadata store")
	if err := metaStore.Close(); err != nil {
		logger.Error("metadata store close error", "error", err)
	}

	if sig == syscall.SIGHUP {
		logger.Info("re-executing for restart")
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("os.Executable: %w", err)
		}
		// syscall.Exec replaces the process image; PID stays the same.
		// Defers do not run after Exec — PID file persists (same PID).
		return syscall.Exec(self, os.Args, os.Environ())
	}

	logger.Info("shutdown complete")
	return nil
}
