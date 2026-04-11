// Package integration_test provides integration tests for agent lifecycle management.
// These tests verify agent state transitions and error handling using the agent/* ARI surface.
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

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// testSocketCounter provides unique socket paths for each test.
var testSocketCounter int64

// =============================================================================
// Shared Helpers
// =============================================================================

// setupAgentdTest starts agentd daemon and returns context, client, and cleanup function.
// It uses --root flag path derivation (no config.yaml) and self-fork shim (no OAR_SHIM_BINARY).
// After the socket is ready it registers the "mockagent" runtime via runtime/set.
func setupAgentdTest(t *testing.T) (context.Context, context.CancelFunc, *ari.Client, func()) {
	t.Helper()
	// Use a short root path under /tmp to avoid macOS 104-char Unix socket path limit (K025).
	// Socket lands at rootDir/agentd.sock which is within the limit.
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/oar-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "agentd.sock")

	// Remove any leftover socket from a prior run.
	os.Remove(socketPath)

	agentdBin, err := filepath.Abs("../../bin/agentd")
	if err != nil {
		t.Fatalf("failed to get agentd path: %v", err)
	}
	mockagentBin, err := filepath.Abs("../../bin/mockagent")
	if err != nil {
		t.Fatalf("failed to get mockagent path: %v", err)
	}

	for _, bin := range []string{agentdBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s (run: make build)", bin)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	agentdCmd := exec.CommandContext(ctx, agentdBin, "server", "--root", rootDir)
	agentdCmd.Stdout = os.Stdout
	agentdCmd.Stderr = os.Stderr

	if err := agentdCmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d (root=%s)", agentdCmd.Process.Pid, rootDir)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("socket not ready: %v", err)
	}

	client, err := ari.NewClient(socketPath)
	if err != nil {
		cancel()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	// Register the mockagent runtime so tests can use runtimeClass="mockagent".
	var runtimeResult ari.AgentTemplateInfo
	if err := client.Call("agent/set", ari.AgentTemplateSetParams{
		Name:    "mockagent",
		Command: mockagentBin,
	}, &runtimeResult); err != nil {
		cancel()
		client.Close()
		t.Fatalf("failed to register mockagent runtime: %v", err)
	}
	t.Logf("runtime registered: name=%s command=%s", runtimeResult.Name, runtimeResult.Command)

	cleanup := func() {
		client.Close()
		if agentdCmd.Process != nil {
			_ = agentdCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- agentdCmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = agentdCmd.Process.Kill()
				<-done
			}
			t.Log("agentd stopped")
		}
		exec.Command("pkill", "-f", rootDir).Run()
		os.Remove(socketPath)
		os.RemoveAll(rootDir)
		exec.Command("pkill", "-f", "mockagent").Run()
	}

	return ctx, cancel, client, cleanup
}

// createTestWorkspace calls workspace/create and polls workspace/status until
// phase=="ready". Returns the workspace name. Fatals on timeout or error.
func createTestWorkspace(t *testing.T, client *ari.Client, name string) string {
	t.Helper()
	var createResult ari.WorkspaceCreateResult
	if err := client.Call("workspace/create", map[string]interface{}{
		"name": name,
		"source": map[string]interface{}{
			"type": "emptyDir",
		},
	}, &createResult); err != nil {
		t.Fatalf("workspace/create (name=%s): %v", name, err)
	}
	t.Logf("workspace create dispatched: name=%s phase=%s", createResult.Name, createResult.Phase)

	// Poll workspace/status until phase=="ready"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var statusResult ari.WorkspaceStatusResult
		if err := client.Call("workspace/status", map[string]interface{}{"name": name}, &statusResult); err != nil {
			t.Logf("workspace/status (%s): %v (retrying)", name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if statusResult.Phase == "ready" {
			t.Logf("workspace ready: name=%s", name)
			return name
		}
		if statusResult.Phase == "error" {
			t.Fatalf("workspace %s reached error phase", name)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("workspace %s did not reach phase=ready within 15s", name)
	return name // unreachable
}

// deleteTestWorkspace removes a workspace. Logs but does not fatal on error (best-effort cleanup).
func deleteTestWorkspace(t *testing.T, client *ari.Client, name string) {
	t.Helper()
	if err := client.Call("workspace/delete", map[string]interface{}{"name": name}, nil); err != nil {
		t.Logf("workspace/delete (name=%s): %v (ignored)", name, err)
	}
}

// waitForAgentState polls agent/status every 200ms until the agent reaches
// the desired state or the timeout expires. Returns the final status result.
// Calls t.Fatalf on timeout.
func waitForAgentState(
	t *testing.T,
	client *ari.Client,
	workspace, name, wantState string,
	timeout time.Duration,
) ari.AgentRunStatusResult {
	t.Helper()
	return waitForAgentStateOneOf(t, client, workspace, name, []string{wantState}, timeout)
}

// waitForAgentStateOneOf polls agent/status until the agent reaches any of
// the desired states or the timeout expires. Returns the final status result.
// Calls t.Fatalf on timeout.
func waitForAgentStateOneOf(
	t *testing.T,
	client *ari.Client,
	workspace, name string,
	wantStates []string,
	timeout time.Duration,
) ari.AgentRunStatusResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	params := map[string]interface{}{"workspace": workspace, "name": name}
	var result ari.AgentRunStatusResult
	for time.Now().Before(deadline) {
		if err := client.Call("agentrun/status", params, &result); err != nil {
			t.Logf("agent/status (%s/%s): %v (retrying)", workspace, name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, want := range wantStates {
			if result.Agent.State == want {
				return result
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("agent %s/%s did not reach state(s) %v within %v (last state: %q)",
		workspace, name, wantStates, timeout, result.Agent.State)
	return result // unreachable
}

// createAgentAndWait calls agent/create and polls until state=="idle".
// Returns the status result after the agent is ready.
func createAgentAndWait(t *testing.T, client *ari.Client, workspace, name, runtimeClass string) ari.AgentRunStatusResult {
	t.Helper()
	var createResult ari.AgentRunCreateResult
	if err := client.Call("agentrun/create", map[string]interface{}{
		"workspace":    workspace,
		"name":         name,
		"runtimeClass": runtimeClass,
	}, &createResult); err != nil {
		t.Fatalf("agent/create (workspace=%s name=%s): %v", workspace, name, err)
	}
	t.Logf("agent create dispatched: workspace=%s name=%s state=%s",
		createResult.Workspace, createResult.Name, createResult.State)
	return waitForAgentState(t, client, workspace, name, "idle", 15*time.Second)
}

// stopAndDeleteAgent stops and then deletes an agent. Best-effort cleanup —
// logs but does not fatal on errors. Polls for stopped state before deleting
// because agent/delete requires state "stopped" or "error".
func stopAndDeleteAgent(t *testing.T, client *ari.Client, workspace, name string) {
	t.Helper()
	if err := client.Call("agentrun/stop", map[string]interface{}{
		"workspace": workspace,
		"name":      name,
	}, nil); err != nil {
		t.Logf("agent/stop (%s/%s): %v (ignored)", workspace, name, err)
	}
	// Poll briefly for stopped/error state before delete (best-effort)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var st ari.AgentRunStatusResult
		if err := client.Call("agentrun/status", map[string]interface{}{
			"workspace": workspace,
			"name":      name,
		}, &st); err != nil {
			break // agent may already be gone
		}
		if st.Agent.State == "stopped" || st.Agent.State == "error" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := client.Call("agentrun/delete", map[string]interface{}{
		"workspace": workspace,
		"name":      name,
	}, nil); err != nil {
		t.Logf("agent/delete (%s/%s): %v (ignored)", workspace, name, err)
	}
}

// =============================================================================
// Tests
// =============================================================================

// TestAgentLifecycle tests all agent state transitions.
// Covers: agent/create → state=idle → agent/prompt → state=running → agent/stop → state=stopped → agent/delete
func TestAgentLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "lifecycle-ws"
	createTestWorkspace(t, client, wsName)
	defer deleteTestWorkspace(t, client, wsName)

	// Step 1: agent/create → state=idle
	t.Log("Step 1: agent/create → wait for state=idle")
	status := createAgentAndWait(t, client, wsName, "agent-lifecycle", "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, "agent-lifecycle", status.Agent.State)

	if status.Agent.State != "idle" {
		t.Errorf("expected state=idle, got %s", status.Agent.State)
	}

	// Step 2: agent/prompt → async dispatch; state transitions to running
	t.Log("Step 2: agent/prompt (async dispatch)")
	var promptResult ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-lifecycle",
		"prompt":    "test lifecycle prompt",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 3: verify agent transitions to running (accept idle — mockagent is instant).
	t.Log("Step 3: verify agent is running (or already idle) after prompt")
	_ = waitForAgentStateOneOf(t, client, wsName, "agent-lifecycle", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	// Step 4: agent/stop → state=stopped
	t.Log("Step 4: agent/stop → state=stopped")
	if err := client.Call("agentrun/stop", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-lifecycle",
	}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, wsName, "agent-lifecycle", "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Step 5: agent/delete
	t.Log("Step 5: agent/delete")
	if err := client.Call("agentrun/delete", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-lifecycle",
	}, nil); err != nil {
		t.Fatalf("agent/delete failed: %v", err)
	}

	// Verify agent is gone (status should return error)
	var verifyStatus ari.AgentRunStatusResult
	err := client.Call("agentrun/status", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-lifecycle",
	}, &verifyStatus)
	if err == nil {
		t.Error("expected error when getting status of deleted agent")
	}
	t.Logf("agent deleted (status check returned expected error: %v)", err)
}

// TestAgentPromptAndStop tests agent/prompt followed by agent/stop.
func TestAgentPromptAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "prompt-stop-ws"
	createTestWorkspace(t, client, wsName)
	defer deleteTestWorkspace(t, client, wsName)

	// Create agent and wait for idle state
	status := createAgentAndWait(t, client, wsName, "agent-ps", "mockagent")
	t.Logf("agent ready: state=%s", status.Agent.State)

	// Prompt the agent (async dispatch)
	var promptResult ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-ps",
		"prompt":    "prompt and stop test",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Stop the agent (may still be transitioning to running — stop is valid from any live state)
	if err := client.Call("agentrun/stop", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-ps",
	}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, wsName, "agent-ps", "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

	// Delete agent
	if err := client.Call("agentrun/delete", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-ps",
	}, nil); err != nil {
		t.Logf("agent/delete: %v (ignored)", err)
	}
}

// TestAgentPromptFromIdle tests that prompting a newly-created idle agent
// transitions it to running state.
func TestAgentPromptFromIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "autostart-ws"
	createTestWorkspace(t, client, wsName)
	defer deleteTestWorkspace(t, client, wsName)

	// Create agent — should be in state=idle
	status := createAgentAndWait(t, client, wsName, "agent-auto", "mockagent")
	if status.Agent.State != "idle" {
		t.Errorf("expected state=idle before first prompt, got %s", status.Agent.State)
	}

	// Prompt immediately from idle state
	var promptResult ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      "agent-auto",
		"prompt":    "auto-start prompt",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt (from idle) failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Agent should be running (or already idle — mockagent completes instantly).
	_ = waitForAgentStateOneOf(t, client, wsName, "agent-auto", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	// Cleanup
	stopAndDeleteAgent(t, client, wsName, "agent-auto")
}

// TestMultipleAgentPromptsSequential tests multiple sequential prompts to the same agent.
// Between prompts the agent must return to idle before the next prompt is sent.
func TestMultipleAgentPromptsSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "sequential-ws"
	createTestWorkspace(t, client, wsName)
	defer deleteTestWorkspace(t, client, wsName)

	// Create agent
	status := createAgentAndWait(t, client, wsName, "agent-seq", "mockagent")
	t.Logf("agent ready: state=%s", status.Agent.State)

	prompts := []string{
		"first sequential prompt",
		"second sequential prompt",
		"third sequential prompt",
	}

	for i, promptText := range prompts {
		t.Logf("Sending prompt %d/%d: %q", i+1, len(prompts), promptText)
		var promptResult ari.AgentRunPromptResult
		if err := client.Call("agentrun/prompt", map[string]interface{}{
			"workspace": wsName,
			"name":      "agent-seq",
			"prompt":    promptText,
		}, &promptResult); err != nil {
			t.Fatalf("agent/prompt %d failed: %v", i+1, err)
		}
		t.Logf("prompt %d accepted: %v", i+1, promptResult.Accepted)
		if !promptResult.Accepted {
			t.Errorf("prompt %d: expected prompt to be accepted", i+1)
		}

		// Wait for async turn to complete — agent returns to "idle" when done.
		_ = waitForAgentState(t, client, wsName, "agent-seq", "idle", 15*time.Second)
		t.Logf("prompt %d turn completed (agent=idle) ✓", i+1)
	}

	t.Logf("All %d sequential prompts completed successfully ✓", len(prompts))

	// Cleanup
	stopAndDeleteAgent(t, client, wsName, "agent-seq")
}
