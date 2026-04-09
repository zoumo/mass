// Package main implements the agentd daemon entry point.
// agentd is the agent daemon that manages agent runtime via ARI.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

func main() {
	// Parse --config flag with default.
	configPath := flag.String("config", "/etc/agentd/config.yaml", "path to config.yaml")
	flag.Parse()

	// Load configuration.
	cfg, err := agentd.ParseConfig(*configPath)
	if err != nil {
		log.Fatalf("agentd: failed to load config: %v", err)
	}

	log.Printf("agentd: loaded config from %s", *configPath)
	log.Printf("agentd: socket=%s workspaceRoot=%s", cfg.Socket, cfg.WorkspaceRoot)

	// Initialize metadata store (required for agent management).
	if cfg.MetaDB == "" {
		log.Fatalf("agentd: metadata store required for agent management (configure metaDB)")
	}

	// Create parent directory for MetaDB path if it doesn't exist.
	metaDBDir := filepath.Dir(cfg.MetaDB)
	if err := os.MkdirAll(metaDBDir, 0o755); err != nil {
		log.Fatalf("agentd: failed to create metadata database directory %s: %v", metaDBDir, err)
	}

	store, err := meta.NewStore(cfg.MetaDB)
	if err != nil {
		log.Fatalf("agentd: failed to initialize metadata store at %s: %v", cfg.MetaDB, err)
	}
	log.Printf("agentd: metadata store initialized at %s", cfg.MetaDB)

	// Create WorkspaceManager.
	manager := workspace.NewWorkspaceManager()
	log.Printf("agentd: workspace manager initialized")

	// Create Registry.
	registry := ari.NewRegistry()
	log.Printf("agentd: registry initialized")

	// Create RuntimeClassRegistry from cfg.RuntimeClasses.
	runtimeClasses, err := agentd.NewRuntimeClassRegistry(cfg.RuntimeClasses)
	if err != nil {
		log.Fatalf("agentd: failed to create runtime class registry: %v", err)
	}
	log.Printf("agentd: runtime class registry initialized")

	// Create AgentManager.
	agents := agentd.NewAgentManager(store)
	log.Printf("agentd: agent manager initialized")

	// Create ProcessManager (no sessions param in new model).
	processes := agentd.NewProcessManager(runtimeClasses, agents, store, cfg)
	log.Printf("agentd: process manager initialized")

	// Run recovery pass: reconnect to shims that survived a daemon restart.
	{
		recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := processes.RecoverSessions(recoverCtx); err != nil {
			log.Printf("agentd: session recovery failed (non-fatal): %v", err)
		} else {
			log.Printf("agentd: session recovery complete")
		}
		recoverCancel()
	}

	// Rebuild registry from DB after recovery.
	if err := registry.RebuildFromDB(store); err != nil {
		log.Printf("agentd: registry rebuild failed (non-fatal): %v", err)
	} else {
		log.Printf("agentd: registry rebuilt from database")
	}

	// Initialize workspace manager refcounts from DB.
	if err := manager.InitRefCounts(store); err != nil {
		log.Printf("agentd: workspace refcount init failed (non-fatal): %v", err)
	}

	// Create ARI Server (no sessions param).
	srv := ari.New(manager, registry, agents, processes, runtimeClasses, cfg, store, cfg.Socket, cfg.WorkspaceRoot)
	log.Printf("agentd: ARI server created")

	// Remove existing socket file if present (unclean shutdown recovery).
	if err := os.Remove(cfg.Socket); err != nil && !os.IsNotExist(err) {
		log.Fatalf("agentd: failed to remove existing socket: %v", err)
	}

	// Start server in goroutine.
	go func() {
		log.Printf("agentd: starting ARI server on %s", cfg.Socket)
		if err := srv.Serve(); err != nil {
			log.Printf("agentd: server error: %v", err)
		}
	}()

	// Setup signal handler for SIGTERM/SIGINT.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	log.Printf("agentd: received signal %v, shutting down", sig)

	// Graceful shutdown with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("agentd: shutdown error: %v", err)
	}

	// Close metadata store.
	log.Printf("agentd: closing metadata store")
	if err := store.Close(); err != nil {
		log.Printf("agentd: metadata store close error: %v", err)
	}

	log.Printf("agentd: shutdown complete")
}
