// Package ari_test tests the ARI JSON-RPC server integration.
// These tests exercise workspace/prepare, workspace/list, workspace/cleanup methods
// over a real Unix socket connection using the jsonrpc2 client.
package ari_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"
)

// ────────────────────────────────────────────────────────────────────────────
// Test Harness
// ────────────────────────────────────────────────────────────────────────────

// testHarness brings up WorkspaceManager + Registry + Server for one test.
// Pattern follows pkg/rpc/server_test.go.
type testHarness struct {
	manager        *workspace.WorkspaceManager
	registry       *ari.Registry
	store          *meta.Store
	runtimeClasses *agentd.RuntimeClassRegistry
	sessions       *agentd.SessionManager
	agents         *agentd.AgentManager
	processes      *agentd.ProcessManager
	server         *ari.Server
	socket         string
	baseDir        string

	// serveErr receives the error from server.Serve when the server exits.
	serveErr chan error
}

// newTestHarness creates a test harness with a running ARI server.
// The server listens on a Unix socket in a temp directory.
// Cleanup is registered via t.Cleanup to shut down server and remove temp dirs.
func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	// Create temp baseDir for workspace creation.
	baseDir, err := os.MkdirTemp("", "ari-test-base-")
	require.NoError(t, err, "failed to create baseDir")

	// Create temp socket directory (keep path short for macOS sun_path limit).
	sockDir, err := os.MkdirTemp("", "ari-sock-")
	require.NoError(t, err, "failed to create sockDir")
	socketPath := filepath.Join(sockDir, "ari.sock")

	// Create temp database for meta store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := meta.NewStore(dbPath)
	require.NoError(t, err, "failed to create meta store")

	// Create WorkspaceManager.
	manager := workspace.NewWorkspaceManager()

	// Create Registry.
	registry := ari.NewRegistry()

	// Create RuntimeClassRegistry (empty for workspace tests).
	runtimeClasses, err := agentd.NewRuntimeClassRegistry(nil)
	require.NoError(t, err, "failed to create runtime class registry")

	// Create SessionManager.
	sessions := agentd.NewSessionManager(store)

	// Create AgentManager.
	agents := agentd.NewAgentManager(store)

	// Create ProcessManager.
	cfg := agentd.Config{
		Socket:        socketPath,
		WorkspaceRoot: baseDir,
	}
	processes := agentd.NewProcessManager(runtimeClasses, sessions, store, cfg)

	// Create Server with all dependencies.
	server := ari.New(manager, registry, sessions, agents, processes, runtimeClasses, cfg, store, socketPath, baseDir)

	// Start Serve goroutine.
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()

	// Wait until the socket file exists (server is accepting).
	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "server socket did not appear")

	h := &testHarness{
		manager:        manager,
		registry:       registry,
		store:          store,
		runtimeClasses: runtimeClasses,
		sessions:       sessions,
		agents:         agents,
		processes:      processes,
		server:         server,
		socket:         socketPath,
		baseDir:        baseDir,
		serveErr:       serveErr,
	}

	// Cleanup: shut down server, wait for Serve to exit, remove temp dirs.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)

		// Wait for Serve to exit.
		select {
		case <-serveErr:
		case <-ctx.Done():
		}

		// Close store.
		_ = store.Close()

		// Remove temp dirs.
		_ = os.RemoveAll(sockDir)
		_ = os.RemoveAll(baseDir)
	})

	return h
}

// newSessionTestHarness creates a test harness with mockagent runtime class.
// This variant is needed for session/* tests that require a real runtime.
func newSessionTestHarness(t *testing.T) *testHarness {
	t.Helper()

	// Build and find binaries.
	shimBinary := findShimBinary(t)
	mockagentBinary := findMockagentBinary(t)

	// Set OAR_SHIM_BINARY env var so ProcessManager finds the shim binary.
	t.Setenv("OAR_SHIM_BINARY", shimBinary)

	// Create temp baseDir for workspace creation.
	baseDir, err := os.MkdirTemp("", "ari-session-test-base-")
	require.NoError(t, err, "failed to create baseDir")

	// Create temp socket directory (keep path short for macOS sun_path limit).
	sockDir, err := os.MkdirTemp("", "ari-sock-")
	require.NoError(t, err, "failed to create sockDir")
	socketPath := filepath.Join(sockDir, "ari.sock")

	// Create temp database for meta store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := meta.NewStore(dbPath)
	require.NoError(t, err, "failed to create meta store")

	// Create WorkspaceManager.
	manager := workspace.NewWorkspaceManager()

	// Create Registry.
	registry := ari.NewRegistry()

	// Create RuntimeClassRegistry with mockagent.
	runtimeClasses := map[string]agentd.RuntimeClassConfig{
		"mockagent": {
			Command: mockagentBinary,
			Args:    []string{},
			Env:     map[string]string{},
			Capabilities: agentd.CapabilitiesConfig{
				Streaming:          true,
				SessionLoad:        false,
				ConcurrentSessions: 1,
			},
		},
	}
	runtimeRegistry, err := agentd.NewRuntimeClassRegistry(runtimeClasses)
	require.NoError(t, err, "failed to create runtime class registry")

	// Create SessionManager.
	sessions := agentd.NewSessionManager(store)

	// Create AgentManager.
	agents := agentd.NewAgentManager(store)

	// Create ProcessManager.
	cfg := agentd.Config{
		Socket:        socketPath,
		WorkspaceRoot: baseDir,
		Runtime: agentd.RuntimeConfig{
			DefaultClass: "mockagent",
		},
		SessionPolicy: agentd.SessionPolicyConfig{
			MaxSessions: 10,
		},
	}
	processes := agentd.NewProcessManager(runtimeRegistry, sessions, store, cfg)

	// Create Server with all dependencies.
	server := ari.New(manager, registry, sessions, agents, processes, runtimeRegistry, cfg, store, socketPath, baseDir)

	// Start Serve goroutine.
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()

	// Wait until the socket file exists (server is accepting).
	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "server socket did not appear")

	h := &testHarness{
		manager:        manager,
		registry:       registry,
		store:          store,
		runtimeClasses: runtimeRegistry,
		sessions:       sessions,
		agents:         agents,
		processes:      processes,
		server:         server,
		socket:         socketPath,
		baseDir:        baseDir,
		serveErr:       serveErr,
	}

	// Cleanup: shut down server, stop any running sessions, wait for Serve to exit.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Stop any running sessions.
		for _, sessionID := range processes.ListProcesses() {
			_ = processes.Stop(ctx, sessionID)
		}

		_ = server.Shutdown(ctx)

		// Wait for Serve to exit.
		select {
		case <-serveErr:
		case <-ctx.Done():
		}

		// Close store.
		_ = store.Close()

		// Remove temp dirs.
		_ = os.RemoveAll(sockDir)
		_ = os.RemoveAll(baseDir)
	})

	return h
}

// dial opens a jsonrpc2 client connection to the ARI server.
// The handler receives inbound notifications from the server (unused in these tests).
func (h *testHarness) dial(t *testing.T, handler jsonrpc2.Handler) *jsonrpc2.Conn {
	t.Helper()
	nc, err := net.Dial("unix", h.socket)
	require.NoError(t, err, "failed to dial Unix socket")
	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(handler))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// createWorkspaceInStore creates a workspace in the meta store database.
// This is only needed for tests that bypass workspace/prepare and directly create sessions.
// Most tests should use prepareWorkspaceForSession which handles database persistence automatically.
func (h *testHarness) createWorkspaceInStore(ctx context.Context, t *testing.T, workspaceID, name, path string) {
	t.Helper()
	workspace := &meta.Workspace{
		ID:     workspaceID,
		Name:   name,
		Path:   path,
		Status: meta.WorkspaceStatusActive,
	}
	err := h.store.CreateWorkspace(ctx, workspace)
	require.NoError(t, err, "failed to create workspace in store")
}

// prepareWorkspaceForSession prepares a workspace via RPC.
// The workspace/prepare handler persists to both the Registry and the meta store,
// so no additional database persistence is needed.
func (h *testHarness) prepareWorkspaceForSession(ctx context.Context, t *testing.T, client *jsonrpc2.Conn, name string) (workspaceId, workspacePath string) {
	t.Helper()
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: name},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")

	return prepareResult.WorkspaceId, prepareResult.Path
}

// nullHandler is a jsonrpc2 handler that ignores all requests.
// Used when we don't expect any notifications from the server.
type nullHandler struct{}

func (h *nullHandler) Handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Ignore all notifications/requests from server.
}

// ────────────────────────────────────────────────────────────────────────────
// Binary Helpers (copied from pkg/agentd/process_test.go)
// ────────────────────────────────────────────────────────────────────────────

// findShimBinary finds the agent-shim binary for testing.
// Returns the path or skips the test if not found.
func findShimBinary(t *testing.T) string {
	t.Helper()
	// Try project bin directory first.
	projectRoot := findProjectRoot(t)
	builtPath := filepath.Join(projectRoot, "bin", "agent-shim")
	if _, err := os.Stat(builtPath); err == nil {
		return builtPath
	}

	// Try building it on-the-fly.
	binDir := filepath.Join(projectRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	// Build agent-shim.
	cmd := exec.Command("go", "build", "-o", builtPath, "./cmd/agent-shim")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("build agent-shim: %v", err)
	}

	return builtPath
}

// findMockagentBinary finds the mockagent binary for testing.
// Returns the path or skips the test if not found.
func findMockagentBinary(t *testing.T) string {
	t.Helper()
	// Try project bin directory first.
	projectRoot := findProjectRoot(t)
	builtPath := filepath.Join(projectRoot, "bin", "mockagent")
	if _, err := os.Stat(builtPath); err == nil {
		return builtPath
	}

	// Try building it on-the-fly.
	binDir := filepath.Join(projectRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	// Build mockagent.
	cmd := exec.Command("go", "build", "-o", builtPath, "./internal/testutil/mockagent")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("build mockagent: %v", err)
	}

	return builtPath
}

// findProjectRoot finds the project root directory.
func findProjectRoot(t *testing.T) string {
	t.Helper()
	// Walk up from current directory until we find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with EmptyDir source
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareEmptyDir(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-empty-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var result ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &result)
	require.NoError(t, err, "workspace/prepare should succeed for EmptyDir")
	require.NotEmpty(t, result.WorkspaceId, "workspaceId should be non-empty")
	require.NotEmpty(t, result.Path, "path should be non-empty")
	require.Equal(t, "ready", result.Status, "status should be ready")

	// Verify the workspace directory exists.
	require.DirExists(t, result.Path, "workspace directory should exist")

	// Verify the workspace is in the registry.
	meta := h.registry.Get(result.WorkspaceId)
	require.NotNil(t, meta, "workspace should be registered")
	require.Equal(t, result.Path, meta.Path, "registry path should match result")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with Git source
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareGit(t *testing.T) {
	// Skip if git not available.
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git not available")
	}

	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Use a small, fast repository for testing.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-git-workspace"},
		Source: workspace.Source{
			Type: workspace.SourceTypeGit,
			Git:  workspace.GitSource{URL: "https://github.com/octocat/Hello-World.git", Depth: 1},
		},
	}

	var result ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &result)
	require.NoError(t, err, "workspace/prepare should succeed for Git")
	require.NotEmpty(t, result.WorkspaceId, "workspaceId should be non-empty")
	require.NotEmpty(t, result.Path, "path should be non-empty")
	require.Equal(t, "ready", result.Status, "status should be ready")

	// Verify the workspace directory exists and has .git.
	require.DirExists(t, result.Path, "workspace directory should exist")
	require.DirExists(t, filepath.Join(result.Path, ".git"), "cloned repo should have .git directory")

	// Verify README exists (Hello-World repo has README).
	require.FileExists(t, filepath.Join(result.Path, "README"), "cloned repo should have README")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with Local source
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareLocal(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create a real local directory.
	localDir := t.TempDir()
	testFile := filepath.Join(localDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-local-workspace"},
		Source: workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: localDir},
		},
	}

	var result ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &result)
	require.NoError(t, err, "workspace/prepare should succeed for Local")
	require.NotEmpty(t, result.WorkspaceId, "workspaceId should be non-empty")
	require.Equal(t, localDir, result.Path, "path should match local source path")
	require.Equal(t, "ready", result.Status, "status should be ready")

	// Verify the test file still exists (local workspace not modified).
	require.FileExists(t, testFile, "local workspace contents should be unchanged")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/list
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceList(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// First, prepare a workspace so list has something to return.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-list-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")

	// Now call workspace/list.
	var listResult ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult)
	require.NoError(t, err, "workspace/list should succeed")
	require.Len(t, listResult.Workspaces, 1, "workspace list should have 1 entry")
	require.Equal(t, prepareResult.WorkspaceId, listResult.Workspaces[0].WorkspaceId, "workspaceId should match")
	require.Equal(t, "test-list-workspace", listResult.Workspaces[0].Name, "name should match")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/list on empty registry
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceListEmpty(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call workspace/list without preparing any workspaces.
	var listResult ari.WorkspaceListResult
	err := client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult)
	require.NoError(t, err, "workspace/list should succeed on empty registry")
	require.Empty(t, listResult.Workspaces, "workspace list should be empty array")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceCleanup(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare a workspace.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-cleanup-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")
	workspacePath := prepareResult.Path

	// Verify workspace exists.
	require.DirExists(t, workspacePath, "workspace should exist before cleanup")

	// Call workspace/cleanup.
	var cleanupResult interface{}
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: prepareResult.WorkspaceId}, &cleanupResult)
	require.NoError(t, err, "workspace/cleanup should succeed")

	// Verify workspace directory is deleted.
	require.NoDirExists(t, workspacePath, "workspace directory should be deleted after cleanup")

	// Verify workspace is removed from registry via workspace/list.
	var listResult ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult)
	require.NoError(t, err, "workspace/list should succeed")
	require.Empty(t, listResult.Workspaces, "workspace list should be empty after cleanup")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup with refs > 0 (failure case)
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceCleanupWithRefs(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare a workspace.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-cleanup-refs-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")
	workspacePath := prepareResult.Path

	// Manually acquire a reference via registry + DB (simulate session).
	h.registry.Acquire(prepareResult.WorkspaceId, "test-session-123")
	// Create a fake session in DB so AcquireWorkspace's FK constraint is satisfied.
	err = h.store.CreateSession(ctx, &meta.Session{
		ID:           "test-session-123",
		WorkspaceID:  prepareResult.WorkspaceId,
		RuntimeClass: "default",
		State:        meta.SessionStateCreated,
	})
	require.NoError(t, err, "CreateSession should succeed")
	err = h.store.AcquireWorkspace(ctx, prepareResult.WorkspaceId, "test-session-123")
	require.NoError(t, err, "AcquireWorkspace should succeed")

	// Verify workspace exists.
	require.DirExists(t, workspacePath, "workspace should exist before cleanup attempt")

	// Call workspace/cleanup - should fail because RefCount > 0.
	var cleanupResult interface{}
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: prepareResult.WorkspaceId}, &cleanupResult)
	require.Error(t, err, "workspace/cleanup should fail when RefCount > 0")

	// Verify JSON-RPC error code is InternalError.
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInternalError), int64(rpcErr.Code),
		"expected CodeInternalError for refs > 0, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "active references",
		"error message should mention active references")

	// Verify workspace still exists (cleanup failed).
	require.DirExists(t, workspacePath, "workspace should still exist after failed cleanup")

	// Verify workspace is still in registry via workspace/list.
	var listResult ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult)
	require.NoError(t, err, "workspace/list should succeed")
	require.Len(t, listResult.Workspaces, 1, "workspace should still be in list")
	require.Equal(t, prepareResult.WorkspaceId, listResult.Workspaces[0].WorkspaceId, "workspaceId should match")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup nonexistent workspace (error case)
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceCleanupNonexistent(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call workspace/cleanup with a nonexistent workspaceId.
	var cleanupResult interface{}
	err := client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: "nonexistent-id"}, &cleanupResult)
	require.Error(t, err, "workspace/cleanup should fail for nonexistent workspace")

	// Verify JSON-RPC error code is InvalidParams.
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nonexistent workspace, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not found",
		"error message should mention not found")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with invalid spec (error case)
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareInvalidSpec(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	tests := []struct {
		name    string
		spec    workspace.WorkspaceSpec
		wantErr string
	}{
		{
			name: "missing oarVersion",
			spec: workspace.WorkspaceSpec{
				OarVersion: "",
				Metadata:   workspace.WorkspaceMetadata{Name: "test"},
				Source:     workspace.Source{Type: workspace.SourceTypeEmptyDir},
			},
			wantErr: "validation",
		},
		{
			name: "missing metadata.name",
			spec: workspace.WorkspaceSpec{
				OarVersion: "0.1.0",
				Metadata:   workspace.WorkspaceMetadata{Name: ""},
				Source:     workspace.Source{Type: workspace.SourceTypeEmptyDir},
			},
			wantErr: "validation",
		},
		{
			name: "git source missing URL",
			spec: workspace.WorkspaceSpec{
				OarVersion: "0.1.0",
				Metadata:   workspace.WorkspaceMetadata{Name: "test"},
				Source: workspace.Source{
					Type: workspace.SourceTypeGit,
					Git:  workspace.GitSource{URL: ""},
				},
			},
			wantErr: "validation",
		},
		{
			name: "local source missing path",
			spec: workspace.WorkspaceSpec{
				OarVersion: "0.1.0",
				Metadata:   workspace.WorkspaceMetadata{Name: "test"},
				Source: workspace.Source{
					Type:  workspace.SourceTypeLocal,
					Local: workspace.LocalSource{Path: ""},
				},
			},
			wantErr: "validation",
		},
		{
			name: "unsupported major version",
			spec: workspace.WorkspaceSpec{
				OarVersion: "1.0.0",
				Metadata:   workspace.WorkspaceMetadata{Name: "test"},
				Source:     workspace.Source{Type: workspace.SourceTypeEmptyDir},
			},
			wantErr: "validation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result ari.WorkspacePrepareResult
			err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: tt.spec}, &result)
			require.Error(t, err, "workspace/prepare should fail for invalid spec")

			var rpcErr *jsonrpc2.Error
			require.ErrorAs(t, err, &rpcErr)
			require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
				"expected CodeInvalidParams for invalid spec, got %d", rpcErr.Code)
			require.Contains(t, rpcErr.Message, tt.wantErr,
				"error message should contain %q", tt.wantErr)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with nil params (malformed input)
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareNilParams(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call workspace/prepare with nil params.
	var result ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", nil, &result)
	require.Error(t, err, "workspace/prepare should fail with nil params")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nil params, got %d", rpcErr.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/prepare with setup hook failure
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspacePrepareHookFailure(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Spec with failing setup hook.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "test-hook-fail-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
		Hooks: workspace.Hooks{
			Setup: []workspace.Hook{
				{Command: "false", Description: "hook that always fails"},
			},
		},
	}

	var result ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &result)
	require.Error(t, err, "workspace/prepare should fail when setup hook fails")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for hook failure, got %d", rpcErr.Code)

	// Verify error message contains "prepare-hooks" phase.
	require.Contains(t, rpcErr.Message, "prepare-hooks",
		"error message should preserve WorkspaceError Phase field")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup with nonexistent workspace
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceCleanupNotFound(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call workspace/cleanup with nonexistent workspaceId.
	var result interface{}
	err := client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: "nonexistent-uuid"}, &result)
	require.Error(t, err, "workspace/cleanup should fail for nonexistent workspace")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nonexistent workspace, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not found",
		"error message should mention workspace not found")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: Unknown method returns MethodNotFound
// ────────────────────────────────────────────────────────────────────────────

func TestARIUnknownMethod(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	var result interface{}
	err := client.Call(ctx, "unknown/method", nil, &result)
	require.Error(t, err, "unknown method should return error")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeMethodNotFound), int64(rpcErr.Code),
		"expected CodeMethodNotFound for unknown method, got %d", rpcErr.Code)
}

// ────────────────────────────────────────────────────────────────────────────
// Integration Test: prepare → list → cleanup round-trip
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceLifecycle(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Prepare a workspace.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "lifecycle-test-workspace"},
		Source: workspace.Source{
			Type:     workspace.SourceTypeEmptyDir,
			EmptyDir: workspace.EmptyDirSource{},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")
	require.NotEmpty(t, prepareResult.WorkspaceId, "workspaceId should be non-empty")
	require.Equal(t, "ready", prepareResult.Status, "status should be ready")

	workspacePath := prepareResult.Path
	require.DirExists(t, workspacePath, "workspace directory should exist after prepare")

	// Step 2: List workspaces and verify the workspace is present.
	var listResult1 ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult1)
	require.NoError(t, err, "workspace/list should succeed")
	require.Len(t, listResult1.Workspaces, 1, "workspace list should have 1 entry")
	require.Equal(t, prepareResult.WorkspaceId, listResult1.Workspaces[0].WorkspaceId, "workspaceId should match prepare result")

	// Step 3: Cleanup the workspace.
	var cleanupResult interface{}
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: prepareResult.WorkspaceId}, &cleanupResult)
	require.NoError(t, err, "workspace/cleanup should succeed")

	// Step 4: List workspaces and verify the workspace is absent.
	var listResult2 ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult2)
	require.NoError(t, err, "workspace/list should succeed")
	require.Empty(t, listResult2.Workspaces, "workspace list should be empty after cleanup")

	// Step 5: Verify workspace directory is deleted.
	require.NoDirExists(t, workspacePath, "workspace directory should be deleted after cleanup")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup for local workspace (unmanaged - not deleted)
// ────────────────────────────────────────────────────────────────────────────

func TestARIWorkspaceCleanupLocalNotDeleted(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create a real local directory with a test file.
	localDir := t.TempDir()
	testFile := filepath.Join(localDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	// Prepare local workspace.
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "local-cleanup-test"},
		Source: workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: localDir},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed for Local")
	require.Equal(t, localDir, prepareResult.Path, "path should match local source")

	// Cleanup the workspace.
	var cleanupResult interface{}
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: prepareResult.WorkspaceId}, &cleanupResult)
	require.NoError(t, err, "workspace/cleanup should succeed for Local")

	// CRITICAL: Verify local directory still exists (not deleted because unmanaged).
	require.DirExists(t, localDir, "local workspace should NOT be deleted")
	require.FileExists(t, testFile, "local workspace contents should NOT be deleted")

	// Verify workspace is removed from registry.
	var listResult ari.WorkspaceListResult
	err = client.Call(ctx, "workspace/list", ari.WorkspaceListParams{}, &listResult)
	require.NoError(t, err, "workspace/list should succeed")
	require.Empty(t, listResult.Workspaces, "workspace should be removed from registry")
}

// ────────────────────────────────────────────────────────────────────────────
// Session Integration Tests
// ────────────────────────────────────────────────────────────────────────────

// TestARIAgentLifecycle tests the full agent round-trip:
// workspace/prepare → room/create → agent/create → agent/prompt → agent/status → agent/stop → agent/delete
// This replaces the old TestARISessionLifecycle now that session/* is removed.
func TestARIAgentLifecycle(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create workspace and room.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "agent-lifecycle-test")
	require.NotEmpty(t, workspaceId, "workspaceId should be non-empty")
	roomCreate(ctx, t, client, "lifecycle-room", "mesh", nil)

	// Step 2: Create agent via agent/create — returns "creating" immediately.
	createResult := agentCreate(ctx, t, client, "lifecycle-room", "lifecycle-agent", "mockagent", workspaceId)
	agentId := createResult.AgentId
	require.NotEmpty(t, agentId, "agentId should be non-empty")

	// Wait for background bootstrap to complete.
	statusAfterCreate := pollAgentUntilReady(ctx, t, client, agentId)
	require.Equal(t, "created", statusAfterCreate.Agent.State, "agent should be 'created' after bootstrap")

	// Step 3: Prompt agent via agent/prompt (session already bootstrapped).
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: agentId,
		Prompt:  "hello mockagent",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed")
	require.NotEmpty(t, promptResult.StopReason, "stopReason should be non-empty")

	// Step 4: Check agent/status - verify state.
	var statusResult ari.AgentStatusResult
	err = client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: agentId}, &statusResult)
	require.NoError(t, err, "agent/status should succeed")
	require.Equal(t, agentId, statusResult.Agent.AgentId, "agentId should match")
	require.Equal(t, "running", statusResult.Agent.State, "state should be 'running' after prompt")

	// Step 5: Stop agent via agent/stop.
	agentStop(ctx, t, client, agentId)

	// Wait for shim process to fully stop.
	time.Sleep(200 * time.Millisecond)

	// Step 6: Check agent/status - state should be "stopped".
	var statusResult2 ari.AgentStatusResult
	err = client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: agentId}, &statusResult2)
	require.NoError(t, err, "agent/status should succeed")
	require.Equal(t, "stopped", statusResult2.Agent.State, "state should be 'stopped' after stop")

	// Step 7: Delete agent via agent/delete.
	var deleteResult interface{}
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: agentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed")

	// Step 8: Verify agent/list is empty.
	var listResult ari.AgentListResult
	err = client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed")
	require.Empty(t, listResult.Agents, "agent list should be empty after delete")
}

// TestARIAgentPromptAutoStart verifies auto-start on agent/prompt when state="created".
// Replaces TestARISessionPromptAutoStart now that session/* is removed.
func TestARIAgentPromptAutoStart(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "auto-start-test")
	roomCreate(ctx, t, client, "auto-start-room", "mesh", nil)

	createResult := agentCreate(ctx, t, client, "auto-start-room", "auto-start-agent", "mockagent", workspaceId)

	// Wait for bootstrap to complete — session must exist before prompt.
	statusAfterCreate := pollAgentUntilReady(ctx, t, client, createResult.AgentId)
	require.Equal(t, "created", statusAfterCreate.Agent.State, "agent should be 'created' after bootstrap")

	// Call agent/prompt — session is "created" (not yet started), so auto-start kicks in.
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "auto-start test",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed (auto-start)")
	require.NotEmpty(t, promptResult.StopReason, "stopReason should be non-empty")

	// Cleanup.
	agentStop(ctx, t, client, createResult.AgentId)
}

// TestARIAgentPromptOnStopped verifies error for agent/prompt on stopped agent.
// Replaces TestARISessionPromptOnStopped now that session/* is removed.
func TestARIAgentPromptOnStopped(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "prompt-stopped-test")
	roomCreate(ctx, t, client, "prompt-stopped-room", "mesh", nil)

	createResult := agentCreate(ctx, t, client, "prompt-stopped-room", "prompt-stopped-agent", "mockagent", workspaceId)

	// Wait for bootstrap before sending the first prompt.
	pollAgentUntilReady(ctx, t, client, createResult.AgentId)

	// Prompt to start the agent (auto-starts session).
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "first prompt",
	}, &promptResult)
	require.NoError(t, err, "first agent/prompt should succeed")

	// Stop the agent.
	agentStop(ctx, t, client, createResult.AgentId)

	// Wait for shim to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Call agent/prompt on stopped agent — should fail.
	var promptResult2 ari.AgentPromptResult
	err = client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "prompt on stopped",
	}, &promptResult2)
	require.Error(t, err, "agent/prompt on stopped agent should fail")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for prompt on stopped, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not running",
		"error message should mention 'not running'")
}

// TestARISessionRemoveProtected verifies agent/delete requires stopped state (migrated from session/* to agent/*).
func TestARISessionRemoveProtected(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "remove-protected-test")
	roomCreate(ctx, t, client, "remove-protected-room", "mesh", nil)

	createResult := agentCreate(ctx, t, client, "remove-protected-room", "protected-agent", "mockagent", workspaceId)

	// Wait for bootstrap before sending prompt.
	pollAgentUntilReady(ctx, t, client, createResult.AgentId)

	// Prompt to start agent (auto-starts session → state=running).
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "running prompt",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed")

	// Verify agent is running.
	var statusResult ari.AgentStatusResult
	err = client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: createResult.AgentId}, &statusResult)
	require.NoError(t, err, "agent/status should succeed")
	require.Equal(t, "running", statusResult.Agent.State, "agent should be running")

	// agent/delete on running agent should fail.
	var deleteResult interface{}
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: createResult.AgentId}, &deleteResult)
	require.Error(t, err, "agent/delete on running agent should fail")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "stopped")

	// Stop agent.
	agentStop(ctx, t, client, createResult.AgentId)
	time.Sleep(200 * time.Millisecond)

	// Now agent/delete should succeed.
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: createResult.AgentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed after stop")
}

// TestARISessionList verifies agent/list returns all agents (migrated from session/* to agent/*).
func TestARISessionList(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "list-test")
	roomCreate(ctx, t, client, "list-room", "mesh", nil)

	// Create 2 agents.
	result1 := agentCreate(ctx, t, client, "list-room", "agent-list-1", "default", workspaceId)
	result2 := agentCreate(ctx, t, client, "list-room", "agent-list-2", "default", workspaceId)

	// agent/list should return 2 agents.
	var listResult ari.AgentListResult
	err := client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed")
	require.Len(t, listResult.Agents, 2, "agent list should have 2 entries")

	agentIds := make(map[string]bool)
	for _, a := range listResult.Agents {
		agentIds[a.AgentId] = true
		require.Equal(t, "list-room", a.Room, "room should match")
		// State is "creating" or "error" (no real runtime in newTestHarness).
		require.Contains(t, []string{"creating", "error"}, a.State, "state should be creating or error")
	}
	require.True(t, agentIds[result1.AgentId], "agent #1 should be in list")
	require.True(t, agentIds[result2.AgentId], "agent #2 should be in list")
}

// TestARISessionNotFound verifies "not found" errors for nonexistent agents (migrated from session/*).
func TestARISessionNotFound(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	nonexistentId := uuid.New().String()

	// Test agent/status with nonexistent agentId.
	var statusResult ari.AgentStatusResult
	err := client.Call(ctx, "agent/status", ari.AgentStatusParams{
		AgentId: nonexistentId,
	}, &statusResult)
	require.Error(t, err, "agent/status should fail for nonexistent agent")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")

	// Test agent/prompt with nonexistent agentId.
	var promptResult ari.AgentPromptResult
	err = client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: nonexistentId,
		Prompt:  "test",
	}, &promptResult)
	require.Error(t, err, "agent/prompt should fail for nonexistent agent")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")

	// Test agent/stop with nonexistent agentId.
	var stopResult interface{}
	err = client.Call(ctx, "agent/stop", ari.AgentStopParams{
		AgentId: nonexistentId,
	}, &stopResult)
	require.Error(t, err, "agent/stop should fail for nonexistent agent")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// TestARISessionNewNilParams tests malformed input: nil params for agent/create (migrated from session/*).
func TestARISessionNewNilParams(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	var result ari.AgentCreateResult
	err := client.Call(ctx, "agent/create", nil, &result)
	require.Error(t, err, "agent/create should fail with nil params")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nil params, got %d", rpcErr.Code)
}

// TestARISessionPromptMissingSessionId tests missing agentId for agent/prompt (migrated from session/*).
func TestARISessionPromptMissingSessionId(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call with empty agentId — agent/prompt should fail.
	var result ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: "",
		Prompt:  "test",
	}, &result)
	require.Error(t, err, "agent/prompt should fail with empty agentId")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "required")
}

// TestARISessionListEmpty verifies agent/list on empty DB (migrated from session/*).
func TestARISessionListEmpty(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	var listResult ari.AgentListResult
	err := client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed on empty DB")
	require.Empty(t, listResult.Agents, "agent list should be empty array")
}

// TestARISessionStatusStopped verifies agent/status on stopped agent (no shimState) (migrated from session/*).
func TestARISessionStatusStopped(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "status-stopped-test")
	roomCreate(ctx, t, client, "status-stopped-room", "mesh", nil)

	createResult := agentCreate(ctx, t, client, "status-stopped-room", "status-stopped-agent", "mockagent", workspaceId)

	// Wait for bootstrap before prompting.
	pollAgentUntilReady(ctx, t, client, createResult.AgentId)

	// Prompt to start agent (auto-starts session).
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "test",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed")

	// Stop agent.
	agentStop(ctx, t, client, createResult.AgentId)
	time.Sleep(500 * time.Millisecond)

	// Check agent/status — shimState should be nil for stopped agent.
	var statusResult ari.AgentStatusResult
	err = client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: createResult.AgentId}, &statusResult)
	require.NoError(t, err, "agent/status should succeed")
	require.Equal(t, "stopped", statusResult.Agent.State, "state should be 'stopped'")
	require.Nil(t, statusResult.ShimState, "shimState should be nil for stopped agent")
}

// ────────────────────────────────────────────────────────────────────────────
// Recovery Guard Tests
// ────────────────────────────────────────────────────────────────────────────

// TestARIRecoveryGuard_BlocksPromptDuringRecovery verifies that agent/prompt
// returns CodeRecoveryBlocked (-32001) while the daemon's recovery phase is
// RecoveryPhaseRecovering.
func TestARIRecoveryGuard_BlocksPromptDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Put the process manager into recovering phase.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// agent/prompt should be blocked during recovery.
	var result ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: "any-agent-id",
		Prompt:  "hello",
	}, &result)
	requireRPCError(t, err, ari.CodeRecoveryBlocked, "recovering")
}

// TestARIRecoveryGuard_BlocksCancelDuringRecovery verifies that agent/cancel
// returns CodeRecoveryBlocked while the daemon is recovering.
func TestARIRecoveryGuard_BlocksCancelDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	var result interface{}
	err := client.Call(ctx, "agent/cancel", ari.AgentCancelParams{
		AgentId: "any-agent-id",
	}, &result)
	requireRPCError(t, err, ari.CodeRecoveryBlocked, "recovering")
}

// TestARIRecoveryGuard_AllowsStatusDuringRecovery verifies that agent/status
// is NOT blocked during recovery (it is read-only).
func TestARIRecoveryGuard_AllowsStatusDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create a workspace and agent so agent/status has something to find.
	wsID, _ := h.prepareWorkspaceForSession(ctx, t, h.dial(t, &nullHandler{}), "guard-test-ws")
	roomCreate(ctx, t, client, "guard-status-room", "mesh", nil)
	createResult := agentCreate(ctx, t, client, "guard-status-room", "guard-status-agent", "default", wsID)

	// Set recovering phase.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// agent/status should still succeed (read-only, not guarded).
	var statusResult ari.AgentStatusResult
	err := client.Call(ctx, "agent/status", ari.AgentStatusParams{
		AgentId: createResult.AgentId,
	}, &statusResult)
	require.NoError(t, err, "agent/status should succeed during recovery")
	require.Equal(t, createResult.AgentId, statusResult.Agent.AgentId)
}

// TestARIRecoveryGuard_AllowsListDuringRecovery verifies that agent/list
// is NOT blocked during recovery.
func TestARIRecoveryGuard_AllowsListDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	var listResult ari.AgentListResult
	err := client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed during recovery")
}

// TestARIRecoveryGuard_AllowsPromptAfterRecovery verifies that agent/prompt
// is no longer blocked once the recovery phase transitions past Recovering.
func TestARIRecoveryGuard_AllowsPromptAfterRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Transition through recovering → complete.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseComplete)

	// agent/prompt should NOT return recovery-blocked.
	var result ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: "nonexistent-agent",
		Prompt:  "hello",
	}, &result)
	// We expect an error (agent not found) but NOT a recovery guard error.
	require.Error(t, err, "should fail because agent doesn't exist")
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.NotEqual(t, ari.CodeRecoveryBlocked, int64(rpcErr.Code),
		"should NOT be CodeRecoveryBlocked after recovery completes")
}

// TestARIRecoveryGuard_AllowsStopDuringRecovery verifies that agent/stop
// is NOT blocked during recovery (safety-critical, intentionally unguarded).
func TestARIRecoveryGuard_AllowsStopDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// agent/stop should NOT be blocked. It may fail (agent not found)
	// but the error should NOT be CodeRecoveryBlocked.
	var result interface{}
	err := client.Call(ctx, "agent/stop", ari.AgentStopParams{
		AgentId: "nonexistent-agent",
	}, &result)
	require.Error(t, err, "should fail because agent doesn't exist")
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.NotEqual(t, ari.CodeRecoveryBlocked, int64(rpcErr.Code),
		"agent/stop should NOT be recovery-blocked")
}

// ────────────────────────────────────────────────────────────────────────────
// Workspace Ref Tests (DB ref_count via session lifecycle)
// ────────────────────────────────────────────────────────────────────────────

// TestARISessionNewAcquiresWorkspaceRef verifies that agent/create records a
// workspace_ref in the DB (incrementing ref_count). Two agents on the same
// workspace should yield ref_count == 2.
// NOTE: With async bootstrap, ref_count is incremented inside the goroutine.
// We poll until bootstrap completes (success or error) then verify.
// newTestHarness has no real runtime, so agents end up in "error" state
// and their sessions are deleted (ref_count drops back to 0) on failure.
// We use newSessionTestHarness with mockagent to test the success path.
func TestARISessionNewAcquiresWorkspaceRef(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace (persisted to DB by handleWorkspacePrepare).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "ref-acquire-test")
	roomCreate(ctx, t, client, "ref-acquire-room", "mesh", nil)

	// Create first agent and wait for bootstrap to complete.
	result1 := agentCreate(ctx, t, client, "ref-acquire-room", "ref-agent-1", "mockagent", workspaceId)
	require.NotEmpty(t, result1.AgentId)
	pollAgentUntilReady(ctx, t, client, result1.AgentId)

	// Assert DB ref_count == 1.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err, "GetWorkspace should succeed")
	require.NotNil(t, ws, "workspace should exist in DB")
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after first agent bootstrap")

	// Create second agent on the same workspace and wait for bootstrap.
	result2 := agentCreate(ctx, t, client, "ref-acquire-room", "ref-agent-2", "mockagent", workspaceId)
	require.NotEmpty(t, result2.AgentId)
	pollAgentUntilReady(ctx, t, client, result2.AgentId)

	// Assert DB ref_count == 2.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err, "GetWorkspace should succeed")
	require.Equal(t, 2, ws.RefCount, "ref_count should be 2 after second agent bootstrap")

	// Cleanup: stop both agents.
	agentStop(ctx, t, client, result1.AgentId)
	agentStop(ctx, t, client, result2.AgentId)
}

// TestARISessionRemoveReleasesWorkspaceRef verifies that deleting an agent
// decrements the workspace ref_count in the DB.
func TestARISessionRemoveReleasesWorkspaceRef(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "ref-release-test")
	roomCreate(ctx, t, client, "ref-release-room", "mesh", nil)

	// Create agent and wait for bootstrap to complete.
	result := agentCreate(ctx, t, client, "ref-release-room", "ref-release-agent", "mockagent", workspaceId)
	pollAgentUntilReady(ctx, t, client, result.AgentId)

	// Assert ref_count == 1 after bootstrap.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after agent bootstrap")

	// Stop then delete the agent.
	agentStop(ctx, t, client, result.AgentId)
	var deleteResult interface{}
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: result.AgentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed")

	// Assert ref_count == 0.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 0, ws.RefCount, "ref_count should be 0 after agent delete")
}

// TestARIWorkspacePrepareSourcePersisted verifies that handleWorkspacePrepare
// serializes the full Source spec into the DB (not the default "{}").
func TestARIWorkspacePrepareSourcePersisted(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare a workspace with a local source (avoids network dependency).
	localDir := t.TempDir()
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata:   workspace.WorkspaceMetadata{Name: "source-persist-test"},
		Source: workspace.Source{
			Type:  workspace.SourceTypeLocal,
			Local: workspace.LocalSource{Path: localDir},
		},
	}

	var prepareResult ari.WorkspacePrepareResult
	err := client.Call(ctx, "workspace/prepare", ari.WorkspacePrepareParams{Spec: spec}, &prepareResult)
	require.NoError(t, err, "workspace/prepare should succeed")

	// Query DB and verify Source is not "{}".
	ws, err := h.store.GetWorkspace(ctx, prepareResult.WorkspaceId)
	require.NoError(t, err, "GetWorkspace should succeed")
	require.NotNil(t, ws, "workspace should exist in DB")
	require.NotEqual(t, "{}", string(ws.Source), "Source should not be empty JSON")
	require.Contains(t, string(ws.Source), `"type"`, "Source should contain type field")
	require.Contains(t, string(ws.Source), `"local"`, "Source should contain local source type")
	require.Contains(t, string(ws.Source), localDir, "Source should contain the local path")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup blocked by DB ref_count
// ────────────────────────────────────────────────────────────────────────────

// TestARIWorkspaceCleanupBlockedByDBRefCount verifies that workspace/cleanup
// gates on the persisted DB ref_count (not the volatile in-memory registry
// RefCount). With an active agent, cleanup is rejected; after agent
// delete the ref_count drops to 0 and cleanup succeeds.
func TestARIWorkspaceCleanupBlockedByDBRefCount(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace (persisted to DB).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "cleanup-db-refcount-test")
	roomCreate(ctx, t, client, "cleanup-refcount-room", "mesh", nil)

	// Create an agent and wait for bootstrap — this acquires a DB workspace_ref (ref_count → 1).
	result := agentCreate(ctx, t, client, "cleanup-refcount-room", "cleanup-ref-agent", "mockagent", workspaceId)
	require.NotEmpty(t, result.AgentId)
	pollAgentUntilReady(ctx, t, client, result.AgentId)

	// Verify DB ref_count == 1.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.NotNil(t, ws)
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after agent bootstrap")

	// Attempt workspace/cleanup — should fail because DB ref_count > 0.
	var cleanupResult interface{}
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: workspaceId}, &cleanupResult)
	require.Error(t, err, "workspace/cleanup should fail when DB ref_count > 0")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInternalError), int64(rpcErr.Code),
		"expected CodeInternalError for refs > 0")
	require.Contains(t, rpcErr.Message, "active references",
		"error message should mention active references")

	// Stop then delete the agent (releases workspace ref).
	agentStop(ctx, t, client, result.AgentId)
	var deleteResult interface{}
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: result.AgentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed")

	// Verify DB ref_count == 0 after deletion.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 0, ws.RefCount, "ref_count should be 0 after agent delete")

	// Retry workspace/cleanup — should succeed now.
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: workspaceId}, &cleanupResult)
	require.NoError(t, err, "workspace/cleanup should succeed when DB ref_count == 0")
}

// TestARIWorkspaceCleanupBlockedDuringRecovery verifies that workspace/cleanup
// returns CodeRecoveryBlocked while the daemon is in RecoveryPhaseRecovering.
func TestARIWorkspaceCleanupBlockedDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Put the process manager into recovering phase.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// workspace/cleanup should be blocked — even the params don't matter,
	// the recovery guard fires before parameter parsing.
	var result interface{}
	err := client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{
		WorkspaceId: "any-workspace-id",
	}, &result)
	requireRPCError(t, err, ari.CodeRecoveryBlocked, "recovering")
}

// ────────────────────────────────────────────────────────────────────────────
// Helper: verify JSON-RPC error structure
// ────────────────────────────────────────────────────────────────────────────

// requireRPCError verifies that err is a jsonrpc2.Error with expected code.
func requireRPCError(t *testing.T, err error, expectedCode int64, containsMsg string) {
	t.Helper()
	require.Error(t, err, "expected JSON-RPC error")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr, "error should be jsonrpc2.Error")
	require.Equal(t, expectedCode, int64(rpcErr.Code),
		"expected JSON-RPC code %d, got %d", expectedCode, rpcErr.Code)
	require.Contains(t, rpcErr.Message, containsMsg,
		"error message should contain %q, got %q", containsMsg, rpcErr.Message)
}

// ────────────────────────────────────────────────────────────────────────────
// Room Lifecycle Integration Tests
// ────────────────────────────────────────────────────────────────────────────

// roomCreate is a test helper that calls room/create via JSON-RPC.
func roomCreate(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, name, mode string, labels map[string]string) ari.RoomCreateResult {
	t.Helper()
	params := ari.RoomCreateParams{
		Name:   name,
		Labels: labels,
	}
	if mode != "" {
		params.Communication = &ari.RoomCommunication{Mode: mode}
	}
	var result ari.RoomCreateResult
	err := conn.Call(ctx, "room/create", params, &result)
	require.NoError(t, err, "room/create should succeed for room %q", name)
	return result
}

// roomStatus is a test helper that calls room/status via JSON-RPC.
func roomStatus(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, name string) ari.RoomStatusResult {
	t.Helper()
	var result ari.RoomStatusResult
	err := conn.Call(ctx, "room/status", ari.RoomStatusParams{Name: name}, &result)
	require.NoError(t, err, "room/status should succeed for room %q", name)
	return result
}

// roomDelete is a test helper that calls room/delete via JSON-RPC.
func roomDelete(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, name string) {
	t.Helper()
	var result interface{}
	err := conn.Call(ctx, "room/delete", ari.RoomDeleteParams{Name: name}, &result)
	require.NoError(t, err, "room/delete should succeed for room %q", name)
}

// TestARIRoomLifecycle tests the full room round-trip:
// room/create → agent/create (2 members) → room/status → agent/stop → room/delete → room/status (not found)
// This is the primary end-to-end test proving the slice demo claim.
func TestARIRoomLifecycle(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create a room with mesh communication mode.
	createResult := roomCreate(ctx, t, client, "test-room", "mesh", map[string]string{"env": "test"})
	require.Equal(t, "test-room", createResult.Name, "room name should match")
	require.Equal(t, "mesh", createResult.CommunicationMode, "communication mode should be mesh")
	require.NotEmpty(t, createResult.CreatedAt, "createdAt should be non-empty")

	// Step 2: Prepare a workspace for agents.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "room-lifecycle-ws")

	// Step 3: Create 2 agents in the room with different names (async bootstrap).
	agentResultA := agentCreate(ctx, t, client, "test-room", "agent-a", "mockagent", workspaceId)
	require.NotEmpty(t, agentResultA.AgentId)

	agentResultB := agentCreate(ctx, t, client, "test-room", "agent-b", "mockagent", workspaceId)
	require.NotEmpty(t, agentResultB.AgentId)

	// Wait for both agents to finish bootstrapping (session is created in goroutine).
	pollAgentUntilReady(ctx, t, client, agentResultA.AgentId)
	pollAgentUntilReady(ctx, t, client, agentResultB.AgentId)

	// Step 4: Call room/status → verify 2 members with correct agentName/state.
	status := roomStatus(ctx, t, client, "test-room")
	require.Equal(t, "test-room", status.Name, "room name should match")
	require.Equal(t, "mesh", status.CommunicationMode, "communication mode should match")
	require.Len(t, status.Members, 2, "room should have 2 members after bootstrap")

	// Build a map for easier assertion (order is not guaranteed).
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Contains(t, memberMap, "agent-a", "agent-a should be a member")
	require.Contains(t, memberMap, "agent-b", "agent-b should be a member")
	// room/status returns session state. After bootstrap, session is "running".
	require.Contains(t, []string{"created", "running"}, memberMap["agent-a"].State, "agent-a session state should be created or running")
	require.Contains(t, []string{"created", "running"}, memberMap["agent-b"].State, "agent-b session state should be created or running")

	// Step 5: Verify room labels survived round-trip.
	require.Equal(t, map[string]string{"env": "test"}, status.Labels, "room labels should match")

	// Step 6: Stop both agents (required before delete).
	agentStop(ctx, t, client, agentResultA.AgentId)
	agentStop(ctx, t, client, agentResultB.AgentId)

	// Step 7: Delete the room → verify success (sessions are stopped).
	roomDelete(ctx, t, client, "test-room")

	// Step 8: Call room/status → verify room not found error.
	var statusResult ari.RoomStatusResult
	err := client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "test-room"}, &statusResult)
	require.Error(t, err, "room/status should fail after delete")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// TestARIRoomCreateDuplicate verifies room/create rejects duplicate room names.
func TestARIRoomCreateDuplicate(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create the room.
	roomCreate(ctx, t, client, "dup-room", "mesh", nil)

	// Attempt to create the same room again — should fail.
	var result ari.RoomCreateResult
	err := client.Call(ctx, "room/create", ari.RoomCreateParams{
		Name: "dup-room",
	}, &result)
	require.Error(t, err, "room/create should fail for duplicate name")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "already exists")
}

// TestARIRoomDeleteWithActiveMembers verifies room/delete rejects when
// non-stopped agents are associated with the room.
func TestARIRoomDeleteWithActiveMembers(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create room.
	roomCreate(ctx, t, client, "active-room", "star", nil)

	// Prepare workspace and create an agent in the room (state=creating → active/non-stopped).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "active-room-ws")
	agentResult := agentCreate(ctx, t, client, "active-room", "agent-x", "default", workspaceId)

	// Attempt to delete the room — should fail because agent is in "creating" state (active).
	var deleteResult interface{}
	err := client.Call(ctx, "room/delete", ari.RoomDeleteParams{Name: "active-room"}, &deleteResult)
	require.Error(t, err, "room/delete should fail with active members")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "active member")

	// Stop the agent first (transitions session to stopped).
	agentStop(ctx, t, client, agentResult.AgentId)

	// Now delete should succeed (all sessions are stopped).
	roomDelete(ctx, t, client, "active-room")
}

// TestARISessionNewRoomValidation verifies agent/create rejects:
// - room='nonexistent' → error mentioning room/create
// This enforces room existence validation.
func TestARISessionNewRoomValidation(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare a workspace (needed for agent/create params).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "room-validation-ws")

	// Test 1: room='nonexistent' → error mentioning room/create.
	var createResult ari.AgentCreateResult
	err := client.Call(ctx, "agent/create", ari.AgentCreateParams{
		Room:         "nonexistent",
		Name:         "agent-z",
		RuntimeClass: "default",
		WorkspaceId:  workspaceId,
	}, &createResult)
	require.Error(t, err, "agent/create should fail for nonexistent room")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "room/create")
}

// TestARIRoomCommunicationModes verifies that rooms can be created with
// each communication mode (mesh/star/isolated) and room/status returns
// the correct mode.
func TestARIRoomCommunicationModes(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	modes := []struct {
		name string
		mode string
	}{
		{"mode-mesh-room", "mesh"},
		{"mode-star-room", "star"},
		{"mode-isolated-room", "isolated"},
	}

	for _, m := range modes {
		t.Run(m.mode, func(t *testing.T) {
			// Create room with this mode.
			createResult := roomCreate(ctx, t, client, m.name, m.mode, nil)
			require.Equal(t, m.mode, createResult.CommunicationMode,
				"room/create should return mode=%s", m.mode)

			// Verify room/status also returns the correct mode.
			status := roomStatus(ctx, t, client, m.name)
			require.Equal(t, m.mode, status.CommunicationMode,
				"room/status should return mode=%s", m.mode)
		})
	}

	// Also test the default mode (no communication specified).
	t.Run("default-mesh", func(t *testing.T) {
		createResult := roomCreate(ctx, t, client, "mode-default-room", "", nil)
		require.Equal(t, "mesh", createResult.CommunicationMode,
			"default communication mode should be mesh")
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Room Send Integration Tests
// ────────────────────────────────────────────────────────────────────────────

// roomSend is a test helper that calls room/send via JSON-RPC.
func roomSend(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, room, targetAgent, message, senderAgent, senderId string) ari.RoomSendResult {
	t.Helper()
	var result ari.RoomSendResult
	err := conn.Call(ctx, "room/send", ari.RoomSendParams{
		Room:        room,
		TargetAgent: targetAgent,
		Message:     message,
		SenderAgent: senderAgent,
		SenderId:    senderId,
	}, &result)
	require.NoError(t, err, "room/send should succeed for target %q in room %q", targetAgent, room)
	return result
}

// TestARIRoomSendBasic tests the happy path: create room, create 2 agents,
// send a message from agent-a to agent-b via room/send, verify delivery.
func TestARIRoomSendBasic(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create room.
	roomCreate(ctx, t, client, "send-room", "mesh", nil)

	// Step 2: Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "room-send-ws")

	// Step 3: Create 2 agents in the room (async bootstrap).
	agentResultA := agentCreate(ctx, t, client, "send-room", "agent-a", "mockagent", workspaceId)
	agentResultB := agentCreate(ctx, t, client, "send-room", "agent-b", "mockagent", workspaceId)

	// Wait for bootstrap — session must exist before room/send can route.
	pollAgentUntilReady(ctx, t, client, agentResultA.AgentId)
	pollAgentUntilReady(ctx, t, client, agentResultB.AgentId)

	// Step 4: Send message from agent-a to agent-b via room/send.
	result := roomSend(ctx, t, client, "send-room", "agent-b", "hello from a", "agent-a", agentResultA.AgentId)
	require.True(t, result.Delivered, "message should be delivered")
	// StopReason depends on mockagent behavior; just verify it's non-empty.
	require.NotEmpty(t, result.StopReason, "stopReason should be non-empty")

	// Cleanup: stop agents.
	agentStop(ctx, t, client, agentResultA.AgentId)
	agentStop(ctx, t, client, agentResultB.AgentId)
}

// TestARIRoomSendErrors tests error paths for room/send:
// 1. Room not found
// 2. Target agent not in room
// 3. Target agent stopped
// 4. Missing required fields (room, targetAgent, message)
func TestARIRoomSendErrors(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Subtest 1: Room not found.
	t.Run("room not found", func(t *testing.T) {
		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "nonexistent-room",
			TargetAgent: "agent-x",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail for nonexistent room")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
	})

	// Create a room and a workspace + session for the remaining subtests.
	roomCreate(ctx, t, client, "error-room", "mesh", nil)
	wsID := uuid.New().String()
	h.createWorkspaceInStore(ctx, t, wsID, "error-ws", t.TempDir())

	// Create agent-a in error-room (with proper agent+session pair).
	agentCreate(ctx, t, client, "error-room", "agent-a", "default", wsID)

	// Subtest 2: Target agent not in room.
	t.Run("target agent not in room", func(t *testing.T) {
		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "error-room",
			TargetAgent: "nonexistent-agent",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail for nonexistent agent")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found in room")
	})

	// Subtest 3: Target agent stopped — create agent, stop it, then room/send should fail.
	// Note: with async bootstrap and no real runtime, the session may not exist when
	// room/send runs (agent is in error/stopped state). We accept "is stopped" or
	// "not found in room" as valid error messages — both indicate the target is unavailable.
	t.Run("target agent stopped", func(t *testing.T) {
		stoppedAgentResult := agentCreate(ctx, t, client, "error-room", "agent-stopped", "default", wsID)
		// Wait for bootstrap to settle (error or creating), then stop.
		time.Sleep(100 * time.Millisecond)
		agentStop(ctx, t, client, stoppedAgentResult.AgentId)

		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "error-room",
			TargetAgent: "agent-stopped",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail for stopped/unavailable agent")
		var rpcErr *jsonrpc2.Error
		require.ErrorAs(t, err, &rpcErr)
		require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code))
		// Accept "is stopped" (session exists and is stopped) or "not found in room"
		// (no session created — bootstrap failed before session was persisted).
		require.True(t, strings.Contains(rpcErr.Message, "is stopped") || strings.Contains(rpcErr.Message, "not found in room"),
			"expected 'is stopped' or 'not found in room', got: %s", rpcErr.Message)
	})

	// Subtest 4: Missing required fields.
	t.Run("missing room", func(t *testing.T) {
		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "",
			TargetAgent: "agent-a",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail with empty room")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "room is required")
	})

	t.Run("missing targetAgent", func(t *testing.T) {
		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "error-room",
			TargetAgent: "",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail with empty targetAgent")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "targetAgent is required")
	})

	t.Run("missing message", func(t *testing.T) {
		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "error-room",
			TargetAgent: "agent-a",
			Message:     "",
		}, &result)
		require.Error(t, err, "room/send should fail with empty message")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "message is required")
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Full-Stack Room Send Integration Tests (real mockagent processes)
// ────────────────────────────────────────────────────────────────────────────

// TestARIRoomSendDelivery proves the complete room/send delivery path with
// real mockagent processes:
//  1. Create room "routing-test"
//  2. Create agents for agent-a and agent-b
//  3. room/send from agent-a → agent-b
//  4. Assert Delivered==true and StopReason non-empty
//  5. Verify agent-b's linked session state is "running" (auto-started by room/send)
func TestARIRoomSendDelivery(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create room.
	roomCreate(ctx, t, client, "routing-test", "mesh", nil)

	// Step 2: Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "routing-test-ws")

	// Step 3: Create agent-a and agent-b (async bootstrap).
	agentResultA := agentCreate(ctx, t, client, "routing-test", "agent-a", "mockagent", workspaceId)
	agentResultB := agentCreate(ctx, t, client, "routing-test", "agent-b", "mockagent", workspaceId)

	// Wait for both agents to finish bootstrapping before sending messages.
	statusA := pollAgentUntilReady(ctx, t, client, agentResultA.AgentId)
	require.Equal(t, "created", statusA.Agent.State, "agent-a should be 'created' after bootstrap")
	statusB := pollAgentUntilReady(ctx, t, client, agentResultB.AgentId)
	require.Equal(t, "created", statusB.Agent.State, "agent-b should be 'created' after bootstrap")

	// Step 4: Send message from agent-a to agent-b via room/send.
	result := roomSend(ctx, t, client, "routing-test", "agent-b", "hello from architect", "agent-a", agentResultA.AgentId)

	// Step 5: Assert delivery succeeded.
	require.True(t, result.Delivered, "message should be delivered to agent-b")
	require.NotEmpty(t, result.StopReason, "stopReason should be non-empty (mockagent returns end_turn)")

	// Step 6: Verify agent-b's status (auto-started linked session).
	var agentStatusResult ari.AgentStatusResult
	err := client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: agentResultB.AgentId}, &agentStatusResult)
	require.NoError(t, err, "agent/status for agent-b should succeed")
	// State reflects the agent-level state (may still be "created" while session is "running").
	// The key proof is the delivery succeeded; session state verified via room/status.
	status := roomStatus(ctx, t, client, "routing-test")
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-b"].State,
		"agent-b session should be 'running' after room/send auto-started it")

	// Cleanup: stop both agents.
	agentStop(ctx, t, client, agentResultA.AgentId)
	agentStop(ctx, t, client, agentResultB.AgentId)
}

// TestARIRoomSendToStoppedTarget proves that room/send returns an error when
// the target agent has been explicitly stopped. Uses real mockagent processes.
func TestARIRoomSendToStoppedTarget(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create room.
	roomCreate(ctx, t, client, "stopped-target-room", "mesh", nil)

	// Step 2: Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "stopped-target-ws")

	// Step 3: Create agent-a (sender) and agent-b (target).
	agentResultA := agentCreate(ctx, t, client, "stopped-target-room", "agent-a", "mockagent", workspaceId)
	agentResultB := agentCreate(ctx, t, client, "stopped-target-room", "agent-b", "mockagent", workspaceId)

	// Wait for both agents to finish bootstrapping.
	pollAgentUntilReady(ctx, t, client, agentResultA.AgentId)
	pollAgentUntilReady(ctx, t, client, agentResultB.AgentId)

	// Step 4: Start agent-b by sending a prompt (auto-start linked session).
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: agentResultB.AgentId,
		Prompt:  "warmup prompt",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt for agent-b should succeed (auto-start)")

	// Step 5: Stop agent-b.
	agentStop(ctx, t, client, agentResultB.AgentId)

	// Wait for shim process to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Verify agent-b session is stopped via room/status.
	status := roomStatus(ctx, t, client, "stopped-target-room")
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "stopped", memberMap["agent-b"].State, "agent-b session should be stopped")

	// Step 6: Attempt room/send targeting stopped agent-b — should fail.
	var sendResult ari.RoomSendResult
	err = client.Call(ctx, "room/send", ari.RoomSendParams{
		Room:        "stopped-target-room",
		TargetAgent: "agent-b",
		Message:     "hello from architect",
		SenderAgent: "agent-a",
		SenderId:    agentResultA.AgentId,
	}, &sendResult)
	require.Error(t, err, "room/send to stopped agent-b should fail")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "is stopped")

	// Cleanup: stop agent-a.
	agentStop(ctx, t, client, agentResultA.AgentId)
}

// TestARIMultiAgentRoundTrip proves the full Room lifecycle with 3-agent
// bidirectional message exchange: create Room → bootstrap 3 agents via agent/create →
// bidirectional message exchange via room/send → verify delivery, state
// transitions, and attribution → clean teardown. All via ARI.
func TestARIMultiAgentRoundTrip(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// ── Step 1: Create room ────────────────────────────────────────────────
	roomCreate(ctx, t, client, "multi-agent-room", "mesh", nil)

	// ── Step 2: Prepare shared workspace ────────────────────────────────────
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "multi-agent-ws")

	// ── Step 3: Create 3 agents (async bootstrap) ────────────────────────
	resultA := agentCreate(ctx, t, client, "multi-agent-room", "agent-a", "mockagent", workspaceId)
	resultB := agentCreate(ctx, t, client, "multi-agent-room", "agent-b", "mockagent", workspaceId)
	resultC := agentCreate(ctx, t, client, "multi-agent-room", "agent-c", "mockagent", workspaceId)

	// Wait for all three agents to finish bootstrapping before sending messages.
	pollAgentUntilReady(ctx, t, client, resultA.AgentId)
	pollAgentUntilReady(ctx, t, client, resultB.AgentId)
	pollAgentUntilReady(ctx, t, client, resultC.AgentId)

	// ── Step 4: Verify room has 3 members, all in "created" state ──────────
	status := roomStatus(ctx, t, client, "multi-agent-room")
	require.Len(t, status.Members, 3, "room should have 3 members")

	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	// room/status returns session state, not agent state. After bootstrap,
	// session is "running" (started by processManager). Agent state is "created".
	require.Contains(t, []string{"created", "running"}, memberMap["agent-a"].State, "agent-a session should be created or running after bootstrap")
	require.Contains(t, []string{"created", "running"}, memberMap["agent-b"].State, "agent-b session should be created or running after bootstrap")
	require.Contains(t, []string{"created", "running"}, memberMap["agent-c"].State, "agent-c session should be created or running after bootstrap")

	// ── Step 5: A→B message — verify delivery ──────────────────────────────
	resultAB := roomSend(ctx, t, client, "multi-agent-room", "agent-b", "hello from a", "agent-a", resultA.AgentId)
	require.True(t, resultAB.Delivered, "A→B message should be delivered")
	require.NotEmpty(t, resultAB.StopReason, "A→B stopReason should be non-empty")

	// ── Step 6: Verify agent-b is now "running" (auto-started) ─────────────
	status = roomStatus(ctx, t, client, "multi-agent-room")
	memberMap = make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should be 'running' after receiving A→B message")

	// ── Step 7: B→A message — bidirectional proof ──────────────────────────
	resultBA := roomSend(ctx, t, client, "multi-agent-room", "agent-a", "reply from b", "agent-b", resultB.AgentId)
	require.True(t, resultBA.Delivered, "B→A message should be delivered")
	require.NotEmpty(t, resultBA.StopReason, "B→A stopReason should be non-empty")

	// ── Step 8: A→C message — 3rd agent participation proof ────────────────
	resultAC := roomSend(ctx, t, client, "multi-agent-room", "agent-c", "hello from a to c", "agent-a", resultA.AgentId)
	require.True(t, resultAC.Delivered, "A→C message should be delivered")
	require.NotEmpty(t, resultAC.StopReason, "A→C stopReason should be non-empty")

	// ── Step 9: Stop all 3 agents ──────────────────────────────────────────
	agentStop(ctx, t, client, resultA.AgentId)
	agentStop(ctx, t, client, resultB.AgentId)
	agentStop(ctx, t, client, resultC.AgentId)

	// Wait for shim processes to fully exit.
	time.Sleep(500 * time.Millisecond)

	// ── Step 10: Delete the room ───────────────────────────────────────────
	roomDelete(ctx, t, client, "multi-agent-room")

	// ── Step 11: Verify room is gone ───────────────────────────────────────
	var statusAfterDelete ari.RoomStatusResult
	err := client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "multi-agent-room"}, &statusAfterDelete)
	require.Error(t, err, "room/status should fail after room delete")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// TestARIRoomTeardownGuards proves the teardown ordering constraints:
// 1. room/delete fails when agents are still active (running or created)
// 2. agent/delete fails on a running agent (ErrDeleteNotStopped)
// 3. Both operations succeed after all agents are properly stopped.
func TestARIRoomTeardownGuards(t *testing.T) {
	if testing.Short() {
		t.Skip("requires mockagent processes")
	}

	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create room and prepare workspace.
	roomCreate(ctx, t, client, "teardown-guard-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "teardown-guard-ws")

	// Create 2 agents (async bootstrap).
	resultA := agentCreate(ctx, t, client, "teardown-guard-room", "agent-a", "mockagent", workspaceId)
	resultB := agentCreate(ctx, t, client, "teardown-guard-room", "agent-b", "mockagent", workspaceId)

	// Wait for both agents to finish bootstrapping before sending messages.
	pollAgentUntilReady(ctx, t, client, resultA.AgentId)
	pollAgentUntilReady(ctx, t, client, resultB.AgentId)

	// Send A→B to auto-start agent-b to "running".
	result := roomSend(ctx, t, client, "teardown-guard-room", "agent-b", "hello from a", "agent-a", resultA.AgentId)
	require.True(t, result.Delivered, "A→B message should be delivered")

	// Verify agent-b is now running.
	status := roomStatus(ctx, t, client, "teardown-guard-room")
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should be 'running' after receiving message")

	// room/delete should FAIL — agent-b is running (session is non-stopped).
	var deleteResult interface{}
	err := client.Call(ctx, "room/delete", ari.RoomDeleteParams{Name: "teardown-guard-room"}, &deleteResult)
	require.Error(t, err, "room/delete should fail with active members")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "active member")

	// agent/delete on running agent-b should FAIL.
	err = client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: resultB.AgentId}, &deleteResult)
	require.Error(t, err, "agent/delete on running agent should fail")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "stopped")

	// Stop both agents.
	agentStop(ctx, t, client, resultB.AgentId)
	time.Sleep(500 * time.Millisecond)
	agentStop(ctx, t, client, resultA.AgentId)
	time.Sleep(500 * time.Millisecond)

	// room/delete should SUCCEED now.
	roomDelete(ctx, t, client, "teardown-guard-room")

	// Verify room is gone.
	var statusAfterDelete ari.RoomStatusResult
	err = client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "teardown-guard-room"}, &statusAfterDelete)
	require.Error(t, err, "room/status should fail after room delete")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// ────────────────────────────────────────────────────────────────────────────
// Agent lifecycle helpers and integration tests
// ────────────────────────────────────────────────────────────────────────────

// agentCreate is a test helper that calls agent/create via JSON-RPC.
// Returns the AgentCreateResult with state "creating" (async bootstrap).
// Callers that need the agent ready before sending prompts should call
// pollAgentUntilReady to wait for state "created" or "error".
func agentCreate(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, room, name, runtimeClass, workspaceId string) ari.AgentCreateResult {
	t.Helper()
	var result ari.AgentCreateResult
	err := conn.Call(ctx, "agent/create", ari.AgentCreateParams{
		Room:         room,
		Name:         name,
		RuntimeClass: runtimeClass,
		WorkspaceId:  workspaceId,
	}, &result)
	require.NoError(t, err, "agent/create should succeed for agent %q in room %q", name, room)
	require.Equal(t, "creating", result.State, "agent/create should return state 'creating' immediately")
	return result
}

// pollAgentUntilReady polls agent/status until state is no longer "creating"
// (i.e., state becomes "created" or "error"). Returns the final AgentStatusResult.
// Times out after maxWait; fails the test if timeout is reached or a poll fails.
func pollAgentUntilReady(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, agentId string) ari.AgentStatusResult {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var statusResult ari.AgentStatusResult
		err := conn.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: agentId}, &statusResult)
		require.NoError(t, err, "agent/status poll should succeed for agent %q", agentId)
		if statusResult.Agent.State != "creating" {
			return statusResult
		}
		if time.Now().After(deadline) {
			t.Fatalf("pollAgentUntilReady: agent %q still in 'creating' state after 30s", agentId)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// agentStop is a test helper that calls agent/stop via JSON-RPC.
func agentStop(ctx context.Context, t *testing.T, conn *jsonrpc2.Conn, agentId string) {
	t.Helper()
	var result interface{}
	err := conn.Call(ctx, "agent/stop", ari.AgentStopParams{AgentId: agentId}, &result)
	require.NoError(t, err, "agent/stop should succeed for agent %q", agentId)
}

// TestARIAgentCreateAndList creates an agent and verifies agent/list returns it.
func TestARIAgentCreateAndList(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create room + workspace.
	roomCreate(ctx, t, client, "list-agent-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "list-agent-ws")

	// Create agent — returns "creating" immediately (async bootstrap).
	result := agentCreate(ctx, t, client, "list-agent-room", "lister-agent", "default", workspaceId)
	require.NotEmpty(t, result.AgentId, "agentId should be non-empty")
	require.Equal(t, "creating", result.State, "initial state should be 'creating' (async)")

	// agent/list should return the created agent (may be in creating or error state
	// since newTestHarness has no real runtime to bootstrap the session).
	var listResult ari.AgentListResult
	err := client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed")
	require.Len(t, listResult.Agents, 1, "agent list should have 1 entry")
	require.Equal(t, result.AgentId, listResult.Agents[0].AgentId, "agentId should match")
	require.Equal(t, "lister-agent", listResult.Agents[0].Name, "name should match")
	require.Equal(t, "list-agent-room", listResult.Agents[0].Room, "room should match")
}

// TestARIAgentCreateDuplicateName verifies that creating two agents with the same
// (room, name) pair returns an error on the second call.
func TestARIAgentCreateDuplicateName(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "dup-agent-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "dup-agent-ws")

	// Create first agent — should succeed.
	agentCreate(ctx, t, client, "dup-agent-room", "dup-agent", "default", workspaceId)

	// Create second agent with the same name — should fail.
	var result ari.AgentCreateResult
	err := client.Call(ctx, "agent/create", ari.AgentCreateParams{
		Room:         "dup-agent-room",
		Name:         "dup-agent",
		RuntimeClass: "default",
		WorkspaceId:  workspaceId,
	}, &result)
	require.Error(t, err, "second agent/create with same room+name should fail")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for duplicate agent, got %d", rpcErr.Code)
}

// TestARIAgentCreateMissingRoom verifies that agent/create with a non-existent room returns an error.
func TestARIAgentCreateMissingRoom(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "missing-room-ws")

	var result ari.AgentCreateResult
	err := client.Call(ctx, "agent/create", ari.AgentCreateParams{
		Room:         "nonexistent-room",
		Name:         "agent-x",
		RuntimeClass: "default",
		WorkspaceId:  workspaceId,
	}, &result)
	require.Error(t, err, "agent/create should fail for nonexistent room")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nonexistent room, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "room/create",
		"error message should mention room/create")
}

// TestARIAgentStatus creates an agent and verifies agent/status returns correct state.
func TestARIAgentStatus(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "status-agent-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "status-agent-ws")

	createResult := agentCreate(ctx, t, client, "status-agent-room", "status-agent", "default", workspaceId)

	var statusResult ari.AgentStatusResult
	err := client.Call(ctx, "agent/status", ari.AgentStatusParams{AgentId: createResult.AgentId}, &statusResult)
	require.NoError(t, err, "agent/status should succeed")
	require.Equal(t, createResult.AgentId, statusResult.Agent.AgentId, "agentId should match")
	// Agent is in "creating" state immediately after create (no real runtime in newTestHarness).
	require.Contains(t, []string{"creating", "error"}, statusResult.Agent.State,
		"state should be 'creating' or 'error' (no real runtime in this harness)")
	require.Equal(t, "status-agent-room", statusResult.Agent.Room, "room should match")
	require.Nil(t, statusResult.ShimState, "shimState should be nil for non-running agent")
}

// TestARIAgentDeleteRequiresStopped verifies that agent/delete fails when the agent is not stopped.
func TestARIAgentDeleteRequiresStopped(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "del-guard-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "del-guard-ws")

	createResult := agentCreate(ctx, t, client, "del-guard-room", "del-guard-agent", "default", workspaceId)

	// Attempt agent/delete without stopping first — should fail.
	var deleteResult interface{}
	err := client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: createResult.AgentId}, &deleteResult)
	require.Error(t, err, "agent/delete should fail when agent is not stopped")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for delete of non-stopped agent, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "stopped",
		"error message should mention 'stopped'")
}

// TestARIAgentDeleteAfterStop verifies that agent/stop → agent/delete succeeds.
func TestARIAgentDeleteAfterStop(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "del-ok-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "del-ok-ws")

	createResult := agentCreate(ctx, t, client, "del-ok-room", "del-ok-agent", "default", workspaceId)

	// Stop the agent first.
	agentStop(ctx, t, client, createResult.AgentId)

	// Now delete should succeed.
	var deleteResult interface{}
	err := client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: createResult.AgentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed after agent/stop")

	// agent/list should be empty.
	var listResult ari.AgentListResult
	err = client.Call(ctx, "agent/list", ari.AgentListParams{}, &listResult)
	require.NoError(t, err, "agent/list should succeed")
	require.Empty(t, listResult.Agents, "agent list should be empty after delete")
}

// TestARISessionMethodsRemoved verifies that session/new (and other session/* methods)
// now return MethodNotFound, confirming the dispatch table migration.
func TestARISessionMethodsRemoved(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	sessionMethods := []string{
		"session/new",
		"session/prompt",
		"session/cancel",
		"session/stop",
		"session/remove",
		"session/list",
		"session/status",
		"session/attach",
		"session/detach",
	}

	for _, method := range sessionMethods {
		t.Run(method, func(t *testing.T) {
			var result interface{}
			err := client.Call(ctx, method, nil, &result)
			require.Error(t, err, "%s should return an error", method)

			var rpcErr *jsonrpc2.Error
			require.ErrorAs(t, err, &rpcErr)
			require.Equal(t, int64(jsonrpc2.CodeMethodNotFound), int64(rpcErr.Code),
				"%s should return CodeMethodNotFound, got %d", method, rpcErr.Code)
		})
	}
}

// TestARIAgentRestartStub verifies that agent/restart returns MethodNotFound (stub).
func TestARIAgentRestartStub(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	var result interface{}
	err := client.Call(ctx, "agent/restart", ari.AgentRestartParams{AgentId: "any-id"}, &result)
	require.Error(t, err, "agent/restart should return error (stub)")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeMethodNotFound), int64(rpcErr.Code),
		"agent/restart should return CodeMethodNotFound, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not implemented",
		"error message should mention 'not implemented'")
}

// TestARIAgentCreateAsync verifies that agent/create returns "creating" immediately
// and the agent transitions to "created" after background bootstrap completes.
func TestARIAgentCreateAsync(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "async-create-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "async-create-ws")

	// agent/create should return "creating" immediately.
	createResult := agentCreate(ctx, t, client, "async-create-room", "async-agent", "mockagent", workspaceId)
	require.Equal(t, "creating", createResult.State, "agent/create must return 'creating' immediately")

	// Poll until state transitions out of "creating".
	statusResult := pollAgentUntilReady(ctx, t, client, createResult.AgentId)
	require.Equal(t, "created", statusResult.Agent.State, "agent should reach 'created' after bootstrap")

	// ShimState should be present because bootstrap called processes.Start successfully.
	require.NotNil(t, statusResult.ShimState, "shimState should be present after successful bootstrap")

	// Cleanup.
	agentStop(ctx, t, client, createResult.AgentId)
	var deleteResult interface{}
	err := client.Call(ctx, "agent/delete", ari.AgentDeleteParams{AgentId: createResult.AgentId}, &deleteResult)
	require.NoError(t, err, "agent/delete should succeed after stop")
}

// TestARIAgentCreateAsyncErrorState verifies that agent/create with an invalid
// runtimeClass returns "creating" immediately, then the agent transitions to "error".
func TestARIAgentCreateAsyncErrorState(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "async-err-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "async-err-ws")

	// agent/create with nonexistent runtimeClass — should return "creating" immediately.
	createResult := agentCreate(ctx, t, client, "async-err-room", "error-agent", "nonexistent-class", workspaceId)
	require.Equal(t, "creating", createResult.State, "agent/create must return 'creating' immediately")

	// Poll until state transitions out of "creating".
	statusResult := pollAgentUntilReady(ctx, t, client, createResult.AgentId)
	require.Equal(t, "error", statusResult.Agent.State, "agent should reach 'error' when runtimeClass is invalid")
	require.NotEmpty(t, statusResult.Agent.ErrorMessage, "error message should be non-empty")
}

// TestARIAgentPrompt verifies agent/create → agent/prompt with a real shim.
func TestARIAgentPrompt(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "prompt-agent-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "prompt-agent-ws")

	createResult := agentCreate(ctx, t, client, "prompt-agent-room", "prompt-agent", "mockagent", workspaceId)

	// Wait for bootstrap to complete before sending prompt.
	pollAgentUntilReady(ctx, t, client, createResult.AgentId)

	// Send a prompt.
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "hello mockagent",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed")
	require.NotEmpty(t, promptResult.StopReason, "stopReason should be non-empty")

	// Cleanup.
	agentStop(ctx, t, client, createResult.AgentId)
}

// TestARIAgentAttach verifies agent/create → agent/prompt → agent/attach returns a non-empty socket path.
func TestARIAgentAttach(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	roomCreate(ctx, t, client, "attach-agent-room", "mesh", nil)
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "attach-agent-ws")

	createResult := agentCreate(ctx, t, client, "attach-agent-room", "attach-agent", "mockagent", workspaceId)

	// Wait for bootstrap to complete before prompting.
	pollAgentUntilReady(ctx, t, client, createResult.AgentId)

	// Auto-start the agent via a prompt.
	var promptResult ari.AgentPromptResult
	err := client.Call(ctx, "agent/prompt", ari.AgentPromptParams{
		AgentId: createResult.AgentId,
		Prompt:  "start up",
	}, &promptResult)
	require.NoError(t, err, "agent/prompt should succeed")

	// agent/attach should return a non-empty socket path.
	var attachResult ari.AgentAttachResult
	err = client.Call(ctx, "agent/attach", ari.AgentAttachParams{AgentId: createResult.AgentId}, &attachResult)
	require.NoError(t, err, "agent/attach should succeed")
	require.NotEmpty(t, attachResult.SocketPath, "socketPath should be non-empty")

	// Cleanup.
	agentStop(ctx, t, client, createResult.AgentId)
}

// ────────────────────────────────────────────────────────────────────────────
// Suppress unused variable warning for harness field
// ────────────────────────────────────────────────────────────────────────────

var _ = (*testHarness)(nil)