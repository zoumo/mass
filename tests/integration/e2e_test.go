// Package integration_test provides end-to-end integration tests for the agentd daemon.
// These tests verify the complete pipeline: agentd → agent-shim → mockagent.
package integration_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// TestEndToEndPipeline tests the complete agentd → agent-shim → mockagent lifecycle
// using the agent/* ARI surface.
// Pipeline: workspace/prepare → room/create → agent/create → agent/prompt → agent/stop → agent/delete → room/delete → workspace/cleanup
func TestEndToEndPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create temp directories
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "agentd.sock")
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDB := filepath.Join(tmpDir, "agentd.db")
	bundleRoot := filepath.Join(tmpDir, "bundles")

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
	defer cancel()

	agentdCmd := exec.CommandContext(ctx, agentdBin, "--config", configPath)
	agentdCmd.Stdout = os.Stdout
	agentdCmd.Stderr = os.Stderr
	// Set OAR_SHIM_BINARY so the ProcessManager can find the shim binary
	agentdCmd.Env = append(os.Environ(), fmt.Sprintf("OAR_SHIM_BINARY=%s", agentShimBin))

	if err := agentdCmd.Start(); err != nil {
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", agentdCmd.Process.Pid)

	// Ensure cleanup
	defer func() {
		if agentdCmd.Process != nil {
			agentdCmd.Process.Signal(os.Interrupt)
			agentdCmd.Wait()
			t.Log("agentd stopped")
		}
	}()

	// Wait for socket to be ready
	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}
	t.Logf("socket ready at %s", socketPath)

	// Create ARI client
	client, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("failed to create ARI client: %v", err)
	}
	defer client.Close()

	// Step 1: workspace/prepare
	t.Log("Step 1: workspace/prepare")
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
	t.Logf("workspace prepared: id=%s path=%s", prepareResult.WorkspaceId, prepareResult.Path)

	// Step 2: room/create
	t.Log("Step 2: room/create")
	var roomResult ari.RoomCreateResult
	if err := client.Call("room/create", map[string]interface{}{"name": "e2e-room"}, &roomResult); err != nil {
		t.Fatalf("room/create failed: %v", err)
	}
	t.Logf("room created: name=%s", roomResult.Name)

	// Step 3: agent/create → wait for state=created
	t.Log("Step 3: agent/create → wait for state=created")
	var agentCreateResult ari.AgentCreateResult
	if err := client.Call("agent/create", map[string]interface{}{
		"workspaceId":  prepareResult.WorkspaceId,
		"room":         "e2e-room",
		"name":         "e2e-agent",
		"runtimeClass": "mockagent",
	}, &agentCreateResult); err != nil {
		t.Fatalf("agent/create failed: %v", err)
	}
	agentId := agentCreateResult.AgentId
	t.Logf("agent created: id=%s state=%s", agentId, agentCreateResult.State)

	// Wait for agent to reach state=created
	agentStatus := waitForAgentState(t, client, agentId, "created", 15*time.Second)
	t.Logf("agent state=created confirmed ✓ (state=%s)", agentStatus.Agent.State)

	// Step 4: agent/prompt (auto-starts shim)
	t.Log("Step 4: agent/prompt (auto-start)")
	var promptResult ari.AgentPromptResult
	if err := client.Call("agent/prompt", map[string]interface{}{
		"agentId": agentId,
		"prompt":  "hello from e2e integration test",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)

	// Verify mockagent responded
	if promptResult.StopReason != "end_turn" {
		t.Errorf("expected stopReason=end_turn, got %s", promptResult.StopReason)
	}

	// Step 5: verify agent is running
	t.Log("Step 5: verify agent state=running")
	_ = waitForAgentState(t, client, agentId, "running", 10*time.Second)
	t.Log("agent state=running ✓")

	// Step 6: agent/stop
	t.Log("Step 6: agent/stop")
	if err := client.Call("agent/stop", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, agentId, "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Step 7: agent/delete
	t.Log("Step 7: agent/delete")
	if err := client.Call("agent/delete", map[string]interface{}{"agentId": agentId}, nil); err != nil {
		t.Fatalf("agent/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	// Step 8: room/delete
	t.Log("Step 8: room/delete")
	if err := client.Call("room/delete", map[string]interface{}{"name": "e2e-room"}, nil); err != nil {
		t.Logf("room/delete: %v (ignored)", err)
	}
	t.Log("room deleted ✓")

	// Step 9: workspace/cleanup
	t.Log("Step 9: workspace/cleanup")
	cleanupParams := map[string]interface{}{
		"workspaceId": prepareResult.WorkspaceId,
	}
	var cleanupResult interface{}
	if err := client.Call("workspace/cleanup", cleanupParams, &cleanupResult); err != nil {
		t.Fatalf("workspace/cleanup failed: %v", err)
	}
	t.Log("workspace cleaned up ✓")

	t.Log("End-to-end pipeline test completed successfully! ✓")
}

// waitForSocket waits for a Unix socket to be ready.
func waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socket %s not ready after %v", socketPath, timeout)
}
