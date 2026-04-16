package mass_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// TestAgentdRestartRecovery proves that agent identity (workspace+name) survives
// daemon restart and that dead agent-runs are fail-closed to "error" state.
func TestAgentdRestartRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	counter := testutil.NewSocketCounter()
	rootDir := fmt.Sprintf("/tmp/mass-restart-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	massBin := testutil.MassBinPath(t)
	mockagentBin := testutil.MockagentBinPath(t)

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

	massCmd1 := testutil.StartMass(t, ctx1, massBin, rootDir, socketPath)

	client1, err := ariclient.Dial(ctx1, socketPath)
	if err != nil {
		t.Fatalf("ARI client: %v", err)
	}

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	if err := client1.Create(ctx1, &ag); err != nil {
		t.Fatalf("agent/create (phase 1): %v", err)
	}
	t.Logf("runtime registered (phase 1): name=%s", ag.Metadata.Name)

	const wsName = "test-ws"
	testutil.CreateTestWorkspace(t, ctx1, client1, wsName)
	t.Logf("workspace ready: %s", wsName)

	t.Log("Creating agent-A")
	arA1 := testutil.CreateAgentAndWait(t, ctx1, client1, wsName, "agent-a", "mockagent")
	t.Logf("agent-A: workspace=%s name=%s state=%s",
		arA1.Metadata.Workspace, arA1.Metadata.Name, arA1.Status.State)

	if arA1.Status.State != "idle" {
		t.Fatalf("expected agent-A state=idle, got %s", arA1.Status.State)
	}

	t.Log("Creating agent-B")
	arB1 := testutil.CreateAgentAndWait(t, ctx1, client1, wsName, "agent-b", "mockagent")
	t.Logf("agent-B: workspace=%s name=%s state=%s",
		arB1.Metadata.Workspace, arB1.Metadata.Name, arB1.Status.State)

	t.Log("Prompting agent-A before restart")
	keyA := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-a"}
	promptResultA, err := client1.AgentRuns().Prompt(ctx1, keyA, []pkgariapi.ContentBlock{pkgariapi.TextBlock("hello before restart")})
	if err != nil {
		t.Fatalf("agentrun/prompt A: %v", err)
	}
	t.Logf("agent-A prompt accepted: %v", promptResultA.Accepted)

	t.Log("Prompting agent-B before restart")
	keyB := pkgariapi.ObjectKey{Workspace: wsName, Name: "agent-b"}
	promptResultB, err := client1.AgentRuns().Prompt(ctx1, keyB, []pkgariapi.ContentBlock{pkgariapi.TextBlock("hello before restart")})
	if err != nil {
		t.Fatalf("agentrun/prompt B: %v", err)
	}
	t.Logf("agent-B prompt accepted: %v", promptResultB.Accepted)

	_ = testutil.WaitForAgentStateOneOf(t, ctx1, client1, wsName, "agent-a", []string{"running", "idle"}, 10*time.Second)
	t.Log("agent-A is in running/idle state after prompt ✓")

	// =========================================================================
	// Phase 2: Stop mass, kill ALL agent-run and runtime processes
	// =========================================================================
	t.Log("Phase 2: Stop mass and kill all agent-run + mockagent processes")

	client1.Close()
	testutil.StopMass(t, massCmd1, socketPath)

	exec.Command("pkill", "-9", "-f", "agent-run").Run()
	exec.Command("pkill", "-9", "-f", "mockagent").Run()
	t.Log("killed all agent-run and mockagent processes")

	time.Sleep(500 * time.Millisecond)

	// =========================================================================
	// Phase 3: Restart mass with same config+metaDB
	// =========================================================================
	t.Log("Phase 3: Restart mass with same config — recovery pass should mark both agents error")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	massCmd2 := testutil.StartMass(t, ctx2, massBin, rootDir, socketPath)
	defer testutil.StopMass(t, massCmd2, socketPath)

	client2, err := ariclient.Dial(ctx2, socketPath)
	if err != nil {
		t.Fatalf("ARI client after restart: %v", err)
	}
	defer client2.Close()

	ag2 := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	if err := client2.Update(ctx2, &ag2); err != nil {
		if err2 := client2.Create(ctx2, &ag2); err2 != nil {
			t.Fatalf("agent register (phase 3): create=%v update=%v", err2, err)
		}
	}
	t.Logf("runtime registered (phase 3): name=%s", ag2.Metadata.Name)

	t.Log("Waiting for recovery pass to complete...")
	time.Sleep(2 * time.Second)

	// =========================================================================
	// Phase 4: Verify agent-A identity is preserved across restart
	// =========================================================================
	t.Log("Phase 4: Verify agent-A identity preserved across restart")

	terminalStates := []string{"stopped", "error"}
	arA2 := testutil.WaitForAgentStateOneOf(t, ctx2, client2, wsName, "agent-a", terminalStates, 10*time.Second)

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

	t.Logf("agent-A post-restart state=%s (agent-run killed → fail-closed, identity preserved)",
		arA2.Status.State)

	// =========================================================================
	// Phase 5: Verify agent-B is in a terminal state
	// =========================================================================
	t.Log("Phase 5: Verify agent-B is in terminal state (dead agent-run fail-closed)")

	arB2 := testutil.WaitForAgentStateOneOf(t, ctx2, client2, wsName, "agent-b", terminalStates, 10*time.Second)
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

	agentStates := make(map[string]string)
	for _, a := range listResult.Items {
		agentStates[a.Metadata.Name] = string(a.Status.State)
		t.Logf("  agent: workspace=%s name=%s state=%s", a.Metadata.Workspace, a.Metadata.Name, a.Status.State)
	}
	for _, aName := range []string{"agent-a", "agent-b"} {
		st := agentStates[aName]
		if st != "stopped" && st != "error" {
			t.Errorf("%s: expected state=stopped or error after recovery, got %q", aName, st)
		}
	}

	// =========================================================================
	// Phase 7: Cleanup
	// =========================================================================
	t.Log("Phase 7: Cleanup")

	for _, agentName := range []string{"agent-a", "agent-b"} {
		key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
		if err := client2.AgentRuns().Stop(ctx2, key); err != nil {
			t.Logf("agentrun/stop %s: %v (may already be stopped)", agentName, err)
		}
		if err := client2.Delete(ctx2, key, &pkgariapi.AgentRun{}); err != nil {
			t.Logf("agentrun/delete %s: %v (ignored)", agentName, err)
		}
	}

	if err := client2.Delete(ctx2, pkgariapi.ObjectKey{Name: wsName}, &pkgariapi.Workspace{}); err != nil {
		t.Logf("workspace/delete: %v (ignored)", err)
	}

	t.Log("TestAgentdRestartRecovery completed ✓")
}
