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

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// setupMassTestWithRuntimeClass creates a temporary mass instance and registers
// a custom runtime via agent/create. Uses --root flag (no config.yaml) and self-fork
// shim (no MASS_SHIM_BINARY).
func setupMassTestWithRuntimeClass(
	t *testing.T,
	runtimeClassName string,
	spec pkgariapi.AgentSpec,
) (context.Context, context.CancelFunc, pkgariapi.Client, func()) {
	t.Helper()

	// Use a short root path under /tmp to avoid macOS 104-char Unix socket path limit (K025).
	counter := atomic.AddInt64(&testSocketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/mass-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	os.Remove(socketPath)

	massBin, err := filepath.Abs("../../bin/mass")
	if err != nil {
		t.Fatalf("failed to get mass path: %v", err)
	}

	if _, err := os.Stat(massBin); os.IsNotExist(err) {
		t.Fatalf("binary not found: %s (run: make build)", massBin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)

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
		massCmd.Process.Kill()
		t.Fatalf("mass socket not ready: %v", err)
	}

	client, err := ariclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		massCmd.Process.Kill()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	// Register the agent template via agent/create.
	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: runtimeClassName},
		Spec:     spec,
	}
	if err := client.Create(ctx, &ag); err != nil {
		cancel()
		client.Close()
		massCmd.Process.Kill()
		t.Fatalf("failed to register runtime %q: %v", runtimeClassName, err)
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
		// Kill leftover agent processes from real CLI runtimes
		exec.Command("pkill", "-f", "pi-acp").Run()
		exec.Command("pkill", "-f", "claude-agent-acp").Run()
	}

	return ctx, cancel, client, cleanup
}

// runRealCLILifecycle exercises the full ARI agent lifecycle against a real
// CLI runtime: workspace/create → agentrun/create → agentrun/prompt → poll idle →
// agentrun/get → agentrun/stop → agentrun/delete → workspace/delete.
func runRealCLILifecycle(t *testing.T, ctx context.Context, client pkgariapi.Client, runtimeClass string) {
	t.Helper()

	wsName := fmt.Sprintf("test-%s", runtimeClass)
	agentName := "agent-cli"

	// Step 1: workspace/create → poll until ready
	t.Log("Step 1: workspace/create → poll until ready")
	createTestWorkspace(t, ctx, client, wsName)
	t.Logf("workspace ready: name=%s", wsName)

	// Step 2: agentrun/create → poll until idle
	t.Log("Step 2: agentrun/create → poll until idle")
	ar := createAgentAndWait(t, ctx, client, wsName, agentName, runtimeClass)
	t.Logf("agent ready: workspace=%s name=%s state=%s",
		wsName, agentName, ar.Status.State)
	if ar.Status.State != "idle" {
		t.Fatalf("expected state=idle, got %s", ar.Status.State)
	}

	// Step 3: agentrun/prompt — async dispatch; agent startup may take 10-30s for real CLIs
	t.Log("Step 3: agentrun/prompt (async — triggers agent startup, may take 10-30s)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, "respond with only the word hello")
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt dispatched: accepted=%v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Fatalf("expected prompt to be accepted")
	}

	// Step 4: poll until idle (turn completed) — real LLM calls may take 30-60s
	t.Log("Step 4: poll agentrun/get until state=idle (turn completed)")
	_ = waitForAgentState(t, ctx, client, wsName, agentName, "idle", 90*time.Second)
	t.Logf("turn completed: workspace=%s name=%s", wsName, agentName)

	// Step 5: agentrun/get — verify shim info is populated
	t.Log("Step 5: agentrun/get")
	var getAR pkgariapi.AgentRun
	if err := client.Get(ctx, key, &getAR); err != nil {
		t.Fatalf("agentrun/get failed: %v", err)
	}
	t.Logf("agent status: state=%s", getAR.Status.State)
	if getAR.Status.Shim != nil {
		t.Logf("shimState: status=%s pid=%d", getAR.Status.Shim.Status, getAR.Status.Shim.PID)
	}

	// Step 6: agentrun/stop → poll until stopped
	t.Log("Step 6: agentrun/stop → poll until stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = waitForAgentState(t, ctx, client, wsName, agentName, "stopped", 15*time.Second)
	t.Log("agent stopped ✓")

	// Step 7: agentrun/delete
	t.Log("Step 7: agentrun/delete")
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Fatalf("agentrun/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	// Step 8: workspace/delete
	t.Log("Step 8: workspace/delete")
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: wsName}, &pkgariapi.Workspace{}); err != nil {
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

	ctx, cancel, client, cleanup := setupMassTestWithRuntimeClass(t, "gsd-pi", pkgariapi.AgentSpec{
		Command: "bunx",
		Args:    []string{"pi-acp"},
		Env: []apiruntime.EnvVar{
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

	ctx, cancel, client, cleanup := setupMassTestWithRuntimeClass(t, "claude-code", pkgariapi.AgentSpec{
		Command: "node",
		Args:    []string{adapterPath},
		Env: []apiruntime.EnvVar{
			{Name: "ANTHROPIC_API_KEY", Value: apiKey},
		},
	})
	defer cleanup()
	defer cancel()

	runRealCLILifecycle(t, ctx, client, "claude-code")
}
