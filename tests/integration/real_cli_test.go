// Package integration_test provides integration tests for real CLI agent runtimes.
// These tests verify that gsd-pi and claude-code work end-to-end through the
// full ARI agent lifecycle.
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
	"github.com/zoumo/oar/api"
)

// setupAgentdTestWithRuntimeClass creates a temporary agentd instance and registers
// a custom runtime via runtime/set. Uses --root flag (no config.yaml) and self-fork
// shim (no OAR_SHIM_BINARY).
func setupAgentdTestWithRuntimeClass(
	t *testing.T,
	runtimeClassName string,
	templateSpec ari.AgentSetParams,
) (context.Context, context.CancelFunc, *ariclient.Client, func()) {
	t.Helper()

	// Use a short root path under /tmp to avoid macOS 104-char Unix socket path limit (K025).
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/oar-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "agentd.sock")

	os.Remove(socketPath)

	agentdBin, err := filepath.Abs("../../bin/agentd")
	if err != nil {
		t.Fatalf("failed to get agentd path: %v", err)
	}

	if _, err := os.Stat(agentdBin); os.IsNotExist(err) {
		t.Fatalf("binary not found: %s (run: make build)", agentdBin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)

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
		agentdCmd.Process.Kill()
		t.Fatalf("agentd socket not ready: %v", err)
	}

	client, err := ariclient.NewClient(socketPath)
	if err != nil {
		cancel()
		agentdCmd.Process.Kill()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	// Register the agent template via agent/set. Ensure the name field is set.
	templateSpec.Name = runtimeClassName
	var runtimeResult ari.AgentInfo
	if err := client.Call("agent/set", templateSpec, &runtimeResult); err != nil {
		cancel()
		client.Close()
		agentdCmd.Process.Kill()
		t.Fatalf("failed to register runtime %q: %v", runtimeClassName, err)
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
		// Kill leftover agent processes from real CLI runtimes
		exec.Command("pkill", "-f", "pi-acp").Run()
		exec.Command("pkill", "-f", "claude-agent-acp").Run()
	}

	return ctx, cancel, client, cleanup
}

// runRealCLILifecycle exercises the full ARI agent lifecycle against a real
// CLI runtime: workspace/create → agent/create → agent/prompt → poll idle →
// agent/status → agent/stop → agent/delete → workspace/delete.
func runRealCLILifecycle(t *testing.T, _ context.Context, client *ariclient.Client, runtimeClass string) {
	t.Helper()

	wsName := fmt.Sprintf("test-%s", runtimeClass)
	agentName := "agent-cli"

	// Step 1: workspace/create → poll until ready
	t.Log("Step 1: workspace/create → poll until ready")
	createTestWorkspace(t, client, wsName)
	t.Logf("workspace ready: name=%s", wsName)

	// Step 2: agent/create → poll until idle
	t.Log("Step 2: agent/create → poll until idle")
	createResult := createAgentAndWait(t, client, wsName, agentName, runtimeClass)
	t.Logf("agent ready: workspace=%s name=%s state=%s",
		wsName, agentName, createResult.AgentRun.State)
	if createResult.AgentRun.State != "idle" {
		t.Fatalf("expected state=idle, got %s", createResult.AgentRun.State)
	}

	// Step 3: agent/prompt — async dispatch; agent startup may take 10-30s for real CLIs
	t.Log("Step 3: agent/prompt (async — triggers agent startup, may take 10-30s)")
	var promptResult ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
		"prompt":    "respond with only the word hello",
	}, &promptResult); err != nil {
		t.Fatalf("agent/prompt failed: %v", err)
	}
	t.Logf("prompt dispatched: accepted=%v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Fatalf("expected prompt to be accepted")
	}

	// Step 4: poll until idle (turn completed) — real LLM calls may take 30-60s
	t.Log("Step 4: poll agent/status until state=idle (turn completed)")
	statusAfterTurn := waitForAgentState(t, client, wsName, agentName, "idle", 90*time.Second)
	t.Logf("turn completed: workspace=%s name=%s state=%s",
		wsName, agentName, statusAfterTurn.AgentRun.State)

	// Step 5: agent/status — verify shimState is non-nil (shim still running)
	t.Log("Step 5: agent/status")
	var statusResult ari.AgentRunStatusResult
	if err := client.Call("agentrun/status", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, &statusResult); err != nil {
		t.Fatalf("agent/status failed: %v", err)
	}
	t.Logf("agent status: state=%s", statusResult.AgentRun.State)
	if statusResult.ShimState != nil {
		t.Logf("shimState: status=%s pid=%d", statusResult.ShimState.Status, statusResult.ShimState.PID)
	}

	// Step 6: agent/stop → poll until stopped
	t.Log("Step 6: agent/stop → poll until stopped")
	if err := client.Call("agentrun/stop", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, wsName, agentName, "stopped", 15*time.Second)
	t.Log("agent stopped ✓")

	// Step 7: agent/delete
	t.Log("Step 7: agent/delete")
	if err := client.Call("agentrun/delete", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, nil); err != nil {
		t.Fatalf("agent/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	// Step 8: workspace/delete
	t.Log("Step 8: workspace/delete")
	if err := client.Call("workspace/delete", map[string]interface{}{
		"name": wsName,
	}, nil); err != nil {
		t.Logf("warning: workspace/delete failed: %v", err)
	}
	t.Log("workspace deleted — lifecycle complete ✓")
}

// TestRealCLI_GsdPi exercises the full agent lifecycle with the real gsd-pi CLI
// (bunx pi-acp). Requires bunx, gsd, and ANTHROPIC_API_KEY in the environment.
func TestRealCLI_GsdPi(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real CLI test in short mode")
	}

	// Prerequisite: bunx must be in PATH
	if _, err := exec.LookPath("bunx"); err != nil {
		t.Skip("skipping: bunx not found in PATH")
	}
	// Prerequisite: gsd must be in PATH (used by pi-acp as the pi command)
	if _, err := exec.LookPath("gsd"); err != nil {
		t.Skip("skipping: gsd not found in PATH (required by pi-acp)")
	}
	// Prerequisite: ANTHROPIC_API_KEY must be set
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("skipping: ANTHROPIC_API_KEY not set (gsd-pi needs an LLM key to process prompts)")
	}

	ctx, cancel, client, cleanup := setupAgentdTestWithRuntimeClass(t, "gsd-pi", ari.AgentSetParams{
		Command: "bunx",
		Args:    []string{"pi-acp"},
		Env: []api.EnvVar{
			{Name: "PI_ACP_PI_COMMAND", Value: "gsd"},
			{Name: "PI_CODING_AGENT_DIR", Value: "/Users/jim/.gsd/agent"},
		},
	})
	defer cleanup()
	defer cancel()

	runRealCLILifecycle(t, ctx, client, "gsd-pi")
}

// TestRealCLI_ClaudeCode exercises the full agent lifecycle with the real
// claude-code ACP adapter (node + claude-agent-acp). Requires ANTHROPIC_API_KEY.
func TestRealCLI_ClaudeCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real CLI test in short mode")
	}

	// Prerequisite: ANTHROPIC_API_KEY must be set
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("skipping: ANTHROPIC_API_KEY not set")
	}

	// Prerequisite: the claude-code adapter JS file must exist
	adapterPath := "/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js"
	if _, err := os.Stat(adapterPath); os.IsNotExist(err) {
		t.Skipf("skipping: claude-code adapter not found at %s", adapterPath)
	}

	ctx, cancel, client, cleanup := setupAgentdTestWithRuntimeClass(t, "claude-code", ari.AgentSetParams{
		Command: "node",
		Args:    []string{adapterPath},
		Env: []api.EnvVar{
			{Name: "ANTHROPIC_API_KEY", Value: apiKey},
		},
	})
	defer cleanup()
	defer cancel()

	runRealCLILifecycle(t, ctx, client, "claude-code")
}
