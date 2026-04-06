// Package main implements the agentd daemon entry point.
// agentd is the agent daemon that manages agent runtime sessions via ARI.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

	// Initialize metadata store if MetaDB is configured.
	var store *meta.Store
	if cfg.MetaDB != "" {
		// Create parent directory for MetaDB path if it doesn't exist.
		metaDBDir := filepath.Dir(cfg.MetaDB)
		if err := os.MkdirAll(metaDBDir, 0755); err != nil {
			log.Fatalf("agentd: failed to create metadata database directory %s: %v", metaDBDir, err)
		}

		// Initialize Store from cfg.MetaDB.
		store, err = meta.NewStore(cfg.MetaDB)
		if err != nil {
			log.Fatalf("agentd: failed to initialize metadata store at %s: %v", cfg.MetaDB, err)
		}
		log.Printf("agentd: metadata store initialized at %s", cfg.MetaDB)
	} else {
		log.Printf("agentd: metadata store not configured (metaDB field empty)")
	}

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

	// Create SessionManager from store.
	// Note: SessionManager requires a store, so if MetaDB is not configured,
	// we cannot create the SessionManager.
	if store == nil {
		log.Fatalf("agentd: metadata store required for session management (configure metaDB)")
	}
	sessions := agentd.NewSessionManager(store)
	log.Printf("agentd: session manager initialized")

	// Create ProcessManager from registry/sessions/store/cfg.
	processes := agentd.NewProcessManager(runtimeClasses, sessions, store, cfg)
	log.Printf("agentd: process manager initialized")

	// Create ARI Server with all dependencies.
	srv := ari.New(manager, registry, sessions, processes, runtimeClasses, cfg, store, cfg.Socket, cfg.WorkspaceRoot)
	log.Printf("agentd: ARI server created")

	// Remove existing socket file if present (unclean shutdown recovery).
	if _, err := os.Stat(cfg.Socket); err == nil {
		log.Printf("agentd: removing existing socket file %s", cfg.Socket)
		if err := os.Remove(cfg.Socket); err != nil {
			log.Fatalf("agentd: failed to remove existing socket: %v", err)
		}
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

	// Wait for signal.
	sig := <-sigChan
	log.Printf("agentd: received signal %v, shutting down", sig)

	// Graceful shutdown with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("agentd: shutdown error: %v", err)
	}

	// Close metadata store if initialized.
	if store != nil {
		log.Printf("agentd: closing metadata store")
		if err := store.Close(); err != nil {
			log.Printf("agentd: metadata store close error: %v", err)
		}
	}

	log.Printf("agentd: shutdown complete")
}