// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file contains integration tests for the ProcessManager.
package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// TestProcessManagerStart tests the full Start workflow:
// get Session → resolve RuntimeClass → generate config.json → create bundle
// → fork agent-shim → wait for socket → connect ShimClient → subscribe events
// → transition session state to "running".
func TestProcessManagerStart(t *testing.T) {
	// Build and find binaries.
	shimBinary := findShimBinary(t)
	mockagentBinary := findMockagentBinary(t)

	// Set OAR_SHIM_BINARY env var so ProcessManager finds the shim binary.
	t.Setenv("OAR_SHIM_BINARY", shimBinary)

	// Setup test environment.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create temp directories.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	bundleRoot := filepath.Join(tmpDir, "bundles")
	stateDirRoot := filepath.Join(tmpDir, "shim-state")

	// Create directories.
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspaceRoot: %v", err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatalf("mkdir bundleRoot: %v", err)
	}
	if err := os.MkdirAll(stateDirRoot, 0o755); err != nil {
		t.Fatalf("mkdir stateDirRoot: %v", err)
	}

	// Create meta store.
	store, err := meta.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Create SessionManager.
	sessionMgr := NewSessionManager(store)

	// Create RuntimeClassRegistry with mockagent.
	runtimeClasses := map[string]RuntimeClassConfig{
		"mockagent": {
			Command: mockagentBinary,
			Args:    []string{},
			Env:     map[string]string{},
			Capabilities: CapabilitiesConfig{
				Streaming:          true,
				SessionLoad:        false,
				ConcurrentSessions: 1,
			},
		},
	}
	registry, err := NewRuntimeClassRegistry(runtimeClasses)
	if err != nil {
		t.Fatalf("NewRuntimeClassRegistry: %v", err)
	}

	// Create ProcessManager config.
	cfg := Config{
		Socket:        filepath.Join(tmpDir, "agentd.sock"), // Use temp dir to indicate test mode
		WorkspaceRoot: bundleRoot,
		MetaDB:        dbPath,
		Runtime: RuntimeConfig{
			DefaultClass: "mockagent",
		},
		SessionPolicy: SessionPolicyConfig{
			MaxSessions: 10,
		},
	}

	// Create ProcessManager.
	agentMgr := NewAgentManager(store)
	procMgr := NewProcessManager(registry, sessionMgr, agentMgr, store, cfg)

	// Create a workspace.
	workspaceID := uuid.New().String()
	workspacePath := filepath.Join(workspaceRoot, "test-workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspacePath: %v", err)
	}
	workspace := &meta.Workspace{
		ID:     workspaceID,
		Name:   "test-workspace",
		Path:   workspacePath,
		Status: meta.WorkspaceStatusActive,
	}
	if err := store.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Create a session in "created" state.
	sessionID := uuid.New().String()
	session := &meta.Session{
		ID:           sessionID,
		RuntimeClass: "mockagent",
		WorkspaceID:  workspaceID,
		State:        meta.SessionStateCreated,
	}
	if err := sessionMgr.Create(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Call ProcessManager.Start.
	shimProc, err := procMgr.Start(ctx, sessionID)
	if err != nil {
		// Print state directory content for debugging.
		stateDirPath := filepath.Join(os.TempDir(), "agentd-shim", sessionID)
		t.Logf("State directory %s contents:", stateDirPath)
		if entries, err := os.ReadDir(stateDirPath); err == nil {
			for _, entry := range entries {
				t.Logf("  %s", entry.Name())
				if entry.Name() == "state.json" {
					if data, err := os.ReadFile(filepath.Join(stateDirPath, "state.json")); err == nil {
						t.Logf("state.json content:\n%s", string(data))
					}
				}
			}
		} else {
			t.Logf("Could not read state directory: %v", err)
		}

		// Print bundle directory content for debugging.
		bundlePath := filepath.Join(bundleRoot, sessionID)
		t.Logf("Bundle directory %s contents:", bundlePath)
		if entries, err := os.ReadDir(bundlePath); err == nil {
			for _, entry := range entries {
				t.Logf("  %s", entry.Name())
				if entry.Name() == "config.json" {
					if data, err := os.ReadFile(filepath.Join(bundlePath, "config.json")); err == nil {
						t.Logf("config.json content:\n%s", string(data))
					}
				}
			}
		}
		t.Fatalf("ProcessManager.Start: %v", err)
	}

	// Verify session state is "running".
	updatedSession, err := sessionMgr.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession after Start: %v", err)
	}
	if updatedSession.State != meta.SessionStateRunning {
		t.Errorf("expected session state 'running', got '%s'", updatedSession.State)
	}

	// Verify PID > 0.
	if shimProc.PID <= 0 {
		t.Errorf("expected PID > 0, got PID=%d", shimProc.PID)
	}

	// Verify ShimClient is connected.
	if shimProc.Client == nil {
		t.Fatal("expected ShimClient to be connected, got nil")
	}

	// Verify shim state via runtime/status RPC.
	statusResult, err := shimProc.Client.Status(ctx)
	if err != nil {
		t.Fatalf("runtime/status RPC: %v", err)
	}
	state := statusResult.State
	t.Logf("Shim state: ID=%s, Status=%s, PID=%d, Bundle=%s, recovery.lastSeq=%d",
		state.ID, state.Status, state.PID, state.Bundle, statusResult.Recovery.LastSeq)

	if state.Status != spec.StatusCreated && state.Status != spec.StatusRunning {
		t.Errorf("expected shim status 'created' or 'running', got '%s'", state.Status)
	}

	// Send a Prompt to trigger events (session/prompt).
	promptResult, err := shimProc.Client.Prompt(ctx, "hello mockagent")
	if err != nil {
		t.Fatalf("Prompt RPC: %v", err)
	}
	t.Logf("Prompt result: stopReason=%s", promptResult.StopReason)

	// Verify events were received.
	// Mockagent sends text events after a prompt.
	eventCount := 0
	timeout := time.After(5 * time.Second)

	// Collect events with timeout.
	for {
		select {
		case ev, ok := <-shimProc.Events:
			if !ok {
				// Channel closed, shim process exited.
				t.Log("Events channel closed")
				goto done
			}
			eventCount++
			t.Logf("Received event #%d: %T", eventCount, ev)
			// Look for text events from mockagent.
			if _, ok := ev.(events.TextEvent); ok {
				t.Logf("TextEvent received")
			}
			// Check for turn_end event to know the prompt is done.
			if _, ok := ev.(events.TurnEndEvent); ok {
				t.Logf("TurnEndEvent received, prompt complete")
				goto done
			}
		case <-timeout:
			if eventCount == 0 {
				t.Error("expected to receive events, got none within timeout")
			}
			goto done
		}
	}
done:

	if eventCount == 0 {
		t.Error("expected to receive at least one event, got none")
	} else {
		t.Logf("Received %d events total", eventCount)
	}

	// Clean up: stop the shim (runtime/stop).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	if err := shimProc.Client.Stop(stopCtx); err != nil {
		t.Logf("runtime/stop RPC (non-fatal): %v", err)
	}

	// Wait for shim process to exit.
	select {
	case <-shimProc.Done:
		t.Log("Shim process exited gracefully")
	case <-time.After(3 * time.Second):
		t.Log("Timeout waiting for shim exit, killing process")
		_ = shimProc.Cmd.Process.Kill()
	}

	// Verify session state transitioned to "stopped".
	finalSession, err := sessionMgr.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession after shutdown: %v", err)
	}
	if finalSession.State != meta.SessionStateStopped {
		t.Errorf("expected session state 'stopped' after shutdown, got '%s'", finalSession.State)
	}

	// Verify bundle directory was cleaned up.
	if _, err := os.Stat(shimProc.BundlePath); err == nil {
		t.Errorf("expected bundle directory to be cleaned up, but %s still exists", shimProc.BundlePath)
	}

	t.Logf("Test complete: session %s lifecycle: created → running → stopped", sessionID)
}

// ── generateConfig ────────────────────────────────────────────────────────────

func TestGenerateConfigWithRoomMCPInjection(t *testing.T) {
	// Minimal ProcessManager with socket config set.
	pm := &ProcessManager{
		config: Config{
			Socket: "/tmp/test-agentd.sock",
		},
	}
	rc := &RuntimeClass{
		Name:    "mockagent",
		Command: "/usr/bin/mockagent",
		Args:    []string{},
		Env:     map[string]string{},
	}

	t.Run("session with Room injects room MCP server", func(t *testing.T) {
		session := &meta.Session{
			ID:        "sess-123",
			AgentID:   "sess-123",
			Room:      "design-room",
			RoomAgent: "agent-alice",
		}

		cfg := pm.generateConfig(session, rc)

		servers := cfg.AcpAgent.Session.McpServers
		if len(servers) != 1 {
			t.Fatalf("expected 1 MCP server, got %d", len(servers))
		}
		srv := servers[0]
		if srv.Type != "stdio" {
			t.Errorf("expected Type=stdio, got %q", srv.Type)
		}
		if srv.Name != "room-tools" {
			t.Errorf("expected Name=room-tools, got %q", srv.Name)
		}
		// Command is resolved via resolveRoomMCPBinary — in test env it falls
		// back to "room-mcp-server" (PATH lookup) since no binary exists.
		if srv.Command == "" {
			t.Error("expected non-empty Command")
		}

		// Verify env vars.
		envMap := make(map[string]string)
		for _, e := range srv.Env {
			envMap[e.Name] = e.Value
		}
		if envMap["OAR_AGENTD_SOCKET"] != "/tmp/test-agentd.sock" {
			t.Errorf("OAR_AGENTD_SOCKET = %q, want /tmp/test-agentd.sock", envMap["OAR_AGENTD_SOCKET"])
		}
		if envMap["OAR_ROOM_NAME"] != "design-room" {
			t.Errorf("OAR_ROOM_NAME = %q, want design-room", envMap["OAR_ROOM_NAME"])
		}
		// Deprecated vars must be absent.
		if _, exists := envMap["OAR_SESSION_ID"]; exists {
			t.Errorf("OAR_SESSION_ID should not be present (deprecated)")
		}
		if _, exists := envMap["OAR_ROOM_AGENT"]; exists {
			t.Errorf("OAR_ROOM_AGENT should not be present (deprecated)")
		}
		// New canonical vars must be present.
		if envMap["OAR_AGENT_ID"] != "sess-123" {
			t.Errorf("OAR_AGENT_ID = %q, want sess-123", envMap["OAR_AGENT_ID"])
		}
		if envMap["OAR_AGENT_NAME"] != "agent-alice" {
			t.Errorf("OAR_AGENT_NAME = %q, want agent-alice", envMap["OAR_AGENT_NAME"])
		}
	})

	t.Run("session without Room has empty McpServers", func(t *testing.T) {
		session := &meta.Session{
			ID: "sess-456",
		}

		cfg := pm.generateConfig(session, rc)

		if len(cfg.AcpAgent.Session.McpServers) != 0 {
			t.Errorf("expected 0 MCP servers for non-room session, got %d", len(cfg.AcpAgent.Session.McpServers))
		}
	})

	t.Run("session with Room but empty RoomAgent still injects", func(t *testing.T) {
		session := &meta.Session{
			ID:        "sess-789",
			Room:      "chat-room",
			RoomAgent: "", // empty is valid
		}

		cfg := pm.generateConfig(session, rc)

		servers := cfg.AcpAgent.Session.McpServers
		if len(servers) != 1 {
			t.Fatalf("expected 1 MCP server, got %d", len(servers))
		}
		// Verify OAR_AGENT_NAME is present with empty value; deprecated OAR_ROOM_AGENT absent.
		envMap := make(map[string]string)
		for _, e := range servers[0].Env {
			envMap[e.Name] = e.Value
		}
		if _, exists := envMap["OAR_ROOM_AGENT"]; exists {
			t.Error("OAR_ROOM_AGENT should not be present (deprecated)")
		}
		if _, exists := envMap["OAR_AGENT_NAME"]; !exists {
			t.Error("OAR_AGENT_NAME env var should exist even when RoomAgent is empty")
		}
		if envMap["OAR_AGENT_NAME"] != "" {
			t.Errorf("OAR_AGENT_NAME = %q, want empty string", envMap["OAR_AGENT_NAME"])
		}
	})
}

// findShimBinary finds the agent-shim binary for testing.
// Returns the path or skips the test if not found.
func findShimBinary(t *testing.T) string {
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
