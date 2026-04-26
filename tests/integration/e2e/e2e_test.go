// Package e2e_test provides end-to-end integration tests for the full pipeline.
// These tests verify the complete mass → agent-run → agent lifecycle.
package e2e_test

import (
	"testing"
	"time"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// TestEndToEndPipeline tests the complete mass → agent-run → mockagent lifecycle
// using the workspace/* and agentrun/* ARI surface.
// Pipeline: workspace/create → poll ready → agentrun/create → poll idle →
//
//	agentrun/prompt → poll running → agentrun/stop → poll stopped →
//	agentrun/delete → workspace/delete
func TestEndToEndPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "e2e-workspace"
	const agentName = "e2e-agent"

	// Step 1: workspace/create → poll until phase=ready
	t.Log("Step 1: workspace/create → poll until phase=ready")
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	t.Log("workspace ready ✓")

	// Step 2: agentrun/create → poll until state=idle
	t.Log("Step 2: agentrun/create → poll until state=idle")
	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, agentName, "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, agentName, ar.Status.Phase)
	if ar.Status.Phase != "idle" {
		t.Errorf("expected state=idle, got %s", ar.Status.Phase)
	}
	t.Log("agent state=idle ✓")

	// Step 3: agentrun/prompt (async dispatch)
	t.Log("Step 3: agentrun/prompt (async dispatch)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, []runapi.ContentBlock{runapi.TextBlock("hello from e2e integration test")})
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 4: poll until state=running or idle (mockagent is instant)
	t.Log("Step 4: verify agent state=running (or idle if turn already completed)")
	st4 := testutil.WaitForAgentStateOneOf(t, ctx, client, wsName, agentName, []string{"running", "idle"}, 10*time.Second)
	t.Logf("agent state=%s after prompt ✓", st4.Status.Phase)

	// Step 5: agentrun/stop → poll until state=stopped
	t.Log("Step 5: agentrun/stop → poll until state=stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = testutil.WaitForAgentState(t, ctx, client, wsName, agentName, "stopped", 10*time.Second)
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
