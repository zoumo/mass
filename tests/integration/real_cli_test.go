// Package integration_test provides integration tests for real CLI agent runtimes.
// These tests verify that gsd-pi and claude-code work end-to-end through the
// full ARI session lifecycle, proving R039.
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

// setupAgentdTestWithRuntimeClass creates a temporary agentd instance with a
// custom runtime class configuration. It reuses the pattern from setupAgentdTest
// in session_test.go but accepts an arbitrary runtime class name and config.
func setupAgentdTestWithRuntimeClass(t *testing.T, runtimeClassName, runtimeClassYAML string) (context.Context, context.CancelFunc, *ari.Client, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	// Use short socket path in /tmp to avoid macOS 104-char Unix socket path limit (K025)
	testSocketCounter++
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), testSocketCounter)
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

// runRealCLILifecycle exercises the full ARI session lifecycle against a real
// CLI runtime: workspace/prepare → session/new → session/prompt → session/status
// → session/stop → session/remove → workspace/cleanup.
func runRealCLILifecycle(t *testing.T, ctx context.Context, client *ari.Client, runtimeClass string) {
	t.Helper()

	// Step 1: workspace/prepare
	t.Log("Step 1: workspace/prepare")
	prepareParams := map[string]interface{}{
		"spec": map[string]interface{}{
			"oarVersion": "0.1.0",
			"metadata": map[string]interface{}{
				"name": fmt.Sprintf("test-%s", runtimeClass),
			},
			"source": map[string]interface{}{
				"type": "emptyDir",
			},
		},
	}
	var prepareResult ari.WorkspacePrepareResult
	if err := client.Call("workspace/prepare", prepareParams, &prepareResult); err != nil {
		t.Fatalf("workspace/prepare failed: %v", err)
	}
	workspaceId := prepareResult.WorkspaceId
	t.Logf("workspace prepared: id=%s", workspaceId)

	// Step 2: session/new
	t.Log("Step 2: session/new")
	sessionNewParams := map[string]interface{}{
		"workspaceId":  workspaceId,
		"runtimeClass": runtimeClass,
	}
	var sessionNewResult ari.SessionNewResult
	if err := client.Call("session/new", sessionNewParams, &sessionNewResult); err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	sessionId := sessionNewResult.SessionId
	t.Logf("session created: id=%s state=%s", sessionId, sessionNewResult.State)
	if sessionNewResult.State != "created" {
		t.Fatalf("expected state=created, got %s", sessionNewResult.State)
	}

	// Step 3: session/prompt — auto-starts the agent, sends a simple prompt
	t.Log("Step 3: session/prompt (this triggers agent startup — may take 10-30s)")
	promptParams := map[string]interface{}{
		"sessionId": sessionId,
		"text":      "respond with only the word hello",
	}
	var promptResult ari.SessionPromptResult
	if err := client.Call("session/prompt", promptParams, &promptResult); err != nil {
		t.Fatalf("session/prompt failed: %v", err)
	}
	t.Logf("prompt completed: stopReason=%s", promptResult.StopReason)

	// Assert stopReason is "end_turn"
	if promptResult.StopReason != "end_turn" {
		t.Fatalf("expected stopReason=end_turn, got %q", promptResult.StopReason)
	}

	// Step 4: session/status — verify state=running and shimState is non-nil
	t.Log("Step 4: session/status")
	statusParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var statusResult ari.SessionStatusResult
	if err := client.Call("session/status", statusParams, &statusResult); err != nil {
		t.Fatalf("session/status failed: %v", err)
	}
	t.Logf("session status: state=%s", statusResult.Session.State)

	if statusResult.Session.State != "running" {
		t.Errorf("expected state=running after prompt, got %s", statusResult.Session.State)
	}
	if statusResult.ShimState == nil {
		t.Error("expected shimState to be non-nil for running session")
	} else {
		t.Logf("shimState: status=%s pid=%d", statusResult.ShimState.Status, statusResult.ShimState.PID)
	}

	// Step 5: session/stop
	t.Log("Step 5: session/stop")
	stopParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var stopResult interface{}
	if err := client.Call("session/stop", stopParams, &stopResult); err != nil {
		t.Fatalf("session/stop failed: %v", err)
	}
	t.Log("session stopped")

	// Step 6: session/remove
	t.Log("Step 6: session/remove")
	removeParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	var removeResult interface{}
	if err := client.Call("session/remove", removeParams, &removeResult); err != nil {
		t.Fatalf("session/remove failed: %v", err)
	}
	t.Log("session removed")

	// Step 7: workspace/cleanup
	t.Log("Step 7: workspace/cleanup")
	cleanupParams := map[string]interface{}{
		"workspaceId": workspaceId,
	}
	var cleanupResult interface{}
	if err := client.Call("workspace/cleanup", cleanupParams, &cleanupResult); err != nil {
		t.Logf("warning: workspace/cleanup failed: %v", err)
	}
	t.Log("workspace cleaned up — lifecycle complete")
}

// TestRealCLI_GsdPi exercises the full session lifecycle with the real gsd-pi CLI
// (bunx pi-acp). This proves R039: converged runtime works with real ACP agents.
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
	// Prerequisite: ANTHROPIC_API_KEY must be set — gsd-pi calls an LLM to
	// process prompts; without a key the prompt hangs until timeout.
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

// TestRealCLI_ClaudeCode exercises the full session lifecycle with the real
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
