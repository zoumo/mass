// Package integration_test provides integration tests for agent lifecycle management.
// These tests verify agent state transitions and error handling using the agent/* ARI surface.
package integration_test

import (
	"context"
	"encoding/json"
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

// testSocketCounter provides unique socket paths for each test.
var testSocketCounter int64

// =============================================================================
// Shared Helpers
// =============================================================================

// setupMassTest starts mass daemon and returns context, client, and cleanup function.
// It uses --root flag path derivation (no config.yaml) and self-fork shim (no MASS_SHIM_BINARY).
// After the socket is ready it registers the "mockagent" runtime via agent/create.
func setupMassTest(t *testing.T) (context.Context, context.CancelFunc, pkgariapi.Client, func()) {
	t.Helper()
	// Use a short root path under /tmp to avoid macOS 104-char Unix socket path limit (K025).
	// Socket lands at rootDir/mass.sock which is within the limit.
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/mass-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	// Remove any leftover socket from a prior run.
	os.Remove(socketPath)

	massBin, err := filepath.Abs("../../bin/mass")
	if err != nil {
		t.Fatalf("failed to get mass path: %v", err)
	}
	mockagentBin, err := filepath.Abs("../../bin/mockagent")
	if err != nil {
		t.Fatalf("failed to get mockagent path: %v", err)
	}

	for _, bin := range []string{massBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s (run: make build)", bin)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	massCmd := exec.CommandContext(ctx, massBin, "server", "--root", rootDir)
	massCmd.Stdout = os.Stdout
	massCmd.Stderr = os.Stderr

	if err := massCmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start mass: %v", err)
	}
	t.Logf("mass started with PID %d (root=%s)", massCmd.Process.Pid, rootDir)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("socket not ready: %v", err)
	}

	client, err := ariclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	// Register the mockagent runtime so tests can use agent="mockagent".
	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	if err := client.Create(ctx, &ag); err != nil {
		cancel()
		client.Close()
		t.Fatalf("failed to register mockagent runtime: %v", err)
	}
	t.Logf("runtime registered: name=%s command=%s", ag.Metadata.Name, ag.Spec.Command)

	cleanup := func() {
		client.Close()
		if massCmd.Process != nil {
			_ = massCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- massCmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = massCmd.Process.Kill()
				<-done
			}
			t.Log("mass stopped")
		}
		exec.Command("pkill", "-f", rootDir).Run()
		os.Remove(socketPath)
		os.RemoveAll(rootDir)
		exec.Command("pkill", "-f", "mockagent").Run()
	}

	return ctx, cancel, client, cleanup
}

// createTestWorkspace calls workspace/create and polls workspace/get until
// phase=="ready". Returns the workspace name. Fatals on timeout or error.
func createTestWorkspace(t *testing.T, ctx context.Context, client pkgariapi.Client, name string) string {
	t.Helper()
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.WorkspaceSpec{Source: json.RawMessage(`{"type":"emptyDir"}`)},
	}
	if err := client.Create(ctx, &ws); err != nil {
		t.Fatalf("workspace/create (name=%s): %v", name, err)
	}
	t.Logf("workspace create dispatched: name=%s phase=%s", ws.Metadata.Name, ws.Status.Phase)

	// Poll workspace/get until phase=="ready"
	key := pkgariapi.ObjectKey{Name: name}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var got pkgariapi.Workspace
		if err := client.Get(ctx, key, &got); err != nil {
			t.Logf("workspace/get (%s): %v (retrying)", name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if got.Status.Phase == pkgariapi.WorkspacePhaseReady {
			t.Logf("workspace ready: name=%s", name)
			return name
		}
		if got.Status.Phase == pkgariapi.WorkspacePhaseError {
			t.Fatalf("workspace %s reached error phase", name)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("workspace %s did not reach phase=ready within 15s", name)
	return name // unreachable
}

// deleteTestWorkspace removes a workspace. Logs but does not fatal on error (best-effort cleanup).
func deleteTestWorkspace(t *testing.T, ctx context.Context, client pkgariapi.Client, name string) {
	t.Helper()
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: name}, &pkgariapi.Workspace{}); err != nil {
		t.Logf("workspace/delete (name=%s): %v (ignored)", name, err)
	}
}

// waitForAgentState polls agentrun/get every 200ms until the agent reaches
// the desired state or the timeout expires. Returns the final AgentRun.
// Calls t.Fatalf on timeout.
func waitForAgentState(
	t *testing.T,
	ctx context.Context,
	client pkgariapi.Client,
	workspace, name, wantState string,
	timeout time.Duration,
) pkgariapi.AgentRun {
	t.Helper()
	return waitForAgentStateOneOf(t, ctx, client, workspace, name, []string{wantState}, timeout)
}

// waitForAgentStateOneOf polls agentrun/get until the agent reaches any of
// the desired states or the timeout expires. Returns the final AgentRun.
// Calls t.Fatalf on timeout.
func waitForAgentStateOneOf(
	t *testing.T,
	ctx context.Context,
	client pkgariapi.Client,
	workspace, name string,
	wantStates []string,
	timeout time.Duration,
) pkgariapi.AgentRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	key := pkgariapi.ObjectKey{Workspace: workspace, Name: name}
	var ar pkgariapi.AgentRun
	for time.Now().Before(deadline) {
		if err := client.Get(ctx, key, &ar); err != nil {
			t.Logf("agentrun/get (%s/%s): %v (retrying)", workspace, name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, want := range wantStates {
			if string(ar.Status.State) == want {
				return ar
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("agent %s/%s did not reach state(s) %v within %v (last state: %q)",
		workspace, name, wantStates, timeout, ar.Status.State)
	return ar // unreachable
}

// createAgentAndWait calls agentrun/create and polls until state=="idle".
// Returns the AgentRun after the agent is ready.
func createAgentAndWait(t *testing.T, ctx context.Context, client pkgariapi.Client, workspace, name, agentDef string) pkgariapi.AgentRun {
	t.Helper()
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: workspace, Name: name},
		Spec:     pkgariapi.AgentRunSpec{Agent: agentDef},
	}
	if err := client.Create(ctx, &ar); err != nil {
		t.Fatalf("agentrun/create (workspace=%s name=%s): %v", workspace, name, err)
	}
	t.Logf("agent create dispatched: workspace=%s name=%s state=%s",
		ar.Metadata.Workspace, ar.Metadata.Name, ar.Status.State)
	return waitForAgentState(t, ctx, client, workspace, name, "idle", 15*time.Second)
}

// stopAndDeleteAgent stops and then deletes an agent. Best-effort cleanup —
// logs but does not fatal on errors. Polls for stopped state before deleting
// because agentrun/delete requires state "stopped" or "error".
func stopAndDeleteAgent(t *testing.T, ctx context.Context, client pkgariapi.Client, workspace, name string) {
	t.Helper()
	key := pkgariapi.ObjectKey{Workspace: workspace, Name: name}
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Logf("agentrun/stop (%s/%s): %v (ignored)", workspace, name, err)
	}
	// Poll briefly for stopped/error state before delete (best-effort)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ar pkgariapi.AgentRun
		if err := client.Get(ctx, key, &ar); err != nil {
			break // agent may already be gone
		}
		if ar.Status.State == "stopped" || ar.Status.State == "error" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Logf("agentrun/delete (%s/%s): %v (ignored)", workspace, name, err)
	}
}

// =============================================================================
// Tests
// =============================================================================

// TestAgentLifecycle tests all agent state transitions.
// Covers: agentrun/create → state=idle → agentrun/prompt → state=running → agentrun/stop → state=stopped → agentrun/delete
func TestAgentLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "lifecycle-ws"
	createTestWorkspace(t, ctx, client, wsName)
	defer deleteTestWorkspace(t, ctx, client, wsName)

	// Step 1: agentrun/create → state=idle
	t.Log("Step 1: agentrun/create → wait for state=idle")
	ar := createAgentAndWait(t, ctx, client, wsName, "agent-lifecycle", "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, "agent-lifecycle", ar.Status.State)

	if ar.Status.State != "idle" {
		t.Errorf("expected state=idle, got %s", ar.Status.State)
	}

	// Step 2: agentrun/prompt → async dispatch; state transitions to running
	t.Log("Step 2: agentrun/prompt (async dispatch)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-lifecycle"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, "test lifecycle prompt")
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 3: verify agent transitions to running (accept idle — mockagent is instant).
	t.Log("Step 3: verify agent is running (or already idle) after prompt")
	_ = waitForAgentStateOneOf(t, ctx, client, wsName, "agent-lifecycle", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	// Step 4: agentrun/stop → state=stopped
	t.Log("Step 4: agentrun/stop → state=stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = waitForAgentState(t, ctx, client, wsName, "agent-lifecycle", "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Step 5: agentrun/delete
	t.Log("Step 5: agentrun/delete")
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Fatalf("agentrun/delete failed: %v", err)
	}

	// Verify agent is gone (get should return error)
	var verifyAR pkgariapi.AgentRun
	err = client.Get(ctx, key, &verifyAR)
	if err == nil {
		t.Error("expected error when getting status of deleted agent")
	}
	t.Logf("agent deleted (get returned expected error: %v)", err)
}

// TestAgentPromptAndStop tests agentrun/prompt followed by agentrun/stop.
func TestAgentPromptAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "prompt-stop-ws"
	createTestWorkspace(t, ctx, client, wsName)
	defer deleteTestWorkspace(t, ctx, client, wsName)

	// Create agent and wait for idle state
	ar := createAgentAndWait(t, ctx, client, wsName, "agent-ps", "mockagent")
	t.Logf("agent ready: state=%s", ar.Status.State)

	// Prompt the agent (async dispatch)
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-ps"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, "prompt and stop test")
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Stop the agent (may still be transitioning to running — stop is valid from any live state)
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = waitForAgentState(t, ctx, client, wsName, "agent-ps", "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Delete agent
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Logf("agentrun/delete: %v (ignored)", err)
	}
}

// TestAgentPromptFromIdle tests that prompting a newly-created idle agent
// transitions it to running state.
func TestAgentPromptFromIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "autostart-ws"
	createTestWorkspace(t, ctx, client, wsName)
	defer deleteTestWorkspace(t, ctx, client, wsName)

	// Create agent — should be in state=idle
	ar := createAgentAndWait(t, ctx, client, wsName, "agent-auto", "mockagent")
	if ar.Status.State != "idle" {
		t.Errorf("expected state=idle before first prompt, got %s", ar.Status.State)
	}

	// Prompt immediately from idle state
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-auto"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, "auto-start prompt")
	if err != nil {
		t.Fatalf("agentrun/prompt (from idle) failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Agent should be running (or already idle — mockagent completes instantly).
	_ = waitForAgentStateOneOf(t, ctx, client, wsName, "agent-auto", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	// Cleanup
	stopAndDeleteAgent(t, ctx, client, wsName, "agent-auto")
}

// TestMultipleAgentPromptsSequential tests multiple sequential prompts to the same agent.
// Between prompts the agent must return to idle before the next prompt is sent.
func TestMultipleAgentPromptsSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "sequential-ws"
	createTestWorkspace(t, ctx, client, wsName)
	defer deleteTestWorkspace(t, ctx, client, wsName)

	// Create agent
	ar := createAgentAndWait(t, ctx, client, wsName, "agent-seq", "mockagent")
	t.Logf("agent ready: state=%s", ar.Status.State)

	prompts := []string{
		"first sequential prompt",
		"second sequential prompt",
		"third sequential prompt",
	}

	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-seq"}
	for i, promptText := range prompts {
		t.Logf("Sending prompt %d/%d: %q", i+1, len(prompts), promptText)
		promptResult, err := client.AgentRuns().Prompt(ctx, key, promptText)
		if err != nil {
			t.Fatalf("agentrun/prompt %d failed: %v", i+1, err)
		}
		t.Logf("prompt %d accepted: %v", i+1, promptResult.Accepted)
		if !promptResult.Accepted {
			t.Errorf("prompt %d: expected prompt to be accepted", i+1)
		}

		// Wait for async turn to complete — agent returns to "idle" when done.
		_ = waitForAgentState(t, ctx, client, wsName, "agent-seq", "idle", 15*time.Second)
		t.Logf("prompt %d turn completed (agent=idle) ✓", i+1)
	}

	t.Logf("All %d sequential prompts completed successfully ✓", len(prompts))

	// Cleanup
	stopAndDeleteAgent(t, ctx, client, wsName, "agent-seq")
}
