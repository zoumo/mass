package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// runRealCLILifecycle exercises the full ARI agent lifecycle against a real
// CLI runtime.
func runRealCLILifecycle(t *testing.T, ctx context.Context, client pkgariapi.Client, runtimeClass string) {
	t.Helper()

	wsName := fmt.Sprintf("test-%s", runtimeClass)
	agentName := "agent-cli"

	t.Log("Step 1: workspace/create → poll until ready")
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	t.Logf("workspace ready: name=%s", wsName)

	t.Log("Step 2: agentrun/create → poll until idle")
	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, agentName, runtimeClass)
	t.Logf("agent ready: workspace=%s name=%s state=%s",
		wsName, agentName, ar.Status.Status)
	if ar.Status.Status != "idle" {
		t.Fatalf("expected state=idle, got %s", ar.Status.Status)
	}

	t.Log("Step 3: agentrun/prompt (async — triggers agent startup, may take 10-30s)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, []runapi.ContentBlock{runapi.TextBlock("respond with only the word hello")})
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt dispatched: accepted=%v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Fatalf("expected prompt to be accepted")
	}

	t.Log("Step 4: poll agentrun/get until state=idle (turn completed)")
	_ = testutil.WaitForAgentState(t, ctx, client, wsName, agentName, "idle", 90*time.Second)
	t.Logf("turn completed: workspace=%s name=%s", wsName, agentName)

	t.Log("Step 5: agentrun/get")
	var getAR pkgariapi.AgentRun
	if err := client.Get(ctx, key, &getAR); err != nil {
		t.Fatalf("agentrun/get failed: %v", err)
	}
	t.Logf("agent status: state=%s", getAR.Status.Status)

	t.Log("Step 6: agentrun/stop → poll until stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = testutil.WaitForAgentState(t, ctx, client, wsName, agentName, "stopped", 15*time.Second)
	t.Log("agent stopped ✓")

	t.Log("Step 7: agentrun/delete")
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Fatalf("agentrun/delete failed: %v", err)
	}
	t.Log("agent deleted ✓")

	t.Log("Step 8: workspace/delete")
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: wsName}, &pkgariapi.Workspace{}); err != nil {
		t.Logf("warning: workspace/delete failed: %v", err)
	}
	t.Log("workspace deleted — lifecycle complete ✓")
}

// TestRealCLI_GsdPi exercises the full agent lifecycle with the real gsd-pi CLI.
func TestRealCLI_GsdPi(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real CLI test in short mode")
	}

	if _, err := exec.LookPath("bunx"); err != nil {
		t.Skip("skipping: bunx not found in PATH")
	}
	if _, err := exec.LookPath("gsd"); err != nil {
		t.Skip("skipping: gsd not found in PATH (required by pi-acp)")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("skipping: ANTHROPIC_API_KEY not set (gsd-pi needs an LLM key to process prompts)")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTestWithRuntimeClass(t, "gsd-pi", pkgariapi.AgentSpec{
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
// claude-code ACP adapter.
func TestRealCLI_ClaudeCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real CLI test in short mode")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("skipping: ANTHROPIC_API_KEY not set")
	}

	adapterPath := "/Users/jim/.bun/install/cache/@GH@agentclientprotocol-claude-agent-acp-7506223@@@1/dist/index.js"
	if _, err := os.Stat(adapterPath); os.IsNotExist(err) {
		t.Skipf("skipping: claude-code adapter not found at %s", adapterPath)
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTestWithRuntimeClass(t, "claude-code", pkgariapi.AgentSpec{
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
