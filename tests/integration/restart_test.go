// Package integration_test provides integration tests for mass restart recovery.
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

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// startMass launches mass with --root rootDir, waits for the socket,
// and returns the Cmd. Caller is responsible for cleanup.
func startMass(t *testing.T, ctx context.Context, massBin, rootDir, socketPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, massBin, "server", "--root", rootDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start mass: %v", err)
	}
	t.Logf("mass started with PID %d (root=%s)", cmd.Process.Pid, rootDir)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}
	return cmd
}

// stopMass gracefully kills mass with SIGINT and waits for exit.
func stopMass(t *testing.T, cmd *exec.Cmd, socketPath string) {
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
		t.Log("mass stopped")
	}
	exec.Command("pkill", "-f", filepath.Dir(socketPath)).Run()
	os.Remove(socketPath)
}

// TestAgentdRestartRecovery proves that agent identity (workspace+name) survives
// daemon restart and that dead shims are fail-closed to "error" state.
//
// Strategy: kill ALL agent-shim and mockagent processes after stopping mass,
// so both agents have dead shims on restart → both should be marked error.
func TestAgentdRestartRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Setup ──────────────────────────────────────────────────────────────
	// Use a persistent rootDir under /tmp so the metaDB survives the restart.
	// Socket lands at rootDir/mass.sock which is within the 104-char macOS limit (K025).
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/mass-restart-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	massBin, _ := filepath.Abs("../../bin/mass")
	mockagentBin, _ := filepath.Abs("../../bin/mockagent")

	for _, bin := range []string{massBin, mockagentBin} {
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
	// Phase 1: Start mass, create workspace, create agent-A and agent-B
	// =========================================================================
	t.Log("Phase 1: Start mass, create workspace, create agent-A and agent-B")

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	massCmd1 := startMass(t, ctx1, massBin, rootDir, socketPath)

	client1, err := ariclient.Dial(ctx1, socketPath)
	if err != nil {
		t.Fatalf("ARI client: %v", err)
	}

	// Register the mockagent runtime so agents can be created with agent="mockagent".
	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	if err := client1.Create(ctx1, &ag); err != nil {
		t.Fatalf("agent/create (phase 1): %v", err)
	}
	t.Logf("runtime registered (phase 1): name=%s", ag.Metadata.Name)

	const wsName = "test-ws"
	createTestWorkspace(t, ctx1, client1, wsName)
	t.Logf("workspace ready: %s", wsName)

	// Create agent-A (will have shim killed before restart).
	t.Log("Creating agent-A")
	arA1 := createAgentAndWait(t, ctx1, client1, wsName, "agent-a", "mockagent")
	t.Logf("agent-A: workspace=%s name=%s state=%s",
		arA1.Metadata.Workspace, arA1.Metadata.Name, arA1.Status.State)

	if arA1.Status.State != "idle" {
		t.Fatalf("expected agent-A state=idle, got %s", arA1.Status.State)
	}

	// Create agent-B (will also have shim killed before restart).
	t.Log("Creating agent-B")
	arB1 := createAgentAndWait(t, ctx1, client1, wsName, "agent-b", "mockagent")
	t.Logf("agent-B: workspace=%s name=%s state=%s",
		arB1.Metadata.Workspace, arB1.Metadata.Name, arB1.Status.State)

	// Prompt agent-A to exercise the running state (async dispatch).
	t.Log("Prompting agent-A before restart")
	keyA := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-a"}
	promptResultA, err := client1.AgentRuns().Prompt(ctx1, keyA, "hello before restart")
	if err != nil {
		t.Fatalf("agentrun/prompt A: %v", err)
	}
	t.Logf("agent-A prompt accepted: %v", promptResultA.Accepted)

	// Prompt agent-B (async dispatch).
	t.Log("Prompting agent-B before restart")
	keyB := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-b"}
	promptResultB, err := client1.AgentRuns().Prompt(ctx1, keyB, "hello before restart")
	if err != nil {
		t.Fatalf("agentrun/prompt B: %v", err)
	}
	t.Logf("agent-B prompt accepted: %v", promptResultB.Accepted)

	// Verify agent-A is in running (or idle) state before killing agentd.
	// The mockagent is instant so the turn may complete before we poll.
	_ = waitForAgentStateOneOf(t, ctx1, client1, wsName, "agent-a", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent-A is in running/idle state after prompt ✓")

	// =========================================================================
	// Phase 2: Stop mass, kill ALL shim and runtime processes
	// =========================================================================
	t.Log("Phase 2: Stop mass and kill all agent-shim + mockagent processes")

	client1.Close()
	stopMass(t, massCmd1, socketPath)

	// Kill all agent-shim and mockagent processes so BOTH agents will have dead
	// shims on restart → both should be marked error by reconciliation.
	exec.Command("pkill", "-9", "-f", "agent-shim").Run()
	exec.Command("pkill", "-9", "-f", "mockagent").Run()
	t.Log("killed all agent-shim and mockagent processes")

	// Give processes time to die.
	time.Sleep(500 * time.Millisecond)

	// =========================================================================
	// Phase 3: Restart mass with same config+metaDB
	// =========================================================================
	t.Log("Phase 3: Restart mass with same config — recovery pass should mark both agents error")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	massCmd2 := startMass(t, ctx2, massBin, rootDir, socketPath)
	defer stopMass(t, massCmd2, socketPath)

	client2, err := ariclient.Dial(ctx2, socketPath)
	if err != nil {
		t.Fatalf("ARI client after restart: %v", err)
	}
	defer client2.Close()

	// Re-register the mockagent runtime on restart (runtimes are persisted in DB,
	// so this is idempotent — ensures the runtime is available after restart).
	ag2 := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	// Use Update since agent already exists from Phase 1 persistence.
	if err := client2.Update(ctx2, &ag2); err != nil {
		// Fallback to Create if update fails (agent might not be persisted).
		if err2 := client2.Create(ctx2, &ag2); err2 != nil {
			t.Fatalf("agent register (phase 3): create=%v update=%v", err2, err)
		}
	}
	t.Logf("runtime registered (phase 3): name=%s", ag2.Metadata.Name)

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
	arA2 := waitForAgentStateOneOf(t, ctx2, client2, wsName, "agent-a", terminalStates, 10*time.Second)

	// Identity: workspace and name must be preserved.
	if arA2.Metadata.Workspace != wsName {
		t.Errorf("agent-A workspace changed across restart: expected=%s got=%s",
			wsName, arA2.Metadata.Workspace)
	} else {
		t.Logf("agent-A workspace preserved ✓: %s", arA2.Metadata.Workspace)
	}

	if arA2.Metadata.Name != "agent-a" {
		t.Errorf("agent-A name changed across restart: expected=agent-a got=%s",
			arA2.Metadata.Name)
	} else {
		t.Logf("agent-A name preserved ✓: %s", arA2.Metadata.Name)
	}

	t.Logf("agent-A post-restart state=%s (shim killed → fail-closed, identity preserved)",
		arA2.Status.State)

	// =========================================================================
	// Phase 5: Verify agent-B is in a terminal state (dead shim → fail-closed)
	// =========================================================================
	t.Log("Phase 5: Verify agent-B is in terminal state (dead shim fail-closed)")

	arB2 := waitForAgentStateOneOf(t, ctx2, client2, wsName, "agent-b", terminalStates, 10*time.Second)
	t.Logf("agent-B post-restart state=%s ✓", arB2.Status.State)

	// =========================================================================
	// Phase 6: Verify agent list — both agents queryable with identity intact
	// =========================================================================
	t.Log("Phase 6: Verify agent list shows both agents in workspace")

	var listResult pkgariapi.AgentRunList
	if err := client2.List(ctx2, &listResult, pkgariapi.InWorkspace(wsName)); err != nil {
		t.Fatalf("agentrun/list: %v", err)
	}
	t.Logf("agentrun/list returned %d agents in workspace %s", len(listResult.Items), wsName)

	if len(listResult.Items) != 2 {
		t.Errorf("expected 2 agents in workspace %s, got %d", wsName, len(listResult.Items))
	}

	agentStates := make(map[string]string) // name → state
	for _, a := range listResult.Items {
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

	// Agents in terminal state (stopped/error): call stop (idempotent) then delete
	for _, agentName := range []string{"agent-a", "agent-b"} {
		key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
		if err := client2.AgentRuns().Stop(ctx2, key); err != nil {
			t.Logf("agentrun/stop %s: %v (may already be stopped)", agentName, err)
		}
		if err := client2.Delete(ctx2, key, &pkgariapi.AgentRun{}); err != nil {
			t.Logf("agentrun/delete %s: %v (ignored)", agentName, err)
		}
	}

	// Delete workspace after all agents are removed.
	if err := client2.Delete(ctx2, pkgariapi.ObjectKey{Name: wsName}, &pkgariapi.Workspace{}); err != nil {
		t.Logf("workspace/delete: %v (ignored)", err)
	}

	t.Log("TestAgentdRestartRecovery completed ✓")
}
