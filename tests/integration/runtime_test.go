// Package integration_test provides integration tests for runtime lifecycle management.
// These tests verify the full ARI runtime/* CRUD surface: set, get, list, delete,
// and that a runtime registered via runtime/set can launch a real agent to idle state.
package integration_test

import (
	"testing"

	pkgariapi "github.com/zoumo/oar/pkg/ari/api"
)

// TestRuntimeLifecycle is the S02 acceptance test.
// It verifies the full chain:
//
//	agentd server --root → runtime/set (via setupAgentdTest)
//	→ runtime/get → runtime/list
//	→ workspace/create + agent/create → idle state
//	→ runtime/delete → runtime/get returns error
func TestRuntimeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// setupAgentdTest starts agentd with --root, waits for socket, and registers
	// "mockagent" runtime via runtime/set. See session_test.go.
	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// ── Step 1: runtime/get mockagent → assert name and non-empty command ──────
	t.Log("Step 1: runtime/get mockagent")
	var getResult pkgariapi.AgentGetResult
	if err := client.Call("agent/get", pkgariapi.AgentGetParams{Name: "mockagent"}, &getResult); err != nil {
		t.Fatalf("runtime/get mockagent: %v", err)
	}
	if getResult.Agent.Metadata.Name != "mockagent" {
		t.Errorf("runtime/get: expected name=%q, got %q", "mockagent", getResult.Agent.Metadata.Name)
	}
	if getResult.Agent.Spec.Command == "" {
		t.Error("runtime/get: expected non-empty command")
	}
	t.Logf("runtime/get OK: name=%s command=%s", getResult.Agent.Metadata.Name, getResult.Agent.Spec.Command)

	// ── Step 2: runtime/list → assert 1 entry ─────────────────────────────────
	t.Log("Step 2: runtime/list")
	var listResult pkgariapi.AgentListResult
	if err := client.Call("agent/list", pkgariapi.AgentListParams{}, &listResult); err != nil {
		t.Fatalf("runtime/list: %v", err)
	}
	if len(listResult.Agents) != 1 {
		t.Errorf("runtime/list: expected 1 runtime, got %d", len(listResult.Agents))
	} else {
		t.Logf("runtime/list OK: 1 runtime (%s)", listResult.Agents[0].Metadata.Name)
	}

	// ── Step 3: workspace/create + agent/create → poll idle ───────────────────
	t.Log("Step 3: workspace/create")
	const wsName = "rt-workspace"
	const agentName = "rt-agent"
	createTestWorkspace(t, client, wsName)
	t.Logf("workspace ready: %s", wsName)

	t.Log("Step 4: agent/create → wait for idle")
	status := createAgentAndWait(t, client, wsName, agentName, "mockagent")

	// ── Step 4: assert state == idle ──────────────────────────────────────────
	if status.AgentRun.Status.State != "idle" {
		t.Errorf("agent/create: expected state=idle, got %s", status.AgentRun.Status.State)
	} else {
		t.Logf("agent reached idle ✓: workspace=%s name=%s state=%s", wsName, agentName, status.AgentRun.Status.State)
	}

	// Cleanup agent before deleting runtime
	stopAndDeleteAgent(t, client, wsName, agentName)
	deleteTestWorkspace(t, client, wsName)

	// ── Step 5: runtime/delete mockagent → no error ───────────────────────────
	t.Log("Step 5: runtime/delete mockagent")
	if err := client.Call("agent/delete", pkgariapi.AgentDeleteParams{Name: "mockagent"}, nil); err != nil {
		t.Fatalf("runtime/delete mockagent: %v", err)
	}
	t.Log("runtime/delete OK ✓")

	// ── Step 6: runtime/get mockagent → expect error response ─────────────────
	t.Log("Step 6: runtime/get mockagent after delete → expect error")
	var getAfterDelete pkgariapi.AgentGetResult
	err := client.Call("agent/get", pkgariapi.AgentGetParams{Name: "mockagent"}, &getAfterDelete)
	if err == nil {
		t.Error("runtime/get after delete: expected error, got nil")
	} else {
		t.Logf("runtime/get after delete returned expected error ✓: %v", err)
	}
}
