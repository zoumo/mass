// Package integration_test provides integration tests for agentd restart recovery.
// These tests verify that agent identity (room+name) survives daemon restart,
// that dead shims are fail-closed to "error" state, and that the recovery
// reconciliation introduced in M005/S07/T01 works end-to-end.
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

// startAgentd launches agentd with the given config and env, waits for the socket,
// and returns the Cmd. Caller is responsible for cleanup.
func startAgentd(t *testing.T, ctx context.Context, agentdBin, configPath, agentShimBin, socketPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, agentdBin, "--config", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("OAR_SHIM_BINARY=%s", agentShimBin))

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", cmd.Process.Pid)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}
	return cmd
}

// stopAgentd gracefully kills agentd with SIGINT and waits for exit.
func stopAgentd(t *testing.T, cmd *exec.Cmd, socketPath string) {
	t.Helper()
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Log("agentd stopped")
	}
	os.Remove(socketPath)
}

// TestAgentdRestartRecovery proves R052: agent identity (room+name) survives
// daemon restart and that dead shims are fail-closed to "error" state.
//
// Strategy: kill ALL agent-shim and mockagent processes after stopping agentd,
// so both agents have dead shims on restart → both should be marked error.
// R052 proof: agent-A's room and name match what was set before restart.
func TestAgentdRestartRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Setup ──────────────────────────────────────────────────────────────
	tmpDir := t.TempDir()
	testSocketCounter++
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), testSocketCounter)
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDB := filepath.Join(tmpDir, "agentd.db")
	bundleRoot := filepath.Join(tmpDir, "bundles")

	os.Remove(socketPath)
	for _, d := range []string{workspaceRoot, bundleRoot} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	agentdBin, _ := filepath.Abs("../../bin/agentd")
	agentShimBin, _ := filepath.Abs("../../bin/agent-shim")
	mockagentBin, _ := filepath.Abs("../../bin/mockagent")

	for _, bin := range []string{agentdBin, agentShimBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s", bin)
		}
	}

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
		t.Fatalf("write config: %v", err)
	}

	// Cleanup leftover processes at the end of the test.
	defer func() {
		os.Remove(socketPath)
		exec.Command("pkill", "-f", "agent-shim").Run()
		exec.Command("pkill", "-f", "mockagent").Run()
	}()

	// =========================================================================
	// Phase 1: Start agentd, create workspace + room, create agent-A + agent-B
	// =========================================================================
	t.Log("Phase 1: Start agentd, create room, create agent-A and agent-B")

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	agentdCmd1 := startAgentd(t, ctx1, agentdBin, configPath, agentShimBin, socketPath)

	client1, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client: %v", err)
	}

	// Prepare workspace.
	workspaceId := prepareTestWorkspace(t, ctx1, client1)
	t.Logf("workspace: %s", workspaceId)

	// Create room (required by agent/create FK constraint).
	var roomResult ari.RoomCreateResult
	if err := client1.Call("room/create", map[string]interface{}{"name": "test-room"}, &roomResult); err != nil {
		t.Fatalf("room/create: %v", err)
	}
	t.Logf("room created: %s", roomResult.Name)

	// Create agent-A (will have shim killed before restart).
	t.Log("Creating agent-A")
	statusA1 := createAgentAndWait(t, client1, workspaceId, "test-room", "agent-a")
	agentAId := statusA1.Agent.AgentId
	t.Logf("agent-A: id=%s state=%s room=%s name=%s",
		agentAId, statusA1.Agent.State, statusA1.Agent.Room, statusA1.Agent.Name)

	if statusA1.Agent.State != "created" {
		t.Fatalf("expected agent-A state=created, got %s", statusA1.Agent.State)
	}

	// Create agent-B (will also have shim killed before restart).
	t.Log("Creating agent-B")
	statusB1 := createAgentAndWait(t, client1, workspaceId, "test-room", "agent-b")
	agentBId := statusB1.Agent.AgentId
	t.Logf("agent-B: id=%s state=%s", agentBId, statusB1.Agent.State)

	// Prompt agent-A to exercise the running state (async dispatch).
	t.Log("Prompting agent-A before restart")
	var promptResultA ari.AgentPromptResult
	if err := client1.Call("agent/prompt", map[string]interface{}{
		"agentId": agentAId,
		"prompt":  "hello before restart",
	}, &promptResultA); err != nil {
		t.Fatalf("agent/prompt A: %v", err)
	}
	t.Logf("agent-A prompt accepted: %v", promptResultA.Accepted)

	// Prompt agent-B (async dispatch).
	t.Log("Prompting agent-B before restart")
	var promptResultB ari.AgentPromptResult
	if err := client1.Call("agent/prompt", map[string]interface{}{
		"agentId": agentBId,
		"prompt":  "hello before restart",
	}, &promptResultB); err != nil {
		t.Fatalf("agent/prompt B: %v", err)
	}
	t.Logf("agent-B prompt accepted: %v", promptResultB.Accepted)

	// agent/prompt is async: agent state transitions to "running" immediately
	// after dispatch. Verify agent-A is in running state before killing agentd.
	_ = waitForAgentState(t, client1, agentAId, "running", 10*time.Second)
	t.Log("agent-A is in running state after prompt ✓")

	// =========================================================================
	// Phase 2: Stop agentd, kill ALL shim and runtime processes
	// =========================================================================
	t.Log("Phase 2: Stop agentd and kill all agent-shim + mockagent processes")

	client1.Close()
	stopAgentd(t, agentdCmd1, socketPath)

	// Kill all agent-shim and mockagent processes so BOTH agents will have dead
	// shims on restart → both should be marked error by T01 reconciliation.
	exec.Command("pkill", "-9", "-f", "agent-shim").Run()
	exec.Command("pkill", "-9", "-f", "mockagent").Run()
	t.Log("killed all agent-shim and mockagent processes")

	// Give processes time to die.
	time.Sleep(500 * time.Millisecond)

	// =========================================================================
	// Phase 3: Restart agentd with same config+metaDB
	// =========================================================================
	t.Log("Phase 3: Restart agentd with same config — recovery pass should mark both agents error")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	agentdCmd2 := startAgentd(t, ctx2, agentdBin, configPath, agentShimBin, socketPath)
	defer stopAgentd(t, agentdCmd2, socketPath)

	client2, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client after restart: %v", err)
	}
	defer client2.Close()

	// Wait for recovery pass to complete (typically 1-2s).
	t.Log("Waiting for recovery pass to complete...")
	time.Sleep(2 * time.Second)

	// =========================================================================
	// Phase 4: Verify agent-A identity is preserved (R052 proof)
	// =========================================================================
	t.Log("Phase 4: Verify agent-A identity preserved across restart (R052)")

	// Agent-A should be in error state (shim killed).
	statusA2 := waitForAgentState(t, client2, agentAId, "error", 10*time.Second)

	// R052: agent identity (room + name + agentId) must be preserved.
	if statusA2.Agent.AgentId != agentAId {
		t.Errorf("R052 FAIL: agent-A ID changed across restart: pre=%s post=%s",
			agentAId, statusA2.Agent.AgentId)
	} else {
		t.Logf("R052: agent-A ID preserved ✓: %s", agentAId)
	}

	if statusA2.Agent.Room != "test-room" {
		t.Errorf("R052 FAIL: agent-A room changed across restart: expected=test-room got=%s",
			statusA2.Agent.Room)
	} else {
		t.Logf("R052: agent-A room preserved ✓: %s", statusA2.Agent.Room)
	}

	if statusA2.Agent.Name != "agent-a" {
		t.Errorf("R052 FAIL: agent-A name changed across restart: expected=agent-a got=%s",
			statusA2.Agent.Name)
	} else {
		t.Logf("R052: agent-A name preserved ✓: %s", statusA2.Agent.Name)
	}

	t.Logf("agent-A post-restart state=%s (shim killed → error, identity preserved)",
		statusA2.Agent.State)

	// =========================================================================
	// Phase 5: Verify agent-B has state=="error" (dead shim → fail-closed)
	// =========================================================================
	t.Log("Phase 5: Verify agent-B state=error (dead shim fail-closed from T01 reconciliation)")

	statusB2 := waitForAgentState(t, client2, agentBId, "error", 10*time.Second)
	t.Logf("agent-B post-restart state=%s ✓", statusB2.Agent.State)

	if statusB2.Agent.State != "error" {
		t.Errorf("expected agent-B state=error (dead shim), got %s", statusB2.Agent.State)
	}

	// =========================================================================
	// Phase 6: Verify R052 agent list — both agents queryable with identity intact
	// =========================================================================
	t.Log("Phase 6: Verify agent list shows both agents with intact room identity")

	var listResult ari.AgentListResult
	if err := client2.Call("agent/list", map[string]interface{}{"room": "test-room"}, &listResult); err != nil {
		t.Fatalf("agent/list: %v", err)
	}
	t.Logf("agent/list returned %d agents in room test-room", len(listResult.Agents))

	if len(listResult.Agents) != 2 {
		t.Errorf("expected 2 agents in test-room, got %d", len(listResult.Agents))
	}

	agentNames := make(map[string]string) // name → state
	for _, a := range listResult.Agents {
		agentNames[a.Name] = a.State
		t.Logf("  agent: id=%s name=%s room=%s state=%s", a.AgentId, a.Name, a.Room, a.State)
	}
	if agentNames["agent-a"] != "error" {
		t.Errorf("agent-a: expected state=error, got %q", agentNames["agent-a"])
	}
	if agentNames["agent-b"] != "error" {
		t.Errorf("agent-b: expected state=error, got %q", agentNames["agent-b"])
	}

	// =========================================================================
	// Phase 7: Cleanup
	// =========================================================================
	t.Log("Phase 7: Cleanup")

	// Stop then delete agent-A. Agents in error state still require agent/stop
	// before agent/delete (the delete gate only allows stopped agents).
	if err := client2.Call("agent/stop", map[string]interface{}{"agentId": agentAId}, nil); err != nil {
		t.Logf("agent/stop A: %v (may already be stopped)", err)
	}
	if err := client2.Call("agent/delete", map[string]interface{}{"agentId": agentAId}, nil); err != nil {
		t.Logf("agent/delete A: %v", err)
	}
	// Stop then delete agent-B.
	if err := client2.Call("agent/stop", map[string]interface{}{"agentId": agentBId}, nil); err != nil {
		t.Logf("agent/stop B: %v (may already be stopped)", err)
	}
	if err := client2.Call("agent/delete", map[string]interface{}{"agentId": agentBId}, nil); err != nil {
		t.Logf("agent/delete B: %v", err)
	}
	// Delete room.
	if err := client2.Call("room/delete", map[string]interface{}{"name": "test-room"}, nil); err != nil {
		t.Logf("room/delete: %v", err)
	}
	// Cleanup workspace.
	cleanupTestWorkspace(t, client2, workspaceId)

	t.Log("TestAgentdRestartRecovery completed ✓")
}
