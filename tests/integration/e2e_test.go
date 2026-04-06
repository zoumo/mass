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

// TestEndToEndPipeline tests the complete agentd → agent-shim → mockagent lifecycle.
// This is the primary integration test proving all components work together.
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

	// Step 2: session/new
	t.Log("Step 2: session/new")
	sessionNewParams := map[string]interface{}{
		"workspaceId":  prepareResult.WorkspaceId,
		"runtimeClass": "mockagent",
	}
	var sessionNewResult ari.SessionNewResult
	if err := client.Call("session/new", sessionNewParams, &sessionNewResult); err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	t.Logf("session created: id=%s state=%s", sessionNewResult.SessionId, sessionNewResult.State)

	// Step 3: session/prompt (auto-starts shim)
	t.Log("Step 3: session/prompt (auto-start)")
	promptParams := map[string]interface{}{
		"sessionId": sessionNewResult.SessionId,
		"text":      "hello from integration test",
	}
	var promptResult ari.SessionPromptResult
	if err := client.Call("session/prompt", promptParams, &promptResult); err != nil {
		t.Fatalf("session/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)

	// Verify mockagent responded
	if promptResult.StopReason != "end_turn" {
		t.Errorf("expected stopReason=end_turn, got %s", promptResult.StopReason)
	}

	// Step 4: session/status (verify running)
	t.Log("Step 4: session/status")
	statusParams := map[string]interface{}{
		"sessionId": sessionNewResult.SessionId,
	}
	var statusResult ari.SessionStatusResult
	if err := client.Call("session/status", statusParams, &statusResult); err != nil {
		t.Fatalf("session/status failed: %v", err)
	}
	t.Logf("session status: state=%s", statusResult.Session.State)

	// Step 5: session/stop
	t.Log("Step 5: session/stop")
	stopParams := map[string]interface{}{
		"sessionId": sessionNewResult.SessionId,
	}
	var stopResult interface{}
	if err := client.Call("session/stop", stopParams, &stopResult); err != nil {
		t.Fatalf("session/stop failed: %v", err)
	}
	t.Log("session stopped")

	// Step 6: session/remove
	t.Log("Step 6: session/remove")
	removeParams := map[string]interface{}{
		"sessionId": sessionNewResult.SessionId,
	}
	var removeResult interface{}
	if err := client.Call("session/remove", removeParams, &removeResult); err != nil {
		t.Fatalf("session/remove failed: %v", err)
	}
	t.Log("session removed")

	// Step 7: workspace/cleanup
	t.Log("Step 7: workspace/cleanup")
	cleanupParams := map[string]interface{}{
		"workspaceId": prepareResult.WorkspaceId,
	}
	var cleanupResult interface{}
	if err := client.Call("workspace/cleanup", cleanupParams, &cleanupResult); err != nil {
		t.Fatalf("workspace/cleanup failed: %v", err)
	}
	t.Log("workspace cleaned up")

	t.Log("End-to-end pipeline test completed successfully!")
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