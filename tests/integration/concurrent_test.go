// Package integration_test provides integration tests for concurrent agent management.
// These tests verify that multiple agents can run concurrently without interference.
package integration_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// TestMultipleConcurrentAgents tests that multiple agents can run concurrently
// without interference. Each agent should respond independently.
func TestMultipleConcurrentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	numAgents := 3

	// Prepare a shared workspace and room for all agents
	workspaceId := prepareTestWorkspace(t, ctx, client)
	defer cleanupTestWorkspace(t, client, workspaceId)

	createRoom(t, client, "concurrent-room")
	defer deleteRoom(t, client, "concurrent-room")

	// Create agents and collect their IDs
	agentIds := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		name := fmt.Sprintf("concurrent-agent-%d", i+1)
		status := createAgentAndWait(t, client, workspaceId, "concurrent-room", name)
		agentIds[i] = status.Agent.AgentId
		t.Logf("created agent %d: id=%s name=%s", i+1, agentIds[i], name)
	}

	// Mutex for serializing client calls (ARI client is not thread-safe for concurrent calls)
	var clientMu sync.Mutex

	// Prompt agents concurrently (with serialized ARI calls)
	t.Log("Prompting agents concurrently...")
	results := make(chan error, numAgents)
	var wg sync.WaitGroup

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func(idx int, agentId string) {
			defer wg.Done()

			promptParams := map[string]interface{}{
				"agentId": agentId,
				"prompt":  fmt.Sprintf("concurrent prompt %d", idx+1),
			}
			var promptResult ari.AgentPromptResult

			clientMu.Lock()
			err := client.Call("agent/prompt", promptParams, &promptResult)
			clientMu.Unlock()

			if err != nil {
				results <- fmt.Errorf("agent %d prompt failed: %w", idx+1, err)
				return
			}

			if !promptResult.Accepted {
				results <- fmt.Errorf("agent %d: expected prompt to be accepted", idx+1)
				return
			}

			t.Logf("agent %d prompt accepted", idx+1)
			results <- nil
		}(i, agentIds[i])
	}

	// Wait for all goroutines to complete
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
		t.Fatalf("%d/%d agents had errors", errorCount, numAgents)
	}

	t.Logf("All %d agents responded successfully! ✓", numAgents)

	// Verify each agent is in running state after prompt
	t.Log("Verifying all agents are running...")
	for i, agentId := range agentIds {
		statusParams := map[string]interface{}{"agentId": agentId}
		var statusResult ari.AgentStatusResult
		clientMu.Lock()
		err := client.Call("agent/status", statusParams, &statusResult)
		clientMu.Unlock()
		if err != nil {
			t.Errorf("agent %d status failed: %v", i+1, err)
			continue
		}
		if statusResult.Agent.State != "running" {
			t.Errorf("agent %d: expected state=running, got %s", i+1, statusResult.Agent.State)
		}
	}

	// Stop and delete all agents
	t.Log("Stopping and deleting all agents...")
	for i, agentId := range agentIds {
		clientMu.Lock()
		stopErr := client.Call("agent/stop", map[string]interface{}{"agentId": agentId}, nil)
		clientMu.Unlock()
		if stopErr != nil {
			t.Logf("warning: agent %d stop failed: %v", i+1, stopErr)
		}

		clientMu.Lock()
		delErr := client.Call("agent/delete", map[string]interface{}{"agentId": agentId}, nil)
		clientMu.Unlock()
		if delErr != nil {
			t.Logf("warning: agent %d delete failed: %v", i+1, delErr)
		}
	}

	t.Log("Multiple concurrent agents test completed successfully! ✓")
}
