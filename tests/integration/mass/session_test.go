package mass_test

import (
	"testing"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// TestAgentLifecycle tests all agent state transitions.
// Covers: agentrun/create → state=idle → agentrun/prompt → state=running → agentrun/stop → state=stopped → agentrun/delete
func TestAgentLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "lifecycle-ws"
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	defer testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	// Step 1: agentrun/create → state=idle
	t.Log("Step 1: agentrun/create → wait for state=idle")
	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, "agent-lifecycle", "mockagent")
	t.Logf("agent ready: workspace=%s name=%s state=%s", wsName, "agent-lifecycle", ar.Status.Status)

	if ar.Status.Status != "idle" {
		t.Errorf("expected state=idle, got %s", ar.Status.Status)
	}

	// Step 2: agentrun/prompt → async dispatch; state transitions to running
	t.Log("Step 2: agentrun/prompt (async dispatch)")
	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-lifecycle"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock("test lifecycle prompt")})
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	// Step 3: verify agent transitions to running (accept idle — mockagent is instant).
	t.Log("Step 3: verify agent is running (or already idle) after prompt")
	_ = testutil.WaitForAgentStateOneOf(t, ctx, client, wsName, "agent-lifecycle", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	// Step 4: agentrun/stop → state=stopped
	t.Log("Step 4: agentrun/stop → state=stopped")
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = testutil.WaitForAgentState(t, ctx, client, wsName, "agent-lifecycle", "stopped", 10*time.Second)
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

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "prompt-stop-ws"
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	defer testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, "agent-ps", "mockagent")
	t.Logf("agent ready: state=%s", ar.Status.Status)

	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-ps"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock("prompt and stop test")})
	if err != nil {
		t.Fatalf("agentrun/prompt failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Fatalf("agentrun/stop failed: %v", err)
	}
	_ = testutil.WaitForAgentState(t, ctx, client, wsName, "agent-ps", "stopped", 10*time.Second)
	t.Log("agent stopped ✓")

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

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "autostart-ws"
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	defer testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, "agent-auto", "mockagent")
	if ar.Status.Status != "idle" {
		t.Errorf("expected state=idle before first prompt, got %s", ar.Status.Status)
	}

	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-auto"}
	promptResult, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock("auto-start prompt")})
	if err != nil {
		t.Fatalf("agentrun/prompt (from idle) failed: %v", err)
	}
	t.Logf("prompt accepted: %v", promptResult.Accepted)
	if !promptResult.Accepted {
		t.Errorf("expected prompt to be accepted")
	}

	_ = testutil.WaitForAgentStateOneOf(t, ctx, client, wsName, "agent-auto", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent running/idle ✓")

	testutil.StopAndDeleteAgent(t, ctx, client, wsName, "agent-auto")
}

// TestMultipleAgentPromptsSequential tests multiple sequential prompts to the same agent.
func TestMultipleAgentPromptsSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "sequential-ws"
	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	defer testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	ar := testutil.CreateAgentAndWait(t, ctx, client, wsName, "agent-seq", "mockagent")
	t.Logf("agent ready: state=%s", ar.Status.Status)

	prompts := []string{
		"first sequential prompt",
		"second sequential prompt",
		"third sequential prompt",
	}

	key := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-seq"}
	for i, promptText := range prompts {
		t.Logf("Sending prompt %d/%d: %q", i+1, len(prompts), promptText)
		promptResult, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock(promptText)})
		if err != nil {
			t.Fatalf("agentrun/prompt %d failed: %v", i+1, err)
		}
		t.Logf("prompt %d accepted: %v", i+1, promptResult.Accepted)
		if !promptResult.Accepted {
			t.Errorf("prompt %d: expected prompt to be accepted", i+1)
		}

		_ = testutil.WaitForAgentState(t, ctx, client, wsName, "agent-seq", "idle", 15*time.Second)
		t.Logf("prompt %d turn completed (agent=idle) ✓", i+1)
	}

	t.Logf("All %d sequential prompts completed successfully ✓", len(prompts))

	testutil.StopAndDeleteAgent(t, ctx, client, wsName, "agent-seq")
}
