package mass_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// TestMultipleConcurrentAgents tests that multiple agents can run concurrently
// without interference.
func TestMultipleConcurrentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := testutil.SetupMassTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "concurrent-ws"
	const numAgents = 3

	testutil.CreateTestWorkspace(t, ctx, client, wsName)
	defer testutil.DeleteTestWorkspace(t, ctx, client, wsName)

	agentNames := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent-agent-%d", i+1)
		agentNames[i] = name
		_ = testutil.CreateAgentAndWait(t, ctx, client, wsName, name, "mockagent")
		t.Logf("created agent %d: workspace=%s name=%s", i+1, wsName, name)
	}

	var clientMu sync.Mutex

	t.Log("Prompting agents concurrently...")
	results := make(chan error, numAgents)
	var wg sync.WaitGroup

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(idx int, agentName string) {
			defer wg.Done()

			key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
			clientMu.Lock()
			promptResult, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock(fmt.Sprintf("concurrent prompt %d", idx+1))})
			clientMu.Unlock()

			if err != nil {
				results <- fmt.Errorf("agent %d (%s) prompt failed: %w", idx+1, agentName, err)
				return
			}

			if !promptResult.Accepted {
				results <- fmt.Errorf("agent %d (%s): expected prompt to be accepted", idx+1, agentName)
				return
			}

			t.Logf("agent %d (%s) prompt accepted", idx+1, agentName)
			results <- nil
		}(i, agentNames[i])
	}

	wg.Wait()
	close(results)

	errorCount := 0
	for err := range results {
		if err != nil {
			t.Error(err)
			errorCount++
		}
	}
	if errorCount > 0 {
		t.Fatalf("%d/%d agents had prompt errors", errorCount, numAgents)
	}

	t.Logf("All %d agents accepted prompts successfully ✓", numAgents)

	t.Log("Verifying all agents are running...")
	for i, name := range agentNames {
		key := pkgariapi.ObjectKey{Workspace: wsName, Name: name}
		var ar pkgariapi.AgentRun
		clientMu.Lock()
		err := client.Get(ctx, key, &ar)
		clientMu.Unlock()
		if err != nil {
			t.Errorf("agent %d (%s) get failed: %v", i+1, name, err)
			continue
		}
		if ar.Status.State != "running" && ar.Status.State != "idle" {
			t.Errorf("agent %d (%s): expected state=running or idle, got %s", i+1, name, ar.Status.State)
		}
	}

	// Stop all agents in parallel to avoid sequential 10s timeouts exhausting
	// the 60s context under heavy CPU load from parallel test packages.
	t.Log("Stopping and deleting all agents...")
	var stopWg sync.WaitGroup
	for i, name := range agentNames {
		stopWg.Add(1)
		go func(idx int, agentName string) {
			defer stopWg.Done()
			key := pkgariapi.ObjectKey{Workspace: wsName, Name: agentName}
			clientMu.Lock()
			stopErr := client.AgentRuns().Stop(ctx, key)
			clientMu.Unlock()
			if stopErr != nil {
				t.Logf("warning: agent %d (%s) stop failed: %v", idx+1, agentName, stopErr)
			}

			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				var ar pkgariapi.AgentRun
				clientMu.Lock()
				err := client.Get(ctx, key, &ar)
				clientMu.Unlock()
				if err != nil {
					break
				}
				if ar.Status.State == "stopped" || ar.Status.State == "error" {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}

			clientMu.Lock()
			delErr := client.Delete(ctx, key, &pkgariapi.AgentRun{})
			clientMu.Unlock()
			if delErr != nil {
				t.Logf("warning: agent %d (%s) delete failed: %v", idx+1, agentName, delErr)
			}
		}(i, name)
	}
	stopWg.Wait()

	t.Log("Multiple concurrent agents test completed successfully! ✓")
}
