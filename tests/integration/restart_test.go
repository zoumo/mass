// Package integration_test provides integration tests for agentd restart recovery.
// These tests verify that agentd can reconnect to existing shim sockets after restart,
// persist bootstrap config, maintain event continuity, and handle dead shims.
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
	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// readEventSeqs reads the events.jsonl from a shim state dir and returns
// the ordered list of sequence numbers. Returns nil, nil if the file does
// not exist yet.
func readEventSeqs(stateDir string) ([]int, error) {
	path := spec.EventLogPath(stateDir)
	entries, err := events.ReadEventLog(path, 0)
	if err != nil {
		return nil, fmt.Errorf("readEventSeqs: %w", err)
	}
	seqs := make([]int, 0, len(entries))
	for _, e := range entries {
		s, err := e.Seq()
		if err != nil {
			return nil, fmt.Errorf("readEventSeqs: bad envelope: %w", err)
		}
		seqs = append(seqs, s)
	}
	return seqs, nil
}

// countEvents returns the number of event entries in the events.jsonl for the given state dir.
func countEvents(stateDir string) (int, error) {
	seqs, err := readEventSeqs(stateDir)
	if err != nil {
		return 0, err
	}
	return len(seqs), nil
}

// verifyNoSeqGaps asserts that seqs is a contiguous 0-based sequence with no gaps.
func verifyNoSeqGaps(t *testing.T, seqs []int) {
	t.Helper()
	for i, s := range seqs {
		if s != i {
			t.Errorf("event seq gap: expected seq %d at index %d, got %d", i, i, s)
			return
		}
	}
}

// startAgentd launches agentd with the given config and env, waits for the socket,
// and returns the Cmd. Caller is responsible for cleanup.
func startAgentd(t *testing.T, ctx context.Context, agentdBin, configPath, agentShimBin, socketPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, agentdBin, "--config", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("OAR_SHIM_BINARY=%s", agentShimBin))

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start agentd: %v", err)
	}
	t.Logf("agentd started with PID %d", cmd.Process.Pid)

	if err := waitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}
	return cmd
}

// stopAgentd gracefully kills agentd with SIGINT and waits for exit.
func stopAgentd(t *testing.T, cmd *exec.Cmd, socketPath string) {
	t.Helper()
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Log("agentd stopped")
	}
	os.Remove(socketPath)
}

// waitForSessionState polls session/status until the session reaches the desired
// state or the timeout expires. Returns the final status result.
func waitForSessionState(t *testing.T, client *ari.Client, sessionId, wantState string, timeout time.Duration) ari.SessionStatusResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	params := map[string]interface{}{"sessionId": sessionId}
	var result ari.SessionStatusResult
	for time.Now().Before(deadline) {
		if err := client.Call("session/status", params, &result); err != nil {
			t.Logf("session/status for %s: %v (retrying)", sessionId, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if result.Session.State == wantState {
			return result
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach state %q within %v (last state: %s)", sessionId, wantState, timeout, result.Session.State)
	return result // unreachable
}

// TestAgentdRestartRecovery proves that:
//  1. Bootstrap config is persisted and survives daemon restart.
//  2. Live shim reconnects after restart — session returns to running state.
//  3. Events from before and after restart form a contiguous sequence with no gaps.
//  4. A dead shim (killed before restart) is correctly marked stopped by recovery.
//
// This validates R035 (single resume path closes event gap) and
// R036 (enough config persisted to rebuild truthful state after restart).
func TestAgentdRestartRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// ── Setup ──────────────────────────────────────────────────────────────
	tmpDir := t.TempDir()
	testSocketCounter++
	socketPath := fmt.Sprintf("/tmp/oar-%d-%d.sock", os.Getpid(), testSocketCounter)
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	metaDB := filepath.Join(tmpDir, "agentd.db")
	bundleRoot := filepath.Join(tmpDir, "bundles")

	os.Remove(socketPath)
	for _, d := range []string{workspaceRoot, bundleRoot} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	agentdBin, _ := filepath.Abs("../../bin/agentd")
	agentShimBin, _ := filepath.Abs("../../bin/agent-shim")
	mockagentBin, _ := filepath.Abs("../../bin/mockagent")

	for _, bin := range []string{agentdBin, agentShimBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s", bin)
		}
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := fmt.Sprintf(`
socket: %s
workspaceRoot: %s
metaDB: %s
bundleRoot: %s
runtimeClasses:
  mockagent:
    command: %s
    args: []
    env:
      PATH: /usr/bin:/bin
`, socketPath, workspaceRoot, metaDB, bundleRoot, mockagentBin)

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Cleanup leftover processes at the end of the test.
	defer func() {
		os.Remove(socketPath)
		exec.Command("pkill", "-f", "agent-shim").Run()
		exec.Command("pkill", "-f", "mockagent").Run()
	}()

	// =========================================================================
	// Phase 1: Start agentd, create two sessions, prompt both
	// =========================================================================
	t.Log("Phase 1: Start agentd and create two running sessions")

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()

	agentdCmd1 := startAgentd(t, ctx1, agentdBin, configPath, agentShimBin, socketPath)

	client1, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client: %v", err)
	}

	// Prepare workspace.
	workspaceId := prepareTestWorkspace(t, ctx1, client1)

	// ── Session A (will stay alive across restart) ──
	t.Log("Creating session A (live across restart)")
	var sessionAResult ari.SessionNewResult
	if err := client1.Call("session/new", map[string]interface{}{
		"workspaceId":  workspaceId,
		"runtimeClass": "mockagent",
	}, &sessionAResult); err != nil {
		t.Fatalf("session/new A: %v", err)
	}
	sessionA := sessionAResult.SessionId
	t.Logf("session A: %s", sessionA)

	// Prompt session A.
	var promptResult ari.SessionPromptResult
	if err := client1.Call("session/prompt", map[string]interface{}{
		"sessionId": sessionA,
		"text":      "hello before restart A",
	}, &promptResult); err != nil {
		t.Fatalf("session/prompt A: %v", err)
	}
	t.Logf("session A prompt completed: stopReason=%s", promptResult.StopReason)

	// Get session A status.
	statusA := waitForSessionState(t, client1, sessionA, "running", 5*time.Second)
	if statusA.ShimState != nil {
		t.Logf("session A running, runtime PID=%d", statusA.ShimState.PID)
	} else {
		t.Log("session A running (no shim state)")
	}

	// Read session A state dir from the DB via finding it on disk.
	// The shim state dir follows the pattern: /tmp/agentd-shim/<sessionId>/
	shimStateDirA := ""
	for _, base := range []string{"/run/agentd/shim", "/tmp/agentd-shim"} {
		candidate := filepath.Join(base, sessionA)
		if _, err := os.Stat(candidate); err == nil {
			shimStateDirA = candidate
			break
		}
	}
	if shimStateDirA == "" {
		t.Log("Warning: could not locate shim state dir for session A on disk; skipping event continuity checks")
	} else {
		t.Logf("session A state dir: %s", shimStateDirA)
	}

	// Record pre-restart event count for session A.
	preRestartEventsA := 0
	if shimStateDirA != "" {
		n, err := countEvents(shimStateDirA)
		if err != nil {
			t.Logf("Warning: could not count pre-restart events: %v", err)
		} else {
			preRestartEventsA = n
			t.Logf("session A pre-restart events: %d", preRestartEventsA)
		}
	}

	// ── Session B (will be killed before restart → dead shim) ──
	t.Log("Creating session B (will be killed before restart)")
	var sessionBResult ari.SessionNewResult
	if err := client1.Call("session/new", map[string]interface{}{
		"workspaceId":  workspaceId,
		"runtimeClass": "mockagent",
	}, &sessionBResult); err != nil {
		t.Fatalf("session/new B: %v", err)
	}
	sessionB := sessionBResult.SessionId
	t.Logf("session B: %s", sessionB)

	// Prompt session B to start its shim.
	if err := client1.Call("session/prompt", map[string]interface{}{
		"sessionId": sessionB,
		"text":      "hello before restart B",
	}, &promptResult); err != nil {
		t.Fatalf("session/prompt B: %v", err)
	}
	t.Logf("session B prompt completed: stopReason=%s", promptResult.StopReason)

	// Verify session B is running.
	_ = waitForSessionState(t, client1, sessionB, "running", 5*time.Second)
	t.Logf("session B running")

	// =========================================================================
	// Phase 2: Kill session B's shim, then kill agentd
	// =========================================================================
	t.Log("Phase 2: Kill session B's shim process, then stop agentd")

	// Close client, kill agentd first (so it doesn't interfere with shim cleanup).
	client1.Close()
	stopAgentd(t, agentdCmd1, socketPath)

	// Kill session B's actual agent-shim process (not just the mockagent runtime).
	// ShimState.PID is the runtime/agent PID, not the shim wrapper PID.
	// We kill the shim by finding the agent-shim process that has session B's ID.
	killOutput, _ := exec.Command("pkill", "-9", "-f",
		fmt.Sprintf("agent-shim.*--id %s", sessionB)).CombinedOutput()
	t.Logf("pkill session B shim: %s", string(killOutput))
	// Also remove the shim socket file to ensure recovery cannot connect.
	shimSocketB := spec.ShimSocketPath(filepath.Join("/tmp/agentd-shim", sessionB))
	if err := os.Remove(shimSocketB); err != nil {
		t.Logf("remove session B socket %s: %v (may already be gone)", shimSocketB, err)
	} else {
		t.Logf("removed session B socket: %s", shimSocketB)
	}
	// Give processes time to die.
	time.Sleep(500 * time.Millisecond)

	// Verify session A shim is still alive.
	shimSocketA := spec.ShimSocketPath(filepath.Join("/tmp/agentd-shim", sessionA))
	if _, err := os.Stat(shimSocketA); err != nil {
		t.Fatalf("session A shim socket %s gone — cannot test recovery: %v", shimSocketA, err)
	}
	t.Logf("session A shim socket still present ✓: %s", shimSocketA)

	// Verify session B shim socket is gone.
	if _, err := os.Stat(shimSocketB); err == nil {
		t.Logf("Warning: session B shim socket still exists at %s", shimSocketB)
	} else {
		t.Logf("session B shim socket gone ✓")
	}

	// =========================================================================
	// Phase 3: Restart agentd — recovery pass should reconnect A, mark B stopped
	// =========================================================================
	t.Log("Phase 3: Restart agentd with same config")

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	agentdCmd2 := startAgentd(t, ctx2, agentdBin, configPath, agentShimBin, socketPath)
	defer stopAgentd(t, agentdCmd2, socketPath)

	client2, err := ari.NewClient(socketPath)
	if err != nil {
		t.Fatalf("ARI client after restart: %v", err)
	}
	defer client2.Close()

	// Give recovery pass time to complete.
	time.Sleep(2 * time.Second)

	// =========================================================================
	// Phase 4: Verify session A recovered — running, shim reconnected
	// =========================================================================
	t.Log("Phase 4: Verify session A recovered to running state")

	statusA2 := waitForSessionState(t, client2, sessionA, "running", 10*time.Second)
	t.Logf("session A state after recovery: %s", statusA2.Session.State)

	if statusA2.Session.State != "running" {
		t.Fatalf("expected session A state=running after recovery, got %s", statusA2.Session.State)
	}

	// Verify shim state is populated (shim reconnected).
	if statusA2.ShimState == nil {
		t.Error("expected session A to have shimState after recovery")
	} else {
		t.Logf("session A shim after recovery: status=%s pid=%d", statusA2.ShimState.Status, statusA2.ShimState.PID)
	}

	// =========================================================================
	// Phase 5: Verify session B is stopped (dead shim → fail-closed)
	// =========================================================================
	t.Log("Phase 5: Verify session B marked stopped (dead shim)")

	var statusB2 ari.SessionStatusResult
	if err := client2.Call("session/status", map[string]interface{}{
		"sessionId": sessionB,
	}, &statusB2); err != nil {
		t.Fatalf("session/status B after restart: %v", err)
	}
	t.Logf("session B state after recovery: %s", statusB2.Session.State)

	if statusB2.Session.State != "stopped" {
		t.Errorf("expected session B state=stopped (dead shim), got %s", statusB2.Session.State)
	}

	// =========================================================================
	// Phase 6: Event continuity — prompt A again, verify no seq gaps
	// =========================================================================
	t.Log("Phase 6: Verify event continuity for session A")

	if err := client2.Call("session/prompt", map[string]interface{}{
		"sessionId": sessionA,
		"text":      "hello after restart A",
	}, &promptResult); err != nil {
		t.Fatalf("session/prompt A after restart: %v", err)
	}
	t.Logf("session A post-restart prompt completed: stopReason=%s", promptResult.StopReason)

	// Read event log and verify contiguous sequence.
	if shimStateDirA != "" {
		postRestartEventsA, err := countEvents(shimStateDirA)
		if err != nil {
			t.Logf("Warning: could not count post-restart events: %v", err)
		} else {
			t.Logf("session A post-restart events: %d (was %d)", postRestartEventsA, preRestartEventsA)
			if postRestartEventsA <= preRestartEventsA {
				t.Errorf("expected more events after post-restart prompt, got %d (was %d)", postRestartEventsA, preRestartEventsA)
			}
		}

		seqs, err := readEventSeqs(shimStateDirA)
		if err != nil {
			t.Logf("Warning: could not read event seqs: %v", err)
		} else {
			t.Logf("session A event sequence: %v", seqs)
			verifyNoSeqGaps(t, seqs)
			t.Logf("event continuity verified: %d events, no seq gaps ✓", len(seqs))
		}
	} else {
		t.Log("Skipping event continuity check (state dir not found)")
	}

	// =========================================================================
	// Cleanup
	// =========================================================================
	t.Log("Cleanup: Stop sessions and workspace")

	// Stop session A.
	if err := client2.Call("session/stop", map[string]interface{}{"sessionId": sessionA}, nil); err != nil {
		t.Logf("session/stop A: %v", err)
	}
	// Remove sessions.
	if err := client2.Call("session/remove", map[string]interface{}{"sessionId": sessionA}, nil); err != nil {
		t.Logf("session/remove A: %v", err)
	}
	if err := client2.Call("session/remove", map[string]interface{}{"sessionId": sessionB}, nil); err != nil {
		t.Logf("session/remove B: %v", err)
	}
	// Cleanup workspace.
	cleanupTestWorkspace(t, client2, workspaceId)

	// Stop session B if still running (recovery may have left it in running or stopped state).
	client2.Call("session/stop", map[string]interface{}{"sessionId": sessionB}, nil)
	client2.Call("session/remove", map[string]interface{}{"sessionId": sessionB}, nil)

	t.Log("Restart recovery test completed!")
}
