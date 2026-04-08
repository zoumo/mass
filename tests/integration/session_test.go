// Package integration_test provides integration tests for agent lifecycle management.
// These tests verify agent state transitions and error handling using the agent/* ARI surface.
package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// testSocketCounter provides unique socket paths for each test.
var testSocketCounter int64

// =============================================================================
// Shared Helpers
// =============================================================================

// setupAgentdTest starts agentd daemon and returns context, client, and cleanup function.
func setupAgentdTest(t *testing.T) (context.Context, context.CancelFunc, *ari.Client, func()) {
	t.Helper()
	// Create temp directories
	tmpDir := t.TempDir()
	// Use short socket path in /tmp to avoid macOS 104-char Unix socket path limit
	// Generate unique socket path using PID and test counter
	counter := atomic.AddInt64(&testSocketCounter, 1)
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), counter)
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
		cancel()
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", agentdCmd.Process.Pid)

	// Wait for socket to be ready
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("socket not ready: %v", err)
	}

	// Create ARI client
	client, err := ari.NewClient(socketPath)
	if err != nil {
		cancel()
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
	t.Helper()
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

// createRoom creates a named room and returns the result. Callers should defer
// room/delete for cleanup.
func createRoom(t *testing.T, client *ari.Client, roomName string) ari.RoomCreateResult {
	t.Helper()
	var result ari.RoomCreateResult
	if err := client.Call("room/create", map[string]interface{}{"name": roomName}, &result); err != nil {
		t.Fatalf("room/create (name=%s): %v", roomName, err)
	}
	t.Logf("room created: name=%s", result.Name)
	return result
}

// deleteRoom removes a room. Logs but does not fail on error (best-effort cleanup).
func deleteRoom(t *testing.T, client *ari.Client, roomName string) {
	t.Helper()
	if err := client.Call("room/delete", map[string]interface{}{"name": roomName}, nil); err != nil {
		t.Logf("room/delete (name=%s): %v (ignored)", roomName, err)
	}
}

// cleanupTestWorkspace removes a test workspace.
func cleanupTestWorkspace(t *testing.T, client *ari.Client, workspaceId string) {
	t.Helper()
	cleanupParams := map[string]interface{}{
		"workspaceId": workspaceId,
	}
	var cleanupResult interface{}
	if err := client.Call("workspace/cleanup", cleanupParams, &cleanupResult); err != nil {
		t.Logf("warning: workspace/cleanup failed: %v", err)
	}
}

// waitForAgentState polls agent/status every 200ms until the agent reaches
// the desired state or the timeout expires. Returns the final status result.
// Calls t.Fatalf on timeout.
func waitForAgentState(t *testing.T, client *ari.Client, agentId, wantState string, timeout time.Duration) ari.AgentStatusResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	params := map[string]interface{}{"agentId": agentId}
	var result ari.AgentStatusResult
	for time.Now().Before(deadline) {
		if err := client.Call("agent/status", params, &result); err != nil {
			t.Logf("agent/status for %s: %v (retrying)", agentId, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if result.Agent.State == wantState {
			return result
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("agent %s did not reach state %q within %v (last state: %q)",
		agentId, wantState, timeout, result.Agent.State)
	return result // unreachable
}

// createAgentAndWait calls agent/create and then waits for the agent to reach
// state="created". Returns the status result after the agent is ready.
func createAgentAndWait(t *testing.T, client *ari.Client, workspaceId, room, name string) ari.AgentStatusResult {
	t.Helper()
	var createResult ari.AgentCreateResult
	if err := client.Call("agent/create", map[string]interface{}{
		"workspaceId":  workspaceId,
		"room":         room,
		"name":         name,
		"runtimeClass": "mockagent",
	}, &createResult); err != nil {
		t.Fatalf("agent/create (name=%s): %v", name, err)
	}
	t.Logf("agent created: id=%s state=%s", createResult.AgentId, createResult.State)
	return waitForAgentState(t, client, createResult.AgentId, "created", 15*time.Second)
}

// stopAndDeleteAgent stops (if needed) and then deletes an agent. Best-effort cleanup.
func stopAndDeleteAgent(t *testing.T, client *ari.Client, agentId string) {
	t.Helper()
	if err := client.Call("agent/stop", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Logf("agent/stop (%s): %v (ignored)", agentId, err)
	}
	if err := client.Call("agent/delete", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Logf("agent/delete (%s): %v (ignored)", agentId, err)
	}
}

// =============================================================================
// Tests
// =============================================================================

// TestAgentLifecycle tests all agent state transitions.
// Covers: agent/create → state=created → agent/prompt → state=running → agent/stop → state=stopped → agent/delete
func TestAgentLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Prepare workspace and room
	workspaceId := prepareTestWorkspace(t, ctx, client)
	createRoom(t, client, "lifecycle-room")
	defer deleteRoom(t, client, "lifecycle-room")
	defer cleanupTestWorkspace(t, client, workspaceId)

	// Step 1: agent/create → state=created
	t.Log("Step 1: agent/create → wait for state=created")
	status := createAgentAndWait(t, client, workspaceId, "lifecycle-room", "agent-lifecycle")
	agentId := status.Agent.AgentId
	t.Logf("agent created: id=%s state=%s", agentId, status.Agent.State)

	if status.Agent.State != "created" {
		t.Errorf("expected state=created, got %s", status.Agent.State)
	}

	// Step 2: agent/prompt → state transitions to running
	t.Log("Step 2: agent/prompt")
	var promptResult ari.AgentPromptResult
	if err := client.Call("agent/prompt", map[string]interface{}{
		"agentId": agentId,
		"prompt":  "test lifecycle prompt",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)

	if promptResult.StopReason != "end_turn" {
		t.Errorf("expected stopReason=end_turn, got %s", promptResult.StopReason)
	}

	// After end_turn, agent state is running
	t.Log("Step 3: verify agent is running after prompt")
	_ = waitForAgentState(t, client, agentId, "running", 10*time.Second)

	// Step 4: agent/stop → state=stopped
	t.Log("Step 4: agent/stop → state=stopped")
	if err := client.Call("agent/stop", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, agentId, "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Step 5: agent/delete
	t.Log("Step 5: agent/delete")
	if err := client.Call("agent/delete", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Fatalf("agent/delete failed: %v", err)
	}

	// Verify agent is gone (status should return error)
	var verifyStatus ari.AgentStatusResult
	err := client.Call("agent/status", map[string]interface{}{"agentId": agentId}, &verifyStatus)
	if err == nil {
		t.Error("expected error when getting status of deleted agent")
	}
	t.Logf("agent deleted (status check returned expected error: %v)", err)
}

// TestAgentPromptAndStop tests agent/prompt followed by agent/stop.
func TestAgentPromptAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	workspaceId := prepareTestWorkspace(t, ctx, client)
	createRoom(t, client, "prompt-stop-room")
	defer deleteRoom(t, client, "prompt-stop-room")
	defer cleanupTestWorkspace(t, client, workspaceId)

	// Create agent and wait for created state
	status := createAgentAndWait(t, client, workspaceId, "prompt-stop-room", "agent-ps")
	agentId := status.Agent.AgentId

	// Prompt the agent
	var promptResult ari.AgentPromptResult
	if err := client.Call("agent/prompt", map[string]interface{}{
		"agentId": agentId,
		"prompt":  "prompt and stop test",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)
	if promptResult.StopReason != "end_turn" {
		t.Errorf("expected stopReason=end_turn, got %s", promptResult.StopReason)
	}

	// Stop the agent
	if err := client.Call("agent/stop", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, agentId, "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Delete agent
	if err := client.Call("agent/delete", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Logf("agent/delete: %v (ignored)", err)
	}
}

// TestAgentPromptFromCreated tests that prompting a newly-created agent auto-starts
// the shim and returns a valid response.
func TestAgentPromptFromCreated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	workspaceId := prepareTestWorkspace(t, ctx, client)
	createRoom(t, client, "autostart-room")
	defer deleteRoom(t, client, "autostart-room")
	defer cleanupTestWorkspace(t, client, workspaceId)

	// Create agent — should be in state=created
	status := createAgentAndWait(t, client, workspaceId, "autostart-room", "agent-auto")
	agentId := status.Agent.AgentId

	if status.Agent.State != "created" {
		t.Errorf("expected state=created before first prompt, got %s", status.Agent.State)
	}

	// Prompt immediately from created state (auto-start)
	var promptResult ari.AgentPromptResult
	if err := client.Call("agent/prompt", map[string]interface{}{
		"agentId": agentId,
		"prompt":  "auto-start prompt",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt (from created) failed: %v", err)
	}
	t.Logf("auto-start prompt completed: stopReason=%s", promptResult.StopReason)

	if promptResult.StopReason != "end_turn" {
		t.Errorf("expected stopReason=end_turn, got %s", promptResult.StopReason)
	}

	// Agent should now be in running state
	_ = waitForAgentState(t, client, agentId, "running", 10*time.Second)
	t.Log("agent auto-started and responded ✓")

	// Cleanup
	stopAndDeleteAgent(t, client, agentId)
}

// TestMultipleAgentPromptsSequential tests multiple sequential prompts to the same agent.
func TestMultipleAgentPromptsSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	workspaceId := prepareTestWorkspace(t, ctx, client)
	createRoom(t, client, "sequential-room")
	defer deleteRoom(t, client, "sequential-room")
	defer cleanupTestWorkspace(t, client, workspaceId)

	// Create agent
	status := createAgentAndWait(t, client, workspaceId, "sequential-room", "agent-seq")
	agentId := status.Agent.AgentId

	prompts := []string{
		"first sequential prompt",
		"second sequential prompt",
		"third sequential prompt",
	}

	for i, promptText := range prompts {
		t.Logf("Sending prompt %d/%d: %q", i+1, len(prompts), promptText)
		var promptResult ari.AgentPromptResult
		if err := client.Call("agent/prompt", map[string]interface{}{
			"agentId": agentId,
			"prompt":  promptText,
		}, &promptResult); err != nil {
			t.Fatalf("agent/prompt %d failed: %v", i+1, err)
		}
		t.Logf("prompt %d completed: stopReason=%s", i+1, promptResult.StopReason)

		if promptResult.StopReason != "end_turn" {
			t.Errorf("prompt %d: expected stopReason=end_turn, got %s", i+1, promptResult.StopReason)
		}
	}

	t.Logf("All %d sequential prompts completed successfully ✓", len(prompts))

	// Cleanup
	stopAndDeleteAgent(t, client, agentId)
}
