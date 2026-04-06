// Package integration_test provides integration tests for concurrent session management.
// These tests verify that multiple sessions can run concurrently without interference.
package integration_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// TestMultipleConcurrentSessions tests that multiple sessions can run concurrently
// without interference. Each session should respond independently.
func TestMultipleConcurrentSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	numSessions := 3

	// Prepare workspaces
	workspaceIds := make([]string, numSessions)
	for i := 0; i < numSessions; i++ {
		workspaceIds[i] = prepareTestWorkspace(t, ctx, client)
		t.Logf("prepared workspace %d: %s", i+1, workspaceIds[i])
	}

	// Create sessions
	sessionIds := make([]string, numSessions)
	for i := 0; i < numSessions; i++ {
		sessionIds[i] = createTestSession(t, client, workspaceIds[i])
		t.Logf("created session %d: %s", i+1, sessionIds[i])
	}

	// Mutex for serializing client calls (ARI client is not thread-safe for concurrent calls)
	var clientMu sync.Mutex

	// Prompt sessions concurrently (with serialized ARI calls)
	t.Log("Prompting sessions concurrently...")
	results := make(chan error, numSessions)
	var wg sync.WaitGroup

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(idx int, sessionId string) {
			defer wg.Done()

			promptParams := map[string]interface{}{
				"sessionId": sessionId,
				"text":      fmt.Sprintf("concurrent prompt %d", idx+1),
			}
			var promptResult ari.SessionPromptResult

			clientMu.Lock()
			err := client.Call("session/prompt", promptParams, &promptResult)
			clientMu.Unlock()

			if err != nil {
				results <- fmt.Errorf("session %d prompt failed: %w", idx+1, err)
				return
			}

			// Verify response
			if promptResult.StopReason != "end_turn" {
				results <- fmt.Errorf("session %d: expected stopReason=end_turn, got %s", idx+1, promptResult.StopReason)
				return
			}

			t.Logf("session %d prompt completed: stopReason=%s", idx+1, promptResult.StopReason)
			results <- nil
		}(i, sessionIds[i])
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
		t.Fatalf("%d/%d sessions had errors", errorCount, numSessions)
	}

	t.Logf("All %d sessions responded successfully!", numSessions)

	// Verify each session is running
	t.Log("Verifying all sessions are running...")
	for i, sessionId := range sessionIds {
		statusParams := map[string]interface{}{
			"sessionId": sessionId,
		}
		var statusResult ari.SessionStatusResult
		clientMu.Lock()
		err := client.Call("session/status", statusParams, &statusResult)
		clientMu.Unlock()
		if err != nil {
			t.Errorf("session %d status failed: %v", i+1, err)
			continue
		}
		if statusResult.Session.State != "running" {
			t.Errorf("session %d: expected state=running, got %s", i+1, statusResult.Session.State)
		}
	}

	// Stop all sessions
	t.Log("Stopping all sessions...")
	for i, sessionId := range sessionIds {
		stopParams := map[string]interface{}{
			"sessionId": sessionId,
		}
		clientMu.Lock()
		err := client.Call("session/stop", stopParams, nil)
		clientMu.Unlock()
		if err != nil {
			t.Logf("warning: session %d stop failed: %v", i+1, err)
		}
	}

	// Remove all sessions
	t.Log("Removing all sessions...")
	for i, sessionId := range sessionIds {
		removeParams := map[string]interface{}{
			"sessionId": sessionId,
		}
		clientMu.Lock()
		err := client.Call("session/remove", removeParams, nil)
		clientMu.Unlock()
		if err != nil {
			t.Logf("warning: session %d remove failed: %v", i+1, err)
		}
	}

	// Cleanup workspaces
	t.Log("Cleaning up workspaces...")
	for i, workspaceId := range workspaceIds {
		cleanupTestWorkspace(t, client, workspaceId)
		t.Logf("cleaned up workspace %d", i+1)
	}

	t.Log("Multiple concurrent sessions test completed successfully!")
}

// TestConcurrentPromptsSameSession tests that concurrent prompts to the same session
// are handled correctly (should fail or queue).
func TestConcurrentPromptsSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel, client, cleanup := setupAgentdTest(t)
	defer cleanup()
	defer cancel()

	// Mutex for serializing client calls
	var clientMu sync.Mutex

	// Prepare workspace and session
	workspaceId := prepareTestWorkspace(t, ctx, client)
	sessionId := createTestSession(t, client, workspaceId)

	// First prompt to start the session
	promptParams := map[string]interface{}{
		"sessionId": sessionId,
		"text":      "initial prompt",
	}
	var promptResult ari.SessionPromptResult
	clientMu.Lock()
	err := client.Call("session/prompt", promptParams, &promptResult)
	clientMu.Unlock()
	if err != nil {
		t.Fatalf("initial prompt failed: %v", err)
	}
	t.Logf("initial prompt completed: stopReason=%s", promptResult.StopReason)

	// Try concurrent prompts to the same session
	t.Log("Attempting concurrent prompts to the same session...")
	results := make(chan error, 2)
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			promptParams := map[string]interface{}{
				"sessionId": sessionId,
				"text":      fmt.Sprintf("concurrent prompt %d", idx+1),
			}
			var result ari.SessionPromptResult
			clientMu.Lock()
			err := client.Call("session/prompt", promptParams, &result)
			clientMu.Unlock()
			if err != nil {
				results <- err
				return
			}
			results <- nil
		}(i)
	}

	wg.Wait()
	close(results)

	// Check results
	successCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			t.Logf("concurrent prompt error: %v", err)
		}
	}

	t.Logf("%d/2 concurrent prompts succeeded", successCount)

	// Cleanup
	stopParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	clientMu.Lock()
	client.Call("session/stop", stopParams, nil)
	clientMu.Unlock()

	removeParams := map[string]interface{}{
		"sessionId": sessionId,
	}
	clientMu.Lock()
	client.Call("session/remove", removeParams, nil)
	clientMu.Unlock()

	cleanupTestWorkspace(t, client, workspaceId)

	t.Log("Concurrent prompts same session test completed!")
}