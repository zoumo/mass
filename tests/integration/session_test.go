// Package integration_test provides integration tests for session lifecycle management.
// These tests verify session state transitions and error handling.
package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// testSocketCounter provides unique socket paths for each test
var testSocketCounter int64

// TestSessionLifecycle tests all session state transitions.
// Covers: created → running → stopped → deleted
func TestSessionLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Step 1: Prepare workspace
	workspaceId := prepareTestWorkspace(t, ctx, client)

	// Step 2: session/new → state=created
	t.Log("Step: session/new → state=created")
	sessionNewParams := map[string]interface{}{
		"workspaceId":  workspaceId,
		"runtimeClass": "mockagent",
	}
	var sessionNewResult ari.SessionNewResult
	if err := client.Call("session/new", sessionNewParams, &sessionNewResult); err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	sessionId := sessionNewResult.SessionId
	t.Logf("session created: id=%s state=%s", sessionId, sessionNewResult.State)

	if sessionNewResult.State != "created" {
		t.Errorf("expected state=created, got %s", sessionNewResult.State)
	}

	// Step 3: session/prompt → auto-start → state=running
	t.Log("Step: session/prompt → auto-start → state=running")
	promptParams := map[string]interface{}{
		"sessionId": sessionId,
		"text":      "test prompt",
	}
	var promptResult ari.SessionPromptResult
	if err := client.Call("session/prompt", promptParams, &promptResult); err != nil {
		t.Fatalf("session/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)

	// Verify state is running
	statusParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var statusResult ari.SessionStatusResult
	if err := client.Call("session/status", statusParams, &statusResult); err != nil {
		t.Fatalf("session/status failed: %v", err)
	}
	if statusResult.Session.State != "running" {
		t.Errorf("expected state=running after prompt, got %s", statusResult.Session.State)
	}

	// Step 4: session/status → returns shim state
	t.Log("Step: session/status → returns shim state")
	if statusResult.ShimState == nil {
		t.Error("expected shimState to be populated for running session")
	} else {
		t.Logf("shim state: status=%s pid=%d", statusResult.ShimState.Status, statusResult.ShimState.PID)
	}

	// Step 5: session/stop → state=stopped
	t.Log("Step: session/stop → state=stopped")
	stopParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var stopResult interface{}
	if err := client.Call("session/stop", stopParams, &stopResult); err != nil {
		t.Fatalf("session/stop failed: %v", err)
	}

	// Verify state is stopped
	if err := client.Call("session/status", statusParams, &statusResult); err != nil {
		t.Fatalf("session/status failed after stop: %v", err)
	}
	if statusResult.Session.State != "stopped" {
		t.Errorf("expected state=stopped, got %s", statusResult.Session.State)
	}
	t.Log("session stopped")

	// Step 6: session/remove → session deleted
	t.Log("Step: session/remove → session deleted")
	removeParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var removeResult interface{}
	if err := client.Call("session/remove", removeParams, &removeResult); err != nil {
		t.Fatalf("session/remove failed: %v", err)
	}

	// Verify session is gone (status should return error)
	var verifyStatus ari.SessionStatusResult
	err := client.Call("session/status", statusParams, &verifyStatus)
	if err == nil {
		t.Error("expected error when getting status of removed session")
	}
	t.Logf("session removed (status check returned expected error: %v)", err)

	// Cleanup workspace
	cleanupTestWorkspace(t, client, workspaceId)
}

// TestSessionPromptStoppedSession tests that prompting a stopped session returns an error.
func TestSessionPromptStoppedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Prepare workspace and create session
	workspaceId := prepareTestWorkspace(t, ctx, client)
	sessionId := createTestSession(t, client, workspaceId)

	// Prompt once to start and run
	promptParams := map[string]interface{}{
		"sessionId": sessionId,
		"text":      "first prompt",
	}
	var promptResult ari.SessionPromptResult
	if err := client.Call("session/prompt", promptParams, &promptResult); err != nil {
		t.Fatalf("first prompt failed: %v", err)
	}

	// Stop session
	stopParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var stopResult interface{}
	if err := client.Call("session/stop", stopParams, &stopResult); err != nil {
		t.Fatalf("session/stop failed: %v", err)
	}

	// Try to prompt stopped session - should return error
	t.Log("Attempting to prompt stopped session (expecting error)")
	promptParams["text"] = "prompt on stopped session"
	err := client.Call("session/prompt", promptParams, &promptResult)
	if err == nil {
		t.Error("expected error when prompting stopped session, but got nil")
	} else {
		t.Logf("got expected error: %v", err)
		// The error should be an InvalidParams type error
		if !containsString(err.Error(), "InvalidParams") && !containsString(err.Error(), "invalid") {
			t.Logf("warning: error may not be InvalidParams type: %v", err)
		}
	}

	// Cleanup
	removeParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	client.Call("session/remove", removeParams, nil) // ignore error
	cleanupTestWorkspace(t, client, workspaceId)
}

// TestSessionRemoveRunningSession tests that removing a running session returns an error.
func TestSessionRemoveRunningSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Prepare workspace and create session
	workspaceId := prepareTestWorkspace(t, ctx, client)
	sessionId := createTestSession(t, client, workspaceId)

	// Prompt to start (state=running)
	promptParams := map[string]interface{}{
		"sessionId": sessionId,
		"text":      "running prompt",
	}
	var promptResult ari.SessionPromptResult
	if err := client.Call("session/prompt", promptParams, &promptResult); err != nil {
		t.Fatalf("prompt failed: %v", err)
	}

	// Try to remove running session - should return error (protected)
	t.Log("Attempting to remove running session (expecting error)")
	removeParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var removeResult interface{}
	err := client.Call("session/remove", removeParams, &removeResult)
	if err == nil {
		t.Error("expected error when removing running session, but got nil")
	} else {
		t.Logf("got expected error: %v", err)
		// The error should be an InvalidParams type error
		if !containsString(err.Error(), "InvalidParams") && !containsString(err.Error(), "invalid") {
			t.Logf("warning: error may not be InvalidParams type: %v", err)
		}
	}

	// Cleanup: stop then remove
	client.Call("session/stop", map[string]interface{}{"sessionId": sessionId}, nil)
	client.Call("session/remove", removeParams, nil)
	cleanupTestWorkspace(t, client, workspaceId)
}

// TestSessionList tests listing multiple sessions.
func TestSessionList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Prepare workspace
	workspaceId := prepareTestWorkspace(t, ctx, client)

	// Create 3 sessions
	sessionIds := make([]string, 3)
	for i := 0; i < 3; i++ {
		sessionId := createTestSession(t, client, workspaceId)
		sessionIds[i] = sessionId
		t.Logf("created session %d: %s", i+1, sessionId)
	}

	// List sessions → verify count=3
	t.Log("Step: session/list → verify count=3")
	listParams := map[string]interface{}{}
	var listResult ari.SessionListResult
	if err := client.Call("session/list", listParams, &listResult); err != nil {
		t.Fatalf("session/list failed: %v", err)
	}
	t.Logf("session/list returned %d sessions", len(listResult.Sessions))

	if len(listResult.Sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(listResult.Sessions))
	}

	// Remove all sessions
	for i, sessionId := range sessionIds {
		removeParams := map[string]interface{}{
			"sessionId": sessionId,
		}
		var removeResult interface{}
		if err := client.Call("session/remove", removeParams, &removeResult); err != nil {
			t.Logf("warning: session/remove failed for session %d: %v", i+1, err)
		}
	}

	// List again → verify count=0
	t.Log("Step: session/list → verify count=0")
	if err := client.Call("session/list", listParams, &listResult); err != nil {
		t.Fatalf("session/list failed: %v", err)
	}
	t.Logf("session/list returned %d sessions after removal", len(listResult.Sessions))

	if len(listResult.Sessions) != 0 {
		t.Errorf("expected 0 sessions after removal, got %d", len(listResult.Sessions))
	}

	// Cleanup workspace
	cleanupTestWorkspace(t, client, workspaceId)
}

// =============================================================================
// Helper Functions
// =============================================================================

// setupAgentdTest starts agentd daemon and returns context, client, and cleanup function.
func setupAgentdTest(t *testing.T) (context.Context, context.CancelFunc, *ari.Client, func()) {
	// Create temp directories
	tmpDir := t.TempDir()
	// Use short socket path in /tmp to avoid macOS 104-char Unix socket path limit
	// Generate unique socket path using PID and test counter
	testSocketCounter++
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), testSocketCounter)
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDB := filepath.Join(tmpDir, "agentd.db")
	bundleRoot := filepath.Join(tmpDir, "bundles")

	// Ensure socket file doesn't exist (clean up any leftover from previous run)
	os.Remove(socketPath)

	// Create directories
	if err := os.MkdirAll(workspaceRoot, 0755); err != nil {
		t.Fatalf("failed to create workspace root: %v", err)
	}
	if err := os.MkdirAll(bundleRoot, 0755); err != nil {
		t.Fatalf("failed to create bundle root: %v", err)
	}

	// Get absolute paths to binaries
	agentdBin, err := filepath.Abs("../../bin/agentd")
	if err != nil {
		t.Fatalf("failed to get agentd path: %v", err)
	}
	agentShimBin, err := filepath.Abs("../../bin/agent-shim")
	if err != nil {
		t.Fatalf("failed to get agent-shim path: %v", err)
	}
	mockagentBin, err := filepath.Abs("../../bin/mockagent")
	if err != nil {
		t.Fatalf("failed to get mockagent path: %v", err)
	}

	// Verify binaries exist
	for _, bin := range []string{agentdBin, agentShimBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s", bin)
		}
	}

	// Create config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := fmt.Sprintf(`
socket: %s
workspaceRoot: %s
metaDB: %s
bundleRoot: %s
runtimeClasses:
  mockagent:
    command: %s
    args: []
    env:
      PATH: /usr/bin:/bin
`, socketPath, workspaceRoot, metaDB, bundleRoot, mockagentBin)

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Start agentd daemon
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	agentdCmd := exec.CommandContext(ctx, agentdBin, "--config", configPath)
	agentdCmd.Stdout = os.Stdout
	agentdCmd.Stderr = os.Stderr
	agentdCmd.Env = append(os.Environ(), fmt.Sprintf("OAR_SHIM_BINARY=%s", agentShimBin))

	if err := agentdCmd.Start(); err != nil {
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", agentdCmd.Process.Pid)

	// Wait for socket to be ready
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}

	// Create ARI client
	client, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("failed to create ARI client: %v", err)
	}

	// Cleanup function
	cleanup := func() {
		client.Close()
		if agentdCmd.Process != nil {
			agentdCmd.Process.Signal(os.Interrupt)
			agentdCmd.Wait()
			t.Log("agentd stopped")
		}
		// Clean up socket file
		os.Remove(socketPath)
		// Kill any leftover shim/mockagent processes
		exec.Command("pkill", "-f", "agent-shim").Run()
		exec.Command("pkill", "-f", "mockagent").Run()
	}

	return ctx, cancel, client, cleanup
}

// prepareTestWorkspace creates a test workspace and returns its ID.
func prepareTestWorkspace(t *testing.T, ctx context.Context, client *ari.Client) string {
	prepareParams := map[string]interface{}{
		"spec": map[string]interface{}{
			"oarVersion": "0.1.0",
			"metadata": map[string]interface{}{
				"name": "test-workspace",
			},
			"source": map[string]interface{}{
				"type": "emptyDir",
			},
		},
	}
	var prepareResult ari.WorkspacePrepareResult
	if err := client.Call("workspace/prepare", prepareParams, &prepareResult); err != nil {
		t.Fatalf("workspace/prepare failed: %v", err)
	}
	t.Logf("workspace prepared: id=%s", prepareResult.WorkspaceId)
	return prepareResult.WorkspaceId
}

// createTestSession creates a test session and returns its ID.
func createTestSession(t *testing.T, client *ari.Client, workspaceId string) string {
	sessionNewParams := map[string]interface{}{
		"workspaceId":  workspaceId,
		"runtimeClass": "mockagent",
	}
	var sessionNewResult ari.SessionNewResult
	if err := client.Call("session/new", sessionNewParams, &sessionNewResult); err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	return sessionNewResult.SessionId
}

// cleanupTestWorkspace removes a test workspace.
func cleanupTestWorkspace(t *testing.T, client *ari.Client, workspaceId string) {
	cleanupParams := map[string]interface{}{
		"workspaceId": workspaceId,
	}
	var cleanupResult interface{}
	if err := client.Call("workspace/cleanup", cleanupParams, &cleanupResult); err != nil {
		t.Logf("warning: workspace/cleanup failed: %v", err)
	}
}

// containsString checks if a string contains a substring (case-insensitive).
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) > 0 &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
		containsString(s[1:], substr)))
}