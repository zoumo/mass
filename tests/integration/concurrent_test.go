// Package integration_test provides integration tests for concurrent agent management.
// These tests verify that multiple agents can run concurrently without interference.
package integration_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	ari "github.com/open-agent-d/open-agent-d/api/ari"
	ariclient "github.com/open-agent-d/open-agent-d/pkg/ari"
)

// TestMultipleConcurrentAgents tests that multiple agents can run concurrently
// without interference. Each agent should respond independently.
// Note: ari.Client is NOT thread-safe — a sync.Mutex serializes all client calls.
func TestMultipleConcurrentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	const wsName = "concurrent-ws"
	const numAgents = 3

	// Create shared workspace
	createTestWorkspace(t, client, wsName)
	defer deleteTestWorkspace(t, client, wsName)

	// Create agents and collect their names; all belong to the same workspace
	agentNames := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent-agent-%d", i+1)
		agentNames[i] = name
		_ = createAgentAndWait(t, client, wsName, name, "mockagent")
		t.Logf("created agent %d: workspace=%s name=%s", i+1, wsName, name)
	}

	// Mutex for serializing client calls (ari.Client is not thread-safe)
	var clientMu sync.Mutex

	// Prompt agents concurrently (serialized ARI calls via mutex)
	t.Log("Prompting agents concurrently...")
	results := make(chan error, numAgents)
	var wg sync.WaitGroup

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(idx int, agentName string) {
			defer wg.Done()

			var promptResult ari.AgentRunPromptResult
			clientMu.Lock()
			err := client.Call("agentrun/prompt", map[string]interface{}{
				"workspace": wsName,
				"name":      agentName,
				"prompt":    fmt.Sprintf("concurrent prompt %d", idx+1),
			}, &promptResult)
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

	// Wait for all goroutines to finish
	wg.Wait()
	close(results)

	// Check for errors
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

	// Verify each agent is in running state after prompt dispatch
	t.Log("Verifying all agents are running...")
	for i, name := range agentNames {
		var statusResult ari.AgentRunStatusResult
		clientMu.Lock()
		err := client.Call("agentrun/status", map[string]interface{}{
			"workspace": wsName,
			"name":      name,
		}, &statusResult)
		clientMu.Unlock()
		if err != nil {
			t.Errorf("agent %d (%s) status failed: %v", i+1, name, err)
			continue
		}
		// Accept running or idle — the mockagent completes turns instantly so
		// the turn may already be done by the time we poll status.
		if statusResult.AgentRun.State != "running" && statusResult.AgentRun.State != "idle" {
			t.Errorf("agent %d (%s): expected state=running or idle, got %s", i+1, name, statusResult.AgentRun.State)
		}
	}

	// Stop and delete all agents; wait for stopped before delete
	t.Log("Stopping and deleting all agents...")
	for i, name := range agentNames {
		clientMu.Lock()
		stopErr := client.Call("agentrun/stop", map[string]interface{}{
			"workspace": wsName,
			"name":      name,
		}, nil)
		clientMu.Unlock()
		if stopErr != nil {
			t.Logf("warning: agent %d (%s) stop failed: %v", i+1, name, stopErr)
		}

		// Poll for stopped/error state before deleting (serialized)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var st ari.AgentRunStatusResult
			clientMu.Lock()
			err := client.Call("agentrun/status", map[string]interface{}{
				"workspace": wsName,
				"name":      name,
			}, &st)
			clientMu.Unlock()
			if err != nil {
				break
			}
			if st.AgentRun.State == "stopped" || st.AgentRun.State == "error" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}

		clientMu.Lock()
		delErr := client.Call("agentrun/delete", map[string]interface{}{
			"workspace": wsName,
			"name":      name,
		}, nil)
		clientMu.Unlock()
		if delErr != nil {
			t.Logf("warning: agent %d (%s) delete failed: %v", i+1, name, delErr)
		}
	}

	t.Log("Multiple concurrent agents test completed successfully! ✓")
}
