// Package agentrun_test provides integration tests for the agent-run process.
// These tests start `mass run` directly and verify the agentrun RPC surface
// (session/prompt, session/watch_event, runtime/status) without going through
// the mass daemon.
package agentrun_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/tests/integration/testutil"
)

// setupAgentRun creates a bundle directory, starts `mass run`, and returns the
// connected agentrun client. The caller must call the returned cleanup function.
func setupAgentRun(t *testing.T) (context.Context, *runclient.Client, func()) {
	t.Helper()

	massBin := testutil.MassBinPath(t)
	mockagentBin := testutil.MockagentBinPath(t)

	// Create temp dirs for bundle and state.
	// Use /tmp for stateBaseDir to keep Unix socket path under macOS 104-byte limit.
	// t.TempDir() paths (via $TMPDIR) are too long on macOS.
	bundleDir := t.TempDir()
	stateBaseDir, err := os.MkdirTemp("/tmp", "mass-run-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(stateBaseDir) })
	agentID := fmt.Sprintf("test-agent-%d", testutil.NewSocketCounter())

	// Create workspace dir inside bundle.
	wsDir := filepath.Join(bundleDir, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	// Write config.json.
	cfg := apiruntime.Config{
		MassVersion: "0.1.0",
		Metadata:    apiruntime.Metadata{Name: agentID},
		AgentRoot:   apiruntime.AgentRoot{Path: "workspace"},
		AcpAgent: apiruntime.AcpAgent{
			Process: apiruntime.AcpProcess{
				Command: mockagentBin,
			},
		},
		Permissions: apiruntime.ApproveAll,
	}
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), cfgData, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	// Compute socket path (state-dir/agent-id/agent-run.sock).
	stateDir := filepath.Join(stateBaseDir, agentID)
	socketPath := filepath.Join(stateDir, "agent-run.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	// Start mass run process.
	cmd := exec.CommandContext(ctx, massBin, "run",
		"--bundle", bundleDir,
		"--state-dir", stateBaseDir,
		"--id", agentID,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start mass run: %v", err)
	}
	t.Logf("mass run started with PID %d (bundle=%s id=%s)", cmd.Process.Pid, bundleDir, agentID)

	// Wait for socket to appear.
	if err := testutil.WaitForSocket(socketPath, 15*time.Second); err != nil {
		cancel()
		cmd.Process.Kill()
		t.Fatalf("agentrun socket not ready: %v", err)
	}

	// Connect agentrun client.
	client, err := runclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		cmd.Process.Kill()
		t.Fatalf("failed to dial agentrun: %v", err)
	}

	cleanup := func() {
		client.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
		cancel()
	}

	return ctx, client, cleanup
}

// TestAgentRunPromptAndEvents tests the agentrun process directly:
// connect → watch events → prompt → verify events arrive → verify turn_end.
func TestAgentRunPromptAndEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, client, cleanup := setupAgentRun(t)
	defer cleanup()

	// Start watching events from the beginning.
	watcher, err := client.WatchEvent(ctx, nil)
	if err != nil {
		t.Fatalf("WatchEvent failed: %v", err)
	}
	defer watcher.Stop()

	// Send a prompt (fire-and-forget; Prompt blocks until turn completes).
	t.Log("Sending prompt to agentrun")
	if err := client.SendPrompt(ctx, &runapi.SessionPromptParams{
		Prompt: []runapi.ContentBlock{runapi.TextBlock("hello from agentrun test")},
	}); err != nil {
		t.Fatalf("SendPrompt failed: %v", err)
	}
	t.Log("Prompt dispatched")

	// Collect events until turn_end or timeout.
	t.Log("Collecting events...")
	var events []runapi.AgentRunEvent
	timeout := time.After(15 * time.Second)

collectLoop:
	for {
		select {
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatal("watcher closed unexpectedly")
			}
			events = append(events, ev)
			t.Logf("event: type=%s seq=%d", ev.Type, ev.Seq)
			if ev.Type == runapi.EventTypeTurnEnd {
				break collectLoop
			}
		case <-timeout:
			t.Fatalf("timeout waiting for turn_end (received %d events)", len(events))
		}
	}

	// Verify we got expected event types.
	typeSet := make(map[string]bool)
	for _, ev := range events {
		typeSet[ev.Type] = true
	}

	// Must have turn_end.
	if !typeSet[runapi.EventTypeTurnEnd] {
		t.Error("missing turn_end event")
	}

	// Should have at least one content event (agent_message from mockagent).
	if !typeSet[runapi.EventTypeAgentMessage] {
		t.Error("missing agent_message event")
	}

	t.Logf("Received %d events with types: %v ✓", len(events), typeSet)
}

// TestAgentRunStatus tests the runtime/status RPC.
func TestAgentRunStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, client, cleanup := setupAgentRun(t)
	defer cleanup()

	// Query status — should be idle after bootstrap.
	t.Log("Querying runtime/status")
	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	t.Logf("status: state=%s pid=%d", status.State.Status, status.State.PID)

	if status.State.Status != apiruntime.StatusIdle {
		t.Errorf("expected status=idle, got %s", status.State.Status)
	}
	if status.State.PID <= 0 {
		t.Errorf("expected positive PID, got %d", status.State.PID)
	}
}

// TestAgentRunWatchEventReplay tests that WatchEvent with fromSeq=0 replays
// historical events (bootstrap metadata).
func TestAgentRunWatchEventReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, client, cleanup := setupAgentRun(t)
	defer cleanup()

	// Watch from seq=0 to get replay of bootstrap events.
	fromSeq := 0
	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{FromSeq: &fromSeq})
	if err != nil {
		t.Fatalf("WatchEvent failed: %v", err)
	}
	defer watcher.Stop()

	// Collect bootstrap events (should include runtime_update with state info).
	var events []runapi.AgentRunEvent
	timeout := time.After(5 * time.Second)

	for {
		select {
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				goto done
			}
			events = append(events, ev)
			t.Logf("replay event: type=%s seq=%d", ev.Type, ev.Seq)
			// After getting a few events, stop collecting.
			if len(events) >= 3 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}

done:
	if len(events) == 0 {
		t.Error("expected at least one replay event from WatchEvent(fromSeq=0)")
	} else {
		t.Logf("Received %d replay events ✓", len(events))
	}

	// Verify events have monotonically increasing sequence numbers.
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Errorf("event seq not monotonic: events[%d].Seq=%d <= events[%d].Seq=%d",
				i, events[i].Seq, i-1, events[i-1].Seq)
		}
	}
}
