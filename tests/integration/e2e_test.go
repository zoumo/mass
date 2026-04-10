// Package integration_test provides end-to-end integration tests for the agentd daemon.
// These tests verify the complete pipeline: agentd → agent-shim → mockagent.
package integration_test

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// TestEndToEndPipeline tests the complete agentd → agent-shim → mockagent lifecycle
// using the workspace/* and agent/* ARI surface.
// Pipeline: workspace/create → poll ready → agent/create → poll idle →
//
//	agent/prompt → poll running → agent/stop → poll stopped →
//	agent/delete → workspace/delete
func TestEndToEndPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "e2e-workspace"
	const agentName = "e2e-agent"

	// Step 1: workspace/create → poll until phase=ready
	t.Log("Step 1: workspace/create → poll until phase=ready")
	createTestWorkspace(t, client, wsName)
	t.Log("workspace ready ✓")

	// Step 2: agent/create → poll until state=idle
	t.Log("Step 2: agent/create → poll until state=idle")
	agentStatus := createAgentAndWait(t, client, wsName, agentName, "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, agentName, agentStatus.Agent.State)
	if agentStatus.Agent.State != "idle" {
		t.Errorf("expected state=idle, got %s", agentStatus.Agent.State)
	}
	t.Log("agent state=idle ✓")

	// Step 3: agent/prompt (async dispatch)
	t.Log("Step 3: agent/prompt (async dispatch)")
	var promptResult ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
		"prompt":    "hello from e2e integration test",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 4: poll until state=running or idle (mockagent is instant, turn may complete fast)
	t.Log("Step 4: verify agent state=running (or idle if turn already completed)")
	st4 := waitForAgentStateOneOf(t, client, wsName, agentName, []string{"running", "idle"}, 10*time.Second)
	t.Logf("agent state=%s after prompt ✓", st4.Agent.State)

	// Step 5: agent/stop → poll until state=stopped
	t.Log("Step 5: agent/stop → poll until state=stopped")
	if err := client.Call("agentrun/stop", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, wsName, agentName, "stopped", 10*time.Second)
	t.Log("agent state=stopped ✓")

	// Step 6: agent/delete
	t.Log("Step 6: agent/delete")
	if err := client.Call("agentrun/delete", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, nil); err != nil {
		t.Fatalf("agent/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	// Step 7: workspace/delete
	t.Log("Step 7: workspace/delete")
	if err := client.Call("workspace/delete", map[string]interface{}{
		"name": wsName,
	}, nil); err != nil {
		t.Fatalf("workspace/delete failed: %v", err)
	}
	t.Log("workspace deleted ✓")

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
