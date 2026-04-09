// Package ari_test tests the ARI JSON-RPC server stub.
// The full integration tests will be added in S03 when server.go is implemented.
// TODO(S03): replace with full integration test suite
package ari_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// TestServerStubServeShutdown verifies that the stub server can be created,
// Serve returns nil, and Shutdown returns nil.
func TestServerStubServeShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := meta.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	manager := workspace.NewWorkspaceManager()
	registry := ari.NewRegistry()

	runtimeClasses, err := agentd.NewRuntimeClassRegistry(nil)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry: %v", err)
	}

	agents := agentd.NewAgentManager(store)
	cfg := agentd.Config{}
	processes := agentd.NewProcessManager(runtimeClasses, agents, store, cfg)

	srv := ari.New(manager, registry, agents, processes, runtimeClasses, cfg, store, "/tmp/test.sock", "/tmp")

	if err := srv.Serve(); err != nil {
		t.Errorf("Serve() returned error: %v", err)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() returned error: %v", err)
	}
}
