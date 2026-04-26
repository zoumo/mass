// Package mass_test provides integration tests for the mass server process.
// These tests verify ARI surfaces through the mass daemon.
package mass_test

import (
	"testing"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// TestRuntimeLifecycle is the S02 acceptance test.
// It verifies the full chain:
//
//	mass server --root → agent/create (via SetupMassTest)
//	→ agent/get → agent/list
//	→ workspace/create + agentrun/create → idle state
//	→ agent/delete → agent/get returns error
func TestRuntimeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	// ── Step 1: agent/get mockagent → assert name and non-empty command ──────
	t.Log("Step 1: agent/get mockagent")
	var getResult pkgariapi.Agent
	if err := client.Get(ctx, pkgariapi.ObjectKey{Name: "mockagent"}, &getResult); err != nil {
		t.Fatalf("agent/get mockagent: %v", err)
	}
	if getResult.Metadata.Name != "mockagent" {
		t.Errorf("agent/get: expected name=%q, got %q", "mockagent", getResult.Metadata.Name)
	}
	if getResult.Spec.Command == "" {
		t.Error("agent/get: expected non-empty command")
	}
	t.Logf("agent/get OK: name=%s command=%s", getResult.Metadata.Name, getResult.Spec.Command)

	// ── Step 2: agent/list → assert 1 entry ─────────────────────────────────
	t.Log("Step 2: agent/list")
	var listResult pkgariapi.AgentList
	if err := client.List(ctx, &listResult); err != nil {
		t.Fatalf("agent/list: %v", err)
	}
	// Built-in agents (claude, codex, gsd-pi) are seeded on startup,
	// so we expect at least mockagent to be present.
	found := false
	for _, ag := range listResult.Items {
		if ag.Metadata.Name == "mockagent" {
			found = true
			break
		}
	}
	if !found {
		t.Error("agent/list: mockagent not found in list")
	}
	t.Logf("agent/list OK: %d agents (includes built-ins)", len(listResult.Items))

	// ── Step 3: workspace/create + agentrun/create → poll idle ───────────────
	t.Log("Step 3: workspace/create")
	const wsName = "rt-workspace"
	const agentName = "rt-agent"
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	t.Logf("workspace ready: %s", wsName)

	t.Log("Step 4: agentrun/create → wait for idle")
	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, agentName, "mockagent")

	// ── Step 4: assert state == idle ──────────────────────────────────────────
	if ar.Status.Phase != "idle" {
		t.Errorf("agentrun/create: expected state=idle, got %s", ar.Status.Phase)
	} else {
		t.Logf("agent reached idle: workspace=%s name=%s state=%s", wsName, agentName, ar.Status.Phase)
	}

	// Cleanup agent before deleting agent definition
	testutil.StopAndDeleteAgent(t, ctx, client, wsName, agentName)
	testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	// ── Step 5: agent/delete mockagent → no error ───────────────────────────
	t.Log("Step 5: agent/delete mockagent")
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: "mockagent"}, &pkgariapi.Agent{}); err != nil {
		t.Fatalf("agent/delete mockagent: %v", err)
	}
	t.Log("agent/delete OK")

	// ── Step 6: agent/get mockagent → expect error response ─────────────────
	t.Log("Step 6: agent/get mockagent after delete → expect error")
	var getAfterDelete pkgariapi.Agent
	err := client.Get(ctx, pkgariapi.ObjectKey{Name: "mockagent"}, &getAfterDelete)
	if err == nil {
		t.Error("agent/get after delete: expected error, got nil")
	} else {
		t.Logf("agent/get after delete returned expected error: %v", err)
	}
}
