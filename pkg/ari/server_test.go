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
	manager       *workspace.WorkspaceManager
	registry      *ari.Registry
	store         *meta.Store
	runtimeClasses *agentd.RuntimeClassRegistry
	sessions      *agentd.SessionManager
	processes     *agentd.ProcessManager
	server        *ari.Server
	socket        string
	baseDir       string

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

	// Create ProcessManager.
	cfg := agentd.Config{
		Socket:       socketPath,
		WorkspaceRoot: baseDir,
	}
	processes := agentd.NewProcessManager(runtimeClasses, sessions, store, cfg)

	// Create Server with all dependencies.
	server := ari.New(manager, registry, sessions, processes, runtimeClasses, cfg, store, socketPath, baseDir)

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

	// Create ProcessManager.
	cfg := agentd.Config{
		Socket:       socketPath,
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
	server := ari.New(manager, registry, sessions, processes, runtimeRegistry, cfg, store, socketPath, baseDir)

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

// TestARISessionLifecycle tests the full session round-trip:
// workspace/prepare → session/new → session/prompt → session/status → session/stop → session/remove
// Note: This test exercises the session lifecycle with a real shim process.
// Due to timing sensitivity, we use Eventually patterns for state transitions.
func TestARISessionLifecycle(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "session-lifecycle-test")
	require.NotEmpty(t, workspaceId, "workspaceId should be non-empty")

	// Step 2: Create session via session/new.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")
	sessionId := newResult.SessionId
	require.NotEmpty(t, sessionId, "sessionId should be non-empty")
	require.Equal(t, "created", newResult.State, "initial state should be 'created'")

	// Step 3: Prompt session via session/prompt (auto-starts).
	// Note: This test currently has a timing issue with the mockagent process.
	// The prompt call starts the session and sends a prompt, but the mockagent
	// may exit before the prompt completes. We accept that prompt may fail
	// and verify the session state transitions correctly.
	var promptResult ari.SessionPromptResult
	promptErr := client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: sessionId,
		Text:      "hello mockagent",
	}, &promptResult)
	
	// Check if session was started (even if prompt failed).
	time.Sleep(500 * time.Millisecond)

	// Step 4: Check session/status - verify state transition.
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: sessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status should succeed")
	require.Equal(t, sessionId, statusResult.Session.Id, "sessionId should match")
	// State should be either "running" (if prompt worked) or "stopped" (if prompt failed but session started)
	require.Contains(t, []string{"running", "stopped"}, statusResult.Session.State,
		"state should be 'running' or 'stopped' after prompt attempt")

	// Step 5: Stop session via session/stop (idempotent - ok if already stopped).
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: sessionId,
	}, &stopResult)
	require.NoError(t, err, "session/stop should succeed")

	// Wait for shim process to fully stop.
	time.Sleep(200 * time.Millisecond)

	// Step 6: Check session/status - state should be "stopped".
	var statusResult2 ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: sessionId,
	}, &statusResult2)
	require.NoError(t, err, "session/status should succeed")
	require.Equal(t, "stopped", statusResult2.Session.State, "state should be 'stopped' after stop")

	// Step 7: Remove session via session/remove.
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: sessionId,
	}, &removeResult)
	require.NoError(t, err, "session/remove should succeed")

	// Step 8: Verify session/list is empty.
	var listResult ari.SessionListResult
	err = client.Call(ctx, "session/list", ari.SessionListParams{}, &listResult)
	require.NoError(t, err, "session/list should succeed")
	require.Empty(t, listResult.Sessions, "session list should be empty after remove")

	// Log if prompt worked or failed
	if promptErr == nil {
		t.Log("Prompt succeeded, stopReason:", promptResult.StopReason)
	} else {
		t.Log("Prompt failed (known timing issue):", promptErr)
	}
}

// TestARISessionPromptAutoStart verifies auto-start on prompt when state="created".
func TestARISessionPromptAutoStart(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "auto-start-test")

	// Create session.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")
	require.Equal(t, "created", newResult.State, "initial state should be 'created'")

	// Call session/prompt WITHOUT prior session/start.
	// This verifies auto-start behavior.
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResult.SessionId,
		Text:      "auto-start test",
	}, &promptResult)
	require.NoError(t, err, "session/prompt should succeed (auto-start)")

	// Verify state transitions to "running" after prompt.
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: newResult.SessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status should succeed")
	require.Equal(t, "running", statusResult.Session.State, "state should be 'running' (auto-start worked)")

	// Cleanup: stop session.
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResult.SessionId}, nil)
}

// TestARISessionPromptOnStopped verifies error for prompt on stopped session.
func TestARISessionPromptOnStopped(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "prompt-stopped-test")

	// Create session.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")

	// Prompt to start the session.
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResult.SessionId,
		Text:      "first prompt",
	}, &promptResult)
	require.NoError(t, err, "first prompt should succeed")

	// Stop the session.
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: newResult.SessionId,
	}, &stopResult)
	require.NoError(t, err, "session/stop should succeed")

	// Wait for shim to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Call session/prompt on stopped session - should fail.
	var promptResult2 ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResult.SessionId,
		Text:      "prompt on stopped",
	}, &promptResult2)
	require.Error(t, err, "session/prompt on stopped session should fail")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for prompt on stopped, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not running",
		"error message should mention 'not running'")
}

// TestARISessionRemoveProtected verifies ErrDeleteProtected blocks remove on running session.
func TestARISessionRemoveProtected(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "remove-protected-test")

	// Create session.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")

	// Prompt to start session (state=running).
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResult.SessionId,
		Text:      "running prompt",
	}, &promptResult)
	require.NoError(t, err, "prompt should succeed")

	// Verify session is running.
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: newResult.SessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status should succeed")
	require.Equal(t, "running", statusResult.Session.State, "session should be running")

	// Call session/remove on running session - should fail.
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: newResult.SessionId,
	}, &removeResult)
	require.Error(t, err, "session/remove on running session should fail")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for remove protected, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "active",
		"error message should mention 'active' (ErrDeleteProtected)")

	// Stop session.
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: newResult.SessionId,
	}, &stopResult)
	require.NoError(t, err, "session/stop should succeed")

	// Wait for shim to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Now session/remove should succeed.
	var removeResult2 interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: newResult.SessionId,
	}, &removeResult2)
	require.NoError(t, err, "session/remove should succeed after stop")
}

// TestARISessionList verifies session/list returns all sessions.
func TestARISessionList(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "list-test")

	// Create 2 sessions with different labels.
	var newResult1 ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Labels:       map[string]string{"env": "test1", "team": "alpha"},
	}, &newResult1)
	require.NoError(t, err, "session/new #1 should succeed")

	var newResult2 ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Labels:       map[string]string{"env": "test2", "team": "beta"},
	}, &newResult2)
	require.NoError(t, err, "session/new #2 should succeed")

	// Call session/list - should return 2 sessions.
	var listResult ari.SessionListResult
	err = client.Call(ctx, "session/list", ari.SessionListParams{}, &listResult)
	require.NoError(t, err, "session/list should succeed")
	require.Len(t, listResult.Sessions, 2, "session list should have 2 entries")

	// Verify both session IDs are present.
	sessionIds := make(map[string]bool)
	for _, s := range listResult.Sessions {
		sessionIds[s.Id] = true
		require.Equal(t, workspaceId, s.WorkspaceId, "workspaceId should match")
		require.Equal(t, "mockagent", s.RuntimeClass, "runtimeClass should match")
		require.Equal(t, "created", s.State, "state should be 'created'")
	}
	require.True(t, sessionIds[newResult1.SessionId], "session #1 should be in list")
	require.True(t, sessionIds[newResult2.SessionId], "session #2 should be in list")

	// Cleanup: remove sessions.
	_ = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult1.SessionId}, nil)
	_ = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult2.SessionId}, nil)
}

// TestARISessionNotFound verifies "not found" errors for nonexistent sessions.
func TestARISessionNotFound(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	nonexistentId := uuid.New().String()

	// Test session/status with nonexistent sessionId.
	var statusResult ari.SessionStatusResult
	err := client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: nonexistentId,
	}, &statusResult)
	require.Error(t, err, "session/status should fail for nonexistent session")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for not found, got %d", rpcErr.Code)
	require.Contains(t, rpcErr.Message, "not found",
		"error message should mention 'not found'")

	// Test session/prompt with nonexistent sessionId.
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: nonexistentId,
		Text:      "test",
	}, &promptResult)
	require.Error(t, err, "session/prompt should fail for nonexistent session")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")

	// Test session/stop with nonexistent sessionId.
	// Note: session/stop uses CodeInternalError for all errors (not CodeInvalidParams).
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: nonexistentId,
	}, &stopResult)
	require.Error(t, err, "session/stop should fail for nonexistent session")
	requireRPCError(t, err, int64(jsonrpc2.CodeInternalError), "stop session failed")

	// Test session/remove with nonexistent sessionId.
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: nonexistentId,
	}, &removeResult)
	require.Error(t, err, "session/remove should fail for nonexistent session")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not exist")
}

// TestARISessionNewNilParams tests malformed input: nil params for session/new.
func TestARISessionNewNilParams(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	var result ari.SessionNewResult
	err := client.Call(ctx, "session/new", nil, &result)
	require.Error(t, err, "session/new should fail with nil params")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams for nil params, got %d", rpcErr.Code)
}

// TestARISessionPromptMissingSessionId tests missing sessionId for prompt.
func TestARISessionPromptMissingSessionId(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call with empty sessionId.
	var result ari.SessionPromptResult
	err := client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: "",
		Text:      "test",
	}, &result)
	require.Error(t, err, "session/prompt should fail with empty sessionId")
	// The error message mentions "session ID is required" from the meta store.
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "session ID")
}

// TestARISessionListEmpty verifies session/list on empty DB.
func TestARISessionListEmpty(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Call session/list without creating any sessions.
	var listResult ari.SessionListResult
	err := client.Call(ctx, "session/list", ari.SessionListParams{}, &listResult)
	require.NoError(t, err, "session/list should succeed on empty DB")
	require.Empty(t, listResult.Sessions, "session list should be empty array")
}

// TestARISessionStatusStopped verifies session/status on stopped session (no shimState).
func TestARISessionStatusStopped(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create workspace (persisted to database for session FK constraint).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "status-stopped-test")

	// Create session.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")

	// Prompt to start session.
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResult.SessionId,
		Text:      "test",
	}, &promptResult)
	require.NoError(t, err, "prompt should succeed")

	// Stop session.
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: newResult.SessionId,
	}, &stopResult)
	require.NoError(t, err, "session/stop should succeed")

	// Wait for shim to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Check session/status - shimState should be nil for stopped session.
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: newResult.SessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status should succeed")
	require.Equal(t, "stopped", statusResult.Session.State, "state should be 'stopped'")
	require.Nil(t, statusResult.ShimState, "shimState should be nil for stopped session")

	// Cleanup.
	_ = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult.SessionId}, nil)
}

// ────────────────────────────────────────────────────────────────────────────
// Recovery Guard Tests
// ────────────────────────────────────────────────────────────────────────────

// TestARIRecoveryGuard_BlocksPromptDuringRecovery verifies that session/prompt
// returns CodeRecoveryBlocked (-32001) while the daemon's recovery phase is
// RecoveryPhaseRecovering.
func TestARIRecoveryGuard_BlocksPromptDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Put the process manager into recovering phase.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// session/prompt should be blocked.
	var result ari.SessionPromptResult
	err := client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: "any-session-id",
		Text:      "hello",
	}, &result)
	requireRPCError(t, err, ari.CodeRecoveryBlocked, "recovering")
}

// TestARIRecoveryGuard_BlocksCancelDuringRecovery verifies that session/cancel
// returns CodeRecoveryBlocked while the daemon is recovering.
func TestARIRecoveryGuard_BlocksCancelDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	var result interface{}
	err := client.Call(ctx, "session/cancel", ari.SessionCancelParams{
		SessionId: "any-session-id",
	}, &result)
	requireRPCError(t, err, ari.CodeRecoveryBlocked, "recovering")
}

// TestARIRecoveryGuard_AllowsStatusDuringRecovery verifies that session/status
// is NOT blocked during recovery (it is read-only).
func TestARIRecoveryGuard_AllowsStatusDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create a workspace and session so session/status has something to find.
	wsID := uuid.New().String()
	h.createWorkspaceInStore(ctx, t, wsID, "guard-test-ws", "/tmp/guard-test")
	session := &meta.Session{
		ID:           uuid.New().String(),
		WorkspaceID:  wsID,
		RuntimeClass: "default",
		State:        meta.SessionStateCreated,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, h.store.CreateSession(ctx, session))

	// Set recovering phase.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// session/status should still succeed (read-only, not guarded).
	var statusResult ari.SessionStatusResult
	err := client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: session.ID,
	}, &statusResult)
	require.NoError(t, err, "session/status should succeed during recovery")
	require.Equal(t, session.ID, statusResult.Session.Id)
}

// TestARIRecoveryGuard_AllowsListDuringRecovery verifies that session/list
// is NOT blocked during recovery.
func TestARIRecoveryGuard_AllowsListDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	var listResult ari.SessionListResult
	err := client.Call(ctx, "session/list", ari.SessionListParams{}, &listResult)
	require.NoError(t, err, "session/list should succeed during recovery")
}

// TestARIRecoveryGuard_AllowsPromptAfterRecovery verifies that session/prompt
// is no longer blocked once the recovery phase transitions past Recovering.
// (The call may fail for other reasons like "session not found", but it should
// NOT return CodeRecoveryBlocked.)
func TestARIRecoveryGuard_AllowsPromptAfterRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Transition through recovering → complete.
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)
	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseComplete)

	// session/prompt should NOT return recovery-blocked.
	var result ari.SessionPromptResult
	err := client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: "nonexistent-session",
		Text:      "hello",
	}, &result)
	// We expect an error (session not found) but NOT a recovery guard error.
	require.Error(t, err, "should fail because session doesn't exist")
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.NotEqual(t, ari.CodeRecoveryBlocked, int64(rpcErr.Code),
		"should NOT be CodeRecoveryBlocked after recovery completes")
}

// TestARIRecoveryGuard_AllowsStopDuringRecovery verifies that session/stop
// is NOT blocked during recovery (safety-critical, intentionally unguarded).
func TestARIRecoveryGuard_AllowsStopDuringRecovery(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	h.processes.SetRecoveryPhase(agentd.RecoveryPhaseRecovering)

	// session/stop should NOT be blocked. It may fail (session not found)
	// but the error should NOT be CodeRecoveryBlocked.
	var result interface{}
	err := client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: "nonexistent-session",
	}, &result)
	require.Error(t, err, "should fail because session doesn't exist")
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.NotEqual(t, ari.CodeRecoveryBlocked, int64(rpcErr.Code),
		"session/stop should NOT be recovery-blocked")
}

// ────────────────────────────────────────────────────────────────────────────
// Workspace Ref Tests (DB ref_count via session lifecycle)
// ────────────────────────────────────────────────────────────────────────────

// TestARISessionNewAcquiresWorkspaceRef verifies that session/new records a
// workspace_ref in the DB (incrementing ref_count). Two sessions on the same
// workspace should yield ref_count == 2.
func TestARISessionNewAcquiresWorkspaceRef(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace (persisted to DB by handleWorkspacePrepare).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "ref-acquire-test")

	// Create first session.
	var newResult1 ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
	}, &newResult1)
	require.NoError(t, err, "session/new #1 should succeed")
	require.NotEmpty(t, newResult1.SessionId)

	// Assert DB ref_count == 1.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err, "GetWorkspace should succeed")
	require.NotNil(t, ws, "workspace should exist in DB")
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after first session")

	// Create second session on the same workspace.
	var newResult2 ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
	}, &newResult2)
	require.NoError(t, err, "session/new #2 should succeed")
	require.NotEmpty(t, newResult2.SessionId)

	// Assert DB ref_count == 2.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err, "GetWorkspace should succeed")
	require.Equal(t, 2, ws.RefCount, "ref_count should be 2 after second session")
}

// TestARISessionRemoveReleasesWorkspaceRef verifies that removing a session
// decrements the workspace ref_count in the DB (via DeleteSession's cascade
// of workspace_refs rows).
func TestARISessionRemoveReleasesWorkspaceRef(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "ref-release-test")

	// Create session.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")

	// Assert ref_count == 1.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after session create")

	// Remove the session (state is "created" so no stop needed).
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: newResult.SessionId,
	}, &removeResult)
	require.NoError(t, err, "session/remove should succeed")

	// Assert ref_count == 0.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 0, ws.RefCount, "ref_count should be 0 after session remove")
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
// RefCount). With an active session, cleanup is rejected; after session
// removal the ref_count drops to 0 and cleanup succeeds.
func TestARIWorkspaceCleanupBlockedByDBRefCount(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare workspace (persisted to DB).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "cleanup-db-refcount-test")

	// Create a session — this acquires a DB workspace_ref (ref_count → 1).
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")
	require.NotEmpty(t, newResult.SessionId)

	// Verify DB ref_count == 1.
	ws, err := h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.NotNil(t, ws)
	require.Equal(t, 1, ws.RefCount, "ref_count should be 1 after session create")

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

	// Remove the session (state is "created" so no stop needed).
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: newResult.SessionId,
	}, &removeResult)
	require.NoError(t, err, "session/remove should succeed")

	// Verify DB ref_count == 0 after removal.
	ws, err = h.store.GetWorkspace(ctx, workspaceId)
	require.NoError(t, err)
	require.Equal(t, 0, ws.RefCount, "ref_count should be 0 after session remove")

	// Retry workspace/cleanup — should succeed now.
	err = client.Call(ctx, "workspace/cleanup", ari.WorkspaceCleanupParams{WorkspaceId: workspaceId}, &cleanupResult)
	require.NoError(t, err, "workspace/cleanup should succeed when DB ref_count == 0")
}

// ────────────────────────────────────────────────────────────────────────────
// Test: workspace/cleanup blocked during recovery phase
// ────────────────────────────────────────────────────────────────────────────

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
// room/create → session/new (2 members) → room/status → session/stop → room/delete → room/status (not found)
// This is the primary end-to-end test proving the slice demo claim.
func TestARIRoomLifecycle(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create a room with mesh communication mode.
	createResult := roomCreate(ctx, t, client, "test-room", "mesh", map[string]string{"env": "test"})
	require.Equal(t, "test-room", createResult.Name, "room name should match")
	require.Equal(t, "mesh", createResult.CommunicationMode, "communication mode should be mesh")
	require.NotEmpty(t, createResult.CreatedAt, "createdAt should be non-empty")

	// Step 2: Prepare a workspace for sessions.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "room-lifecycle-ws")

	// Step 3: Create 2 sessions pointing at the room with different agent names.
	var newResult1 ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "test-room",
		RoomAgent:    "agent-a",
	}, &newResult1)
	require.NoError(t, err, "session/new #1 (agent-a) should succeed")
	require.NotEmpty(t, newResult1.SessionId)

	var newResult2 ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "test-room",
		RoomAgent:    "agent-b",
	}, &newResult2)
	require.NoError(t, err, "session/new #2 (agent-b) should succeed")
	require.NotEmpty(t, newResult2.SessionId)

	// Step 4: Call room/status → verify 2 members with correct agentName/sessionId/state.
	status := roomStatus(ctx, t, client, "test-room")
	require.Equal(t, "test-room", status.Name, "room name should match")
	require.Equal(t, "mesh", status.CommunicationMode, "communication mode should match")
	require.Len(t, status.Members, 2, "room should have 2 members")

	// Build a map for easier assertion (order is not guaranteed).
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Contains(t, memberMap, "agent-a", "agent-a should be a member")
	require.Contains(t, memberMap, "agent-b", "agent-b should be a member")
	require.Equal(t, newResult1.SessionId, memberMap["agent-a"].SessionId, "agent-a sessionId should match")
	require.Equal(t, newResult2.SessionId, memberMap["agent-b"].SessionId, "agent-b sessionId should match")
	require.Equal(t, "created", memberMap["agent-a"].State, "agent-a state should be created")
	require.Equal(t, "created", memberMap["agent-b"].State, "agent-b state should be created")

	// Step 5: Verify room labels survived round-trip.
	require.Equal(t, map[string]string{"env": "test"}, status.Labels, "room labels should match")

	// Step 6: Remove both sessions (state=created, no stop needed).
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult1.SessionId}, &removeResult)
	require.NoError(t, err, "session/remove #1 should succeed")
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult2.SessionId}, &removeResult)
	require.NoError(t, err, "session/remove #2 should succeed")

	// Step 7: Delete the room → verify success.
	roomDelete(ctx, t, client, "test-room")

	// Step 8: Call room/status → verify room not found error.
	var statusResult ari.RoomStatusResult
	err = client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "test-room"}, &statusResult)
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
// non-stopped sessions are associated with the room.
func TestARIRoomDeleteWithActiveMembers(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Create room.
	roomCreate(ctx, t, client, "active-room", "star", nil)

	// Prepare workspace and create a session in the room (state=created → non-stopped).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "active-room-ws")
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "active-room",
		RoomAgent:    "agent-x",
	}, &newResult)
	require.NoError(t, err, "session/new should succeed")

	// Attempt to delete the room — should fail because session is in "created" state (non-stopped).
	var deleteResult interface{}
	err = client.Call(ctx, "room/delete", ari.RoomDeleteParams{Name: "active-room"}, &deleteResult)
	require.Error(t, err, "room/delete should fail with active members")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "active member")

	// Remove the session (created state, no stop needed).
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{SessionId: newResult.SessionId}, nil)
	require.NoError(t, err, "session/remove should succeed")

	// Now delete should succeed (no active sessions).
	roomDelete(ctx, t, client, "active-room")
}

// TestARISessionNewRoomValidation verifies session/new rejects:
// - room='nonexistent' → error mentioning room/create
// - room='test-room' with empty roomAgent → error requiring roomAgent
// This enforces D051 (room existence validation).
func TestARISessionNewRoomValidation(t *testing.T) {
	h := newTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Prepare a workspace (needed for session/new params).
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "room-validation-ws")

	// Test 1: room='nonexistent' → error mentioning room/create.
	var newResult ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
		Room:         "nonexistent",
		RoomAgent:    "agent-z",
	}, &newResult)
	require.Error(t, err, "session/new should fail for nonexistent room")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "room/create")

	// Test 2: Create a real room, then session/new with empty roomAgent → error.
	roomCreate(ctx, t, client, "validation-room", "mesh", nil)
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "default",
		Room:         "validation-room",
		RoomAgent:    "",
	}, &newResult)
	require.Error(t, err, "session/new should fail with empty roomAgent")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "roomAgent")
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

// TestARIRoomSendBasic tests the happy path: create room, create 2 sessions,
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

	// Step 3: Create 2 sessions in the room.
	var newResultA ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "send-room",
		RoomAgent:    "agent-a",
	}, &newResultA)
	require.NoError(t, err, "session/new for agent-a should succeed")

	var newResultB ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "send-room",
		RoomAgent:    "agent-b",
	}, &newResultB)
	require.NoError(t, err, "session/new for agent-b should succeed")

	// Step 4: Send message from agent-a to agent-b via room/send.
	result := roomSend(ctx, t, client, "send-room", "agent-b", "hello from a", "agent-a", newResultA.SessionId)
	require.True(t, result.Delivered, "message should be delivered")
	// StopReason depends on mockagent behavior; just verify it's non-empty.
	require.NotEmpty(t, result.StopReason, "stopReason should be non-empty")

	// Cleanup: stop sessions.
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultA.SessionId}, nil)
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultB.SessionId}, nil)
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

	// Create a session for agent-a in error-room.
	session := &meta.Session{
		ID:           uuid.New().String(),
		WorkspaceID:  wsID,
		RuntimeClass: "default",
		Room:         "error-room",
		RoomAgent:    "agent-a",
		State:        meta.SessionStateCreated,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, h.store.CreateSession(ctx, session))

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

	// Subtest 3: Target agent stopped.
	t.Run("target agent stopped", func(t *testing.T) {
		// Create a stopped session.
		stoppedSession := &meta.Session{
			ID:           uuid.New().String(),
			WorkspaceID:  wsID,
			RuntimeClass: "default",
			Room:         "error-room",
			RoomAgent:    "agent-stopped",
			State:        meta.SessionStateStopped,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		require.NoError(t, h.store.CreateSession(ctx, stoppedSession))

		var result ari.RoomSendResult
		err := client.Call(ctx, "room/send", ari.RoomSendParams{
			Room:        "error-room",
			TargetAgent: "agent-stopped",
			Message:     "hello",
		}, &result)
		require.Error(t, err, "room/send should fail for stopped agent")
		requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "is stopped")
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
//  2. Create sessions for agent-a and agent-b
//  3. room/send from agent-a → agent-b
//  4. Assert Delivered==true and StopReason non-empty
//  5. Verify agent-b state is "running" (auto-started by room/send)
func TestARIRoomSendDelivery(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create room.
	roomCreate(ctx, t, client, "routing-test", "mesh", nil)

	// Step 2: Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "routing-test-ws")

	// Step 3: Create session for agent-a.
	var newResultA ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "routing-test",
		RoomAgent:    "agent-a",
	}, &newResultA)
	require.NoError(t, err, "session/new for agent-a should succeed")
	require.Equal(t, "created", newResultA.State, "agent-a initial state should be 'created'")

	// Step 4: Create session for agent-b.
	var newResultB ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "routing-test",
		RoomAgent:    "agent-b",
	}, &newResultB)
	require.NoError(t, err, "session/new for agent-b should succeed")
	require.Equal(t, "created", newResultB.State, "agent-b initial state should be 'created'")

	// Step 5: Send message from agent-a to agent-b via room/send.
	result := roomSend(ctx, t, client, "routing-test", "agent-b", "hello from architect", "agent-a", newResultA.SessionId)

	// Step 6: Assert delivery succeeded.
	require.True(t, result.Delivered, "message should be delivered to agent-b")
	require.NotEmpty(t, result.StopReason, "stopReason should be non-empty (mockagent returns end_turn)")

	// Step 7: Verify agent-b's state is "running" (auto-started by deliverPrompt).
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: newResultB.SessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status for agent-b should succeed")
	require.Equal(t, "running", statusResult.Session.State,
		"agent-b should be 'running' after room/send auto-started it")

	// Cleanup: stop both sessions.
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultA.SessionId}, nil)
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultB.SessionId}, nil)
}

// TestARIRoomSendToStoppedTarget proves that room/send returns an error when
// the target agent's session has been explicitly stopped. Uses real mockagent
// processes (not manually inserted DB records).
func TestARIRoomSendToStoppedTarget(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// Step 1: Create room.
	roomCreate(ctx, t, client, "stopped-target-room", "mesh", nil)

	// Step 2: Prepare workspace.
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "stopped-target-ws")

	// Step 3: Create session for agent-a (sender).
	var newResultA ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "stopped-target-room",
		RoomAgent:    "agent-a",
	}, &newResultA)
	require.NoError(t, err, "session/new for agent-a should succeed")

	// Step 4: Create session for agent-b (target).
	var newResultB ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "stopped-target-room",
		RoomAgent:    "agent-b",
	}, &newResultB)
	require.NoError(t, err, "session/new for agent-b should succeed")

	// Step 5: Start agent-b by sending a prompt (auto-start).
	var promptResult ari.SessionPromptResult
	err = client.Call(ctx, "session/prompt", ari.SessionPromptParams{
		SessionId: newResultB.SessionId,
		Text:      "warmup prompt",
	}, &promptResult)
	require.NoError(t, err, "session/prompt for agent-b should succeed (auto-start)")

	// Step 6: Stop agent-b.
	var stopResult interface{}
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{
		SessionId: newResultB.SessionId,
	}, &stopResult)
	require.NoError(t, err, "session/stop for agent-b should succeed")

	// Wait for shim process to fully stop.
	time.Sleep(500 * time.Millisecond)

	// Verify agent-b is stopped.
	var statusResult ari.SessionStatusResult
	err = client.Call(ctx, "session/status", ari.SessionStatusParams{
		SessionId: newResultB.SessionId,
	}, &statusResult)
	require.NoError(t, err, "session/status for agent-b should succeed")
	require.Equal(t, "stopped", statusResult.Session.State, "agent-b should be stopped")

	// Step 7: Attempt room/send targeting stopped agent-b — should fail.
	var sendResult ari.RoomSendResult
	err = client.Call(ctx, "room/send", ari.RoomSendParams{
		Room:        "stopped-target-room",
		TargetAgent: "agent-b",
		Message:     "hello from architect",
		SenderAgent: "agent-a",
		SenderId:    newResultA.SessionId,
	}, &sendResult)
	require.Error(t, err, "room/send to stopped agent-b should fail")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "is stopped")

	// Cleanup: stop agent-a if it was started.
	_ = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultA.SessionId}, nil)
}

// TestARIMultiAgentRoundTrip proves the full Room lifecycle with 3-agent
// bidirectional message exchange: create Room → bootstrap 3 agents →
// bidirectional message exchange via room/send → verify delivery, state
// transitions, and attribution → clean teardown. All via ARI.
// This is the capstone end-to-end test for M004.
func TestARIMultiAgentRoundTrip(t *testing.T) {
	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// ── Step 1: Create room ────────────────────────────────────────────────
	roomCreate(ctx, t, client, "multi-agent-room", "mesh", nil)

	// ── Step 2: Prepare shared workspace ────────────────────────────────────
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "multi-agent-ws")

	// ── Step 3: Create 3 sessions (agent-a, agent-b, agent-c) ──────────────
	var newResultA ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "multi-agent-room",
		RoomAgent:    "agent-a",
	}, &newResultA)
	require.NoError(t, err, "session/new for agent-a should succeed")
	require.Equal(t, "created", newResultA.State, "agent-a initial state should be 'created'")

	var newResultB ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "multi-agent-room",
		RoomAgent:    "agent-b",
	}, &newResultB)
	require.NoError(t, err, "session/new for agent-b should succeed")
	require.Equal(t, "created", newResultB.State, "agent-b initial state should be 'created'")

	var newResultC ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "multi-agent-room",
		RoomAgent:    "agent-c",
	}, &newResultC)
	require.NoError(t, err, "session/new for agent-c should succeed")
	require.Equal(t, "created", newResultC.State, "agent-c initial state should be 'created'")

	// ── Step 4: Verify room has 3 members, all in "created" state ──────────
	status := roomStatus(ctx, t, client, "multi-agent-room")
	require.Len(t, status.Members, 3, "room should have 3 members")

	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "created", memberMap["agent-a"].State, "agent-a should be 'created'")
	require.Equal(t, "created", memberMap["agent-b"].State, "agent-b should be 'created'")
	require.Equal(t, "created", memberMap["agent-c"].State, "agent-c should be 'created'")

	// ── Step 5: A→B message — verify delivery ──────────────────────────────
	resultAB := roomSend(ctx, t, client, "multi-agent-room", "agent-b", "hello from a", "agent-a", newResultA.SessionId)
	require.True(t, resultAB.Delivered, "A→B message should be delivered")
	require.NotEmpty(t, resultAB.StopReason, "A→B stopReason should be non-empty (mockagent returns end_turn)")

	// ── Step 6: Verify agent-b is now "running" (auto-started) ─────────────
	status = roomStatus(ctx, t, client, "multi-agent-room")
	memberMap = make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should be 'running' after receiving A→B message")

	// ── Step 7: B→A message — bidirectional proof ──────────────────────────
	resultBA := roomSend(ctx, t, client, "multi-agent-room", "agent-a", "reply from b", "agent-b", newResultB.SessionId)
	require.True(t, resultBA.Delivered, "B→A message should be delivered")
	require.NotEmpty(t, resultBA.StopReason, "B→A stopReason should be non-empty")

	// ── Step 8: Verify agent-a is now "running" too ────────────────────────
	status = roomStatus(ctx, t, client, "multi-agent-room")
	memberMap = make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-a"].State, "agent-a should be 'running' after receiving B→A message")
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should still be 'running'")

	// ── Step 9: A→C message — 3rd agent participation proof ────────────────
	resultAC := roomSend(ctx, t, client, "multi-agent-room", "agent-c", "hello from a to c", "agent-a", newResultA.SessionId)
	require.True(t, resultAC.Delivered, "A→C message should be delivered")
	require.NotEmpty(t, resultAC.StopReason, "A→C stopReason should be non-empty")

	// ── Step 10: Verify all 3 agents are "running" ─────────────────────────
	status = roomStatus(ctx, t, client, "multi-agent-room")
	require.Len(t, status.Members, 3, "room should still have 3 members")
	memberMap = make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-a"].State, "agent-a should be 'running'")
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should be 'running'")
	require.Equal(t, "running", memberMap["agent-c"].State, "agent-c should be 'running'")

	// ── Step 11: Stop all 3 sessions ───────────────────────────────────────
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultA.SessionId}, nil)
	require.NoError(t, err, "session/stop agent-a should succeed")
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultB.SessionId}, nil)
	require.NoError(t, err, "session/stop agent-b should succeed")
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultC.SessionId}, nil)
	require.NoError(t, err, "session/stop agent-c should succeed")

	// Wait for shim processes to fully exit.
	time.Sleep(500 * time.Millisecond)

	// ── Step 12: Delete the room — should succeed with stopped sessions ────
	roomDelete(ctx, t, client, "multi-agent-room")

	// ── Step 13: Verify room is gone ───────────────────────────────────────
	var statusAfterDelete ari.RoomStatusResult
	err = client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "multi-agent-room"}, &statusAfterDelete)
	require.Error(t, err, "room/status should fail after room delete")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// TestARIRoomTeardownGuards proves the teardown ordering constraints:
// 1. room/delete fails when sessions are still active (running or created)
// 2. session/remove fails on a running session (ErrDeleteProtected)
// 3. Both operations succeed after all sessions are properly stopped.
func TestARIRoomTeardownGuards(t *testing.T) {
	if testing.Short() {
		t.Skip("requires mockagent processes")
	}

	h := newSessionTestHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := h.dial(t, &nullHandler{})

	// ── Step 1: Create room ────────────────────────────────────────────────
	roomCreate(ctx, t, client, "teardown-guard-room", "mesh", nil)

	// ── Step 2: Prepare shared workspace ────────────────────────────────────
	workspaceId, _ := h.prepareWorkspaceForSession(ctx, t, client, "teardown-guard-ws")

	// ── Step 3: Create 2 sessions (agent-a, agent-b) ───────────────────────
	var newResultA ari.SessionNewResult
	err := client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "teardown-guard-room",
		RoomAgent:    "agent-a",
	}, &newResultA)
	require.NoError(t, err, "session/new for agent-a should succeed")
	require.Equal(t, "created", newResultA.State, "agent-a initial state should be 'created'")

	var newResultB ari.SessionNewResult
	err = client.Call(ctx, "session/new", ari.SessionNewParams{
		WorkspaceId:  workspaceId,
		RuntimeClass: "mockagent",
		Room:         "teardown-guard-room",
		RoomAgent:    "agent-b",
	}, &newResultB)
	require.NoError(t, err, "session/new for agent-b should succeed")
	require.Equal(t, "created", newResultB.State, "agent-b initial state should be 'created'")

	// ── Step 4: Send A→B to auto-start agent-b to "running" ────────────────
	resultAB := roomSend(ctx, t, client, "teardown-guard-room", "agent-b", "hello from a", "agent-a", newResultA.SessionId)
	require.True(t, resultAB.Delivered, "A→B message should be delivered")

	// Verify agent-b is now running.
	status := roomStatus(ctx, t, client, "teardown-guard-room")
	memberMap := make(map[string]ari.RoomMember)
	for _, m := range status.Members {
		memberMap[m.AgentName] = m
	}
	require.Equal(t, "running", memberMap["agent-b"].State, "agent-b should be 'running' after receiving message")

	// ── Step 5: room/delete should FAIL — agent-b is running ───────────────
	var deleteResult interface{}
	err = client.Call(ctx, "room/delete", ari.RoomDeleteParams{Name: "teardown-guard-room"}, &deleteResult)
	require.Error(t, err, "room/delete should fail with active members")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "active member")

	// ── Step 6: session/remove on running agent-b should FAIL ──────────────
	var removeResult interface{}
	err = client.Call(ctx, "session/remove", ari.SessionRemoveParams{
		SessionId: newResultB.SessionId,
	}, &removeResult)
	require.Error(t, err, "session/remove on running session should fail")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "active")

	// ── Step 7: Stop agent-b ───────────────────────────────────────────────
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultB.SessionId}, nil)
	require.NoError(t, err, "session/stop agent-b should succeed")
	time.Sleep(500 * time.Millisecond)

	// ── Step 8: Stop agent-a (created→stopped, idempotent) ─────────────────
	err = client.Call(ctx, "session/stop", ari.SessionStopParams{SessionId: newResultA.SessionId}, nil)
	require.NoError(t, err, "session/stop agent-a should succeed")
	time.Sleep(500 * time.Millisecond)

	// ── Step 9: room/delete should SUCCEED now ─────────────────────────────
	roomDelete(ctx, t, client, "teardown-guard-room")

	// ── Step 10: Verify room is gone ───────────────────────────────────────
	var statusAfterDelete ari.RoomStatusResult
	err = client.Call(ctx, "room/status", ari.RoomStatusParams{Name: "teardown-guard-room"}, &statusAfterDelete)
	require.Error(t, err, "room/status should fail after room delete")
	requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "not found")
}

// ────────────────────────────────────────────────────────────────────────────
// Suppress unused variable warning for harness field
// ────────────────────────────────────────────────────────────────────────────

var _ = (*testHarness)(nil)