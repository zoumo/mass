// Package integration_test provides integration tests for agentd restart recovery.
// These tests verify that agent identity (workspace+name) survives daemon restart,
// that dead shims are fail-closed to a terminal state (stopped per D012/D029), and
// that the recovery reconciliation works end-to-end.
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

	ari "github.com/zoumo/oar/api/ari"
	ariclient "github.com/zoumo/oar/pkg/ari"
)

// startAgentd launches agentd with --root rootDir, waits for the socket,
// and returns the Cmd. Caller is responsible for cleanup.
func startAgentd(t *testing.T, ctx context.Context, agentdBin, rootDir, socketPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, agentdBin, "server", "--root", rootDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d (root=%s)", cmd.Process.Pid, rootDir)

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
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		t.Log("agentd stopped")
	}
	exec.Command("pkill", "-f", filepath.Dir(socketPath)).Run()
	os.Remove(socketPath)
}

// TestAgentdRestartRecovery proves that agent identity (workspace+name) survives
// daemon restart and that dead shims are fail-closed to "error" state.
//
// Strategy: kill ALL agent-shim and mockagent processes after stopping agentd,
// so both agents have dead shims on restart → both should be marked error.
func TestAgentdRestartRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Setup ──────────────────────────────────────────────────────────────
	// Use a persistent rootDir under /tmp so the metaDB survives the restart.
	// Socket lands at rootDir/agentd.sock which is within the 104-char macOS limit (K025).
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/oar-restart-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "agentd.sock")

	agentdBin, _ := filepath.Abs("../../bin/agentd")
	mockagentBin, _ := filepath.Abs("../../bin/mockagent")

	for _, bin := range []string{agentdBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s (run: make build)", bin)
		}
	}

	// Cleanup leftover processes and rootDir at the end of the test.
	defer func() {
		os.Remove(socketPath)
		os.RemoveAll(rootDir)
		exec.Command("pkill", "-f", rootDir).Run()
		exec.Command("pkill", "-f", "mockagent").Run()
	}()

	// =========================================================================
	// Phase 1: Start agentd, create workspace, create agent-A and agent-B
	// =========================================================================
	t.Log("Phase 1: Start agentd, create workspace, create agent-A and agent-B")

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	agentdCmd1 := startAgentd(t, ctx1, agentdBin, rootDir, socketPath)

	client1, err := ariclient.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client: %v", err)
	}

	// Register the mockagent runtime so agents can be created with runtimeClass="mockagent".
	var runtimeResult1 ari.AgentSetResult
	if err := client1.Call("agent/set", ari.AgentSetParams{
		Name:    "mockagent",
		Command: mockagentBin,
	}, &runtimeResult1); err != nil {
		t.Fatalf("runtime/set (phase 1): %v", err)
	}
	t.Logf("runtime registered (phase 1): name=%s", runtimeResult1.Agent.Metadata.Name)

	const wsName = "test-ws"
	createTestWorkspace(t, client1, wsName)
	t.Logf("workspace ready: %s", wsName)

	// Create agent-A (will have shim killed before restart).
	t.Log("Creating agent-A")
	statusA1 := createAgentAndWait(t, client1, wsName, "agent-a", "mockagent")
	t.Logf("agent-A: workspace=%s name=%s state=%s",
		statusA1.AgentRun.Metadata.Workspace, statusA1.AgentRun.Metadata.Name, statusA1.AgentRun.Status.State)

	if statusA1.AgentRun.Status.State != "idle" {
		t.Fatalf("expected agent-A state=idle, got %s", statusA1.AgentRun.Status.State)
	}

	// Create agent-B (will also have shim killed before restart).
	t.Log("Creating agent-B")
	statusB1 := createAgentAndWait(t, client1, wsName, "agent-b", "mockagent")
	t.Logf("agent-B: workspace=%s name=%s state=%s",
		statusB1.AgentRun.Metadata.Workspace, statusB1.AgentRun.Metadata.Name, statusB1.AgentRun.Status.State)

	// Prompt agent-A to exercise the running state (async dispatch).
	t.Log("Prompting agent-A before restart")
	var promptResultA ari.AgentRunPromptResult
	if err := client1.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-a",
		"prompt":    "hello before restart",
	}, &promptResultA); err != nil {
		t.Fatalf("agent/prompt A: %v", err)
	}
	t.Logf("agent-A prompt accepted: %v", promptResultA.Accepted)

	// Prompt agent-B (async dispatch).
	t.Log("Prompting agent-B before restart")
	var promptResultB ari.AgentRunPromptResult
	if err := client1.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-b",
		"prompt":    "hello before restart",
	}, &promptResultB); err != nil {
		t.Fatalf("agent/prompt B: %v", err)
	}
	t.Logf("agent-B prompt accepted: %v", promptResultB.Accepted)

	// Verify agent-A is in running (or idle) state before killing agentd.
	// The mockagent is instant so the turn may complete before we poll.
	_ = waitForAgentStateOneOf(t, client1, wsName, "agent-a", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent-A is in running/idle state after prompt ✓")

	// =========================================================================
	// Phase 2: Stop agentd, kill ALL shim and runtime processes
	// =========================================================================
	t.Log("Phase 2: Stop agentd and kill all agent-shim + mockagent processes")

	client1.Close()
	stopAgentd(t, agentdCmd1, socketPath)

	// Kill all agent-shim and mockagent processes so BOTH agents will have dead
	// shims on restart → both should be marked error by reconciliation.
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

	agentdCmd2 := startAgentd(t, ctx2, agentdBin, rootDir, socketPath)
	defer stopAgentd(t, agentdCmd2, socketPath)

	client2, err := ariclient.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client after restart: %v", err)
	}
	defer client2.Close()

	// Re-register the mockagent runtime on restart (runtimes are persisted in DB,
	// so this is idempotent — ensures the runtime is available after restart).
	var runtimeResult2 ari.AgentSetResult
	if err := client2.Call("agent/set", ari.AgentSetParams{
		Name:    "mockagent",
		Command: mockagentBin,
	}, &runtimeResult2); err != nil {
		t.Fatalf("runtime/set (phase 3): %v", err)
	}
	t.Logf("runtime registered (phase 3): name=%s", runtimeResult2.Agent.Metadata.Name)

	// Wait for recovery pass to complete (typically 1-2s).
	t.Log("Waiting for recovery pass to complete...")
	time.Sleep(2 * time.Second)

	// =========================================================================
	// Phase 4: Verify agent-A identity is preserved across restart
	// =========================================================================
	t.Log("Phase 4: Verify agent-A identity preserved across restart")

	// Agent-A should reach a terminal state after recovery — "stopped" per D012/D029
	// (the recovery code fail-closes dead shims as stopped, not error).
	terminalStates := []string{"stopped", "error"}
	statusA2 := waitForAgentStateOneOf(t, client2, wsName, "agent-a", terminalStates, 10*time.Second)

	// Identity: workspace and name must be preserved.
	if statusA2.AgentRun.Metadata.Workspace != wsName {
		t.Errorf("agent-A workspace changed across restart: expected=%s got=%s",
			wsName, statusA2.AgentRun.Metadata.Workspace)
	} else {
		t.Logf("agent-A workspace preserved ✓: %s", statusA2.AgentRun.Metadata.Workspace)
	}

	if statusA2.AgentRun.Metadata.Name != "agent-a" {
		t.Errorf("agent-A name changed across restart: expected=agent-a got=%s",
			statusA2.AgentRun.Metadata.Name)
	} else {
		t.Logf("agent-A name preserved ✓: %s", statusA2.AgentRun.Metadata.Name)
	}

	t.Logf("agent-A post-restart state=%s (shim killed → fail-closed, identity preserved)",
		statusA2.AgentRun.Status.State)

	// =========================================================================
	// Phase 5: Verify agent-B is in a terminal state (dead shim → fail-closed)
	// =========================================================================
	t.Log("Phase 5: Verify agent-B is in terminal state (dead shim fail-closed)")

	statusB2 := waitForAgentStateOneOf(t, client2, wsName, "agent-b", terminalStates, 10*time.Second)
	t.Logf("agent-B post-restart state=%s ✓", statusB2.AgentRun.Status.State)

	// =========================================================================
	// Phase 6: Verify agent list — both agents queryable with identity intact
	// =========================================================================
	t.Log("Phase 6: Verify agent list shows both agents in workspace")

	var listResult ari.AgentRunListResult
	if err := client2.Call("agentrun/list", map[string]interface{}{"workspace": wsName}, &listResult); err != nil {
		t.Fatalf("agent/list: %v", err)
	}
	t.Logf("agent/list returned %d agents in workspace %s", len(listResult.AgentRuns), wsName)

	if len(listResult.AgentRuns) != 2 {
		t.Errorf("expected 2 agents in workspace %s, got %d", wsName, len(listResult.AgentRuns))
	}

	agentStates := make(map[string]string) // name → state
	for _, a := range listResult.AgentRuns {
		agentStates[a.Metadata.Name] = string(a.Status.State)
		t.Logf("  agent: workspace=%s name=%s state=%s", a.Metadata.Workspace, a.Metadata.Name, a.Status.State)
	}
	// Recovery marks dead shims as stopped (D012/D029); accept stopped or error.
	for _, aName := range []string{"agent-a", "agent-b"} {
		st := agentStates[aName]
		if st != "stopped" && st != "error" {
			t.Errorf("%s: expected state=stopped or error after recovery, got %q", aName, st)
		}
	}

	// =========================================================================
	// Phase 7: Cleanup — stop then delete each agent; delete workspace
	// =========================================================================
	t.Log("Phase 7: Cleanup")

	// Agents in terminal state (stopped/error): call agent/stop (idempotent) then delete
	for _, agentName := range []string{"agent-a", "agent-b"} {
		if err := client2.Call("agentrun/stop", map[string]interface{}{
			"workspace": wsName,
			"name":      agentName,
		}, nil); err != nil {
			t.Logf("agent/stop %s: %v (may already be stopped)", agentName, err)
		}
		if err := client2.Call("agentrun/delete", map[string]interface{}{
			"workspace": wsName,
			"name":      agentName,
		}, nil); err != nil {
			t.Logf("agent/delete %s: %v (ignored)", agentName, err)
		}
	}

	// Delete workspace after all agents are removed.
	if err := client2.Call("workspace/delete", map[string]interface{}{"name": wsName}, nil); err != nil {
		t.Logf("workspace/delete: %v (ignored)", err)
	}

	t.Log("TestAgentdRestartRecovery completed ✓")
}
