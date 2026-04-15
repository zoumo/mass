// Package integration_test provides end-to-end integration tests for the mass daemon.
// These tests verify the complete pipeline: mass → agent-shim → mockagent.
package integration_test

import (
	"fmt"
	"net"
	"testing"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// TestEndToEndPipeline tests the complete mass → agent-shim → mockagent lifecycle
// using the workspace/* and agentrun/* ARI surface.
// Pipeline: workspace/create → poll ready → agentrun/create → poll idle →
//
//	agentrun/prompt → poll running → agentrun/stop → poll stopped →
//	agentrun/delete → workspace/delete
func TestEndToEndPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "e2e-workspace"
	const agentName = "e2e-agent"

	// Step 1: workspace/create → poll until phase=ready
	t.Log("Step 1: workspace/create → poll until phase=ready")
	createTestWorkspace(t, ctx, client, wsName)
	t.Log("workspace ready ✓")

	// Step 2: agentrun/create → poll until state=idle
	t.Log("Step 2: agentrun/create → poll until state=idle")
	ar := createAgentAndWait(t, ctx, client, wsName, agentName, "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, agentName, ar.Status.State)
	if ar.Status.State != "idle" {
		t.Errorf("expected state=idle, got %s", ar.Status.State)
	}
	t.Log("agent state=idle ✓")

	// Step 3: agentrun/prompt (async dispatch)
	t.Log("Step 3: agentrun/prompt (async dispatch)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, "hello from e2e integration test")
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 4: poll until state=running or idle (mockagent is instant, turn may complete fast)
	t.Log("Step 4: verify agent state=running (or idle if turn already completed)")
	st4 := waitForAgentStateOneOf(t, ctx, client, wsName, agentName, []string{"running", "idle"}, 10*time.Second)
	t.Logf("agent state=%s after prompt ✓", st4.Status.State)

	// Step 5: agentrun/stop → poll until state=stopped
	t.Log("Step 5: agentrun/stop → poll until state=stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = waitForAgentState(t, ctx, client, wsName, agentName, "stopped", 10*time.Second)
	t.Log("agent state=stopped ✓")

	// Step 6: agentrun/delete
	t.Log("Step 6: agentrun/delete")
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Fatalf("agentrun/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	// Step 7: workspace/delete
	t.Log("Step 7: workspace/delete")
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: wsName}, &pkgariapi.Workspace{}); err != nil {
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
