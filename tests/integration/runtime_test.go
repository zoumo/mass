// Package integration_test provides integration tests for runtime lifecycle management.
// These tests verify the full ARI agent/* CRUD surface: create, get, list, delete,
// and that an agent registered via agent/create can launch a real agent run to idle state.
package integration_test

import (
	"testing"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// TestRuntimeLifecycle is the S02 acceptance test.
// It verifies the full chain:
//
//	mass server --root → agent/create (via setupMassTest)
//	→ agent/get → agent/list
//	→ workspace/create + agentrun/create → idle state
//	→ agent/delete → agent/get returns error
func TestRuntimeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// setupMassTest starts mass with --root, waits for socket, and registers
	// "mockagent" agent via agent/create. See session_test.go.
	ctx, cancel, client, cleanup := setupMassTest(t)
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
	if len(listResult.Items) != 1 {
		t.Errorf("agent/list: expected 1 agent, got %d", len(listResult.Items))
	} else {
		t.Logf("agent/list OK: 1 agent (%s)", listResult.Items[0].Metadata.Name)
	}

	// ── Step 3: workspace/create + agentrun/create → poll idle ───────────────
	t.Log("Step 3: workspace/create")
	const wsName = "rt-workspace"
	const agentName = "rt-agent"
	createTestWorkspace(t, ctx, client, wsName)
	t.Logf("workspace ready: %s", wsName)

	t.Log("Step 4: agentrun/create → wait for idle")
	ar := createAgentAndWait(t, ctx, client, wsName, agentName, "mockagent")

	// ── Step 4: assert state == idle ──────────────────────────────────────────
	if ar.Status.State != "idle" {
		t.Errorf("agentrun/create: expected state=idle, got %s", ar.Status.State)
	} else {
		t.Logf("agent reached idle: workspace=%s name=%s state=%s", wsName, agentName, ar.Status.State)
	}

	// Cleanup agent before deleting agent definition
	stopAndDeleteAgent(t, ctx, client, wsName, agentName)
	deleteTestWorkspace(t, ctx, client, wsName)

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
