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

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// setupAgentdTestWithRuntimeClass creates a temporary agentd instance with a
// custom runtime class configuration. Accepts an arbitrary runtime class name
// and its YAML body (indented for embedding under runtimeClasses:).
func setupAgentdTestWithRuntimeClass(
	t *testing.T,
	runtimeClassName, runtimeClassYAML string,
) (context.Context, context.CancelFunc, *ari.Client, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	// Use short socket path in /tmp to avoid macOS 104-char Unix socket path limit (K025)
	counter := atomic.AddInt64(&testSocketCounter, 1)
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), counter)
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDB := filepath.Join(tmpDir, "agentd.db")
	bundleRoot := filepath.Join(tmpDir, "bundles")

	os.Remove(socketPath)

	for _, dir := range []string{workspaceRoot, bundleRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create directory %s: %v", dir, err)
		}
	}

	agentdBin, err := filepath.Abs("../../bin/agentd")
	if err != nil {
		t.Fatalf("failed to get agentd path: %v", err)
	}
	agentShimBin, err := filepath.Abs("../../bin/agent-shim")
	if err != nil {
		t.Fatalf("failed to get agent-shim path: %v", err)
	}

	for _, bin := range []string{agentdBin, agentShimBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s (run: go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim)", bin)
		}
	}

	// Build config YAML with the provided runtime class
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := fmt.Sprintf(`socket: %s
workspaceRoot: %s
metaDB: %s
bundleRoot: %s
runtimeClasses:
  %s:
%s
`, socketPath, workspaceRoot, metaDB, bundleRoot, runtimeClassName, runtimeClassYAML)

	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	t.Logf("config written to %s", configPath)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)

	agentdCmd := exec.CommandContext(ctx, agentdBin, "--config", configPath)
	agentdCmd.Stdout = os.Stdout
	agentdCmd.Stderr = os.Stderr
	agentdCmd.Env = append(os.Environ(), fmt.Sprintf("OAR_SHIM_BINARY=%s", agentShimBin))

	if err := agentdCmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", agentdCmd.Process.Pid)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		agentdCmd.Process.Kill()
		t.Fatalf("agentd socket not ready: %v", err)
	}

	client, err := ari.NewClient(socketPath)
	if err != nil {
		cancel()
		agentdCmd.Process.Kill()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	cleanup := func() {
		client.Close()
		if agentdCmd.Process != nil {
			agentdCmd.Process.Signal(os.Interrupt)
			agentdCmd.Wait()
			t.Log("agentd stopped")
		}
		os.Remove(socketPath)
		// Kill leftover agent processes from real CLI runtimes
		exec.Command("pkill", "-f", "agent-shim").Run()
		exec.Command("pkill", "-f", "pi-acp").Run()
		exec.Command("pkill", "-f", "claude-agent-acp").Run()
	}

	return ctx, cancel, client, cleanup
}

// runRealCLILifecycle exercises the full ARI agent lifecycle against a real
// CLI runtime: workspace/create → agent/create → agent/prompt → poll idle →
// agent/status → agent/stop → agent/delete → workspace/delete.
func runRealCLILifecycle(t *testing.T, _ context.Context, client *ari.Client, runtimeClass string) {
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
		wsName, agentName, createResult.Agent.State)
	if createResult.Agent.State != "idle" {
		t.Fatalf("expected state=idle, got %s", createResult.Agent.State)
	}

	// Step 3: agent/prompt — async dispatch; agent startup may take 10-30s for real CLIs
	t.Log("Step 3: agent/prompt (async — triggers agent startup, may take 10-30s)")
	var promptResult ari.AgentPromptResult
	if err := client.Call("agent/prompt", map[string]interface{}{
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
		wsName, agentName, statusAfterTurn.Agent.State)

	// Step 5: agent/status — verify shimState is non-nil (shim still running)
	t.Log("Step 5: agent/status")
	var statusResult ari.AgentStatusResult
	if err := client.Call("agent/status", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, &statusResult); err != nil {
		t.Fatalf("agent/status failed: %v", err)
	}
	t.Logf("agent status: state=%s", statusResult.Agent.State)
	if statusResult.ShimState != nil {
		t.Logf("shimState: status=%s pid=%d", statusResult.ShimState.Status, statusResult.ShimState.PID)
	}

	// Step 6: agent/stop → poll until stopped
	t.Log("Step 6: agent/stop → poll until stopped")
	if err := client.Call("agent/stop", map[string]interface{}{
		"workspace": wsName,
		"name":      agentName,
	}, nil); err != nil {
		t.Fatalf("agent/stop failed: %v", err)
	}
	_ = waitForAgentState(t, client, wsName, agentName, "stopped", 15*time.Second)
	t.Log("agent stopped ✓")

	// Step 7: agent/delete
	t.Log("Step 7: agent/delete")
	if err := client.Call("agent/delete", map[string]interface{}{
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

	runtimeClassYAML := `    command: bunx
    args:
      - "pi-acp"
    env:
      PI_ACP_PI_COMMAND: gsd
      PI_CODING_AGENT_DIR: /Users/jim/.gsd/agent`

	ctx, cancel, client, cleanup := setupAgentdTestWithRuntimeClass(t, "gsd-pi", runtimeClassYAML)
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

	runtimeClassYAML := fmt.Sprintf(`    command: node
    args:
      - "%s"
    env:
      ANTHROPIC_API_KEY: "%s"`, adapterPath, apiKey)

	ctx, cancel, client, cleanup := setupAgentdTestWithRuntimeClass(t, "claude-code", runtimeClassYAML)
	defer cleanup()
	defer cancel()

	runRealCLILifecycle(t, ctx, client, "claude-code")
}
