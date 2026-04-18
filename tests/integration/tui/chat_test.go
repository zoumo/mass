// Package tui_test provides integration tests for the TUI chat data flow.
// These tests verify the end-to-end data pipeline that the TUI consumes:
// agentrun events → state changes → chat component rendering.
//
// No real terminal is needed. Tests drive the data flow programmatically and
// verify that:
//   - Events received from agentrun match expectations
//   - State transitions are correct (idle → running → idle)
//   - The chat component renders expected content
package tui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/tui/chat"
	"github.com/zoumo/mass/pkg/tui/component"
	"github.com/zoumo/mass/tests/integration/testutil"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

// setupAgentRunForTUI creates a bundle, starts `mass run`, and returns the
// connected agentrun client plus the socket path.
func setupAgentRunForTUI(t *testing.T) (context.Context, *runclient.Client, string, func()) {
	t.Helper()

	massBin := testutil.MassBinPath(t)
	mockagentBin := testutil.MockagentBinPath(t)

	bundleDir := t.TempDir()
	// Use /tmp for stateBaseDir to keep Unix socket path under macOS 104-byte limit.
	// t.TempDir() paths (via $TMPDIR) are too long on macOS.
	stateBaseDir, err := os.MkdirTemp("/tmp", "mass-tui-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(stateBaseDir) })
	agentID := fmt.Sprintf("tui-test-%d", testutil.NewSocketCounter())

	wsDir := filepath.Join(bundleDir, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	cfg := apiruntime.Config{
		MassVersion:    "0.1.0",
		Metadata:       apiruntime.Metadata{Name: agentID},
		AgentRoot:      apiruntime.AgentRoot{Path: "workspace"},
		ClientProtocol: apiruntime.ClientProtocolACP,
		Process: apiruntime.Process{
			Command: mockagentBin,
		},
		Session: apiruntime.Session{
			Permissions: apiruntime.ApproveAll,
		},
	}
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), cfgData, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	stateDir := filepath.Join(stateBaseDir, agentID)
	socketPath := filepath.Join(stateDir, "agent-run.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

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
	t.Logf("mass run started PID %d", cmd.Process.Pid)

	if err := testutil.WaitForSocket(socketPath, 15*time.Second); err != nil {
		cancel()
		cmd.Process.Kill()
		t.Fatalf("agentrun socket not ready: %v", err)
	}

	client, err := runclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		cmd.Process.Kill()
		t.Fatalf("dial agentrun: %v", err)
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

	return ctx, client, socketPath, cleanup
}

// TestChatDataFlow verifies the TUI data pipeline end-to-end:
// 1. Connect to agentrun and watch events
// 2. Send prompt, collect events
// 3. Feed events into chat component (same as chatModel.handleNotif)
// 4. Verify rendered output contains expected content
func TestChatDataFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, client, _, cleanup := setupAgentRunForTUI(t)
	defer cleanup()

	// Start watching events.
	watcher, err := client.WatchEvent(ctx, nil)
	if err != nil {
		t.Fatalf("WatchEvent: %v", err)
	}
	defer watcher.Stop()

	// Send prompt (fire-and-forget).
	t.Log("Sending prompt")
	if err := client.SendPrompt(ctx, &runapi.SessionPromptParams{
		Prompt: []runapi.ContentBlock{runapi.TextBlock("hello from tui test")},
	}); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	// Collect events until turn_end.
	var events []runapi.AgentRunEvent
	timeout := time.After(15 * time.Second)

collectLoop:
	for {
		select {
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatal("watcher closed")
			}
			events = append(events, ev)
			t.Logf("event: type=%s seq=%d", ev.Type, ev.Seq)
			if ev.Type == runapi.EventTypeTurnEnd {
				break collectLoop
			}
		case <-timeout:
			t.Fatalf("timeout (received %d events)", len(events))
		}
	}

	t.Logf("Collected %d events", len(events))

	// Now replay events through the chat component to verify rendering.
	chatView := component.NewChat()
	sty := styles.DefaultStyles()
	turnCounter := 0
	nextID := func(prefix string) string {
		turnCounter++
		return fmt.Sprintf("%s-%d", prefix, turnCounter)
	}

	// Add user message.
	userMsg := chat.NewFinishedStreamingMessage(nextID("user"), component.RoleUser, "hello from tui test")
	chatView.AppendMessages(component.NewUserMessageItem(&sty, userMsg))

	// Create assistant streaming message.
	assistantMsg := chat.NewStreamingMessage(nextID("assistant"), component.RoleAssistant)
	assistantItem := component.NewAssistantMessageItem(&sty, assistantMsg)
	chatView.AppendMessages(assistantItem)

	// Process events as chatModel.handleNotif would.
	for _, ev := range events {
		switch pl := ev.Payload.(type) {
		case runapi.ContentEvent:
			switch ev.Type {
			case runapi.EventTypeAgentMessage:
				if pl.Content.Text != nil {
					assistantMsg.AppendText(pl.Content.Text.Text)
				}
			case runapi.EventTypeAgentThinking:
				if pl.Content.Text != nil {
					assistantMsg.AppendThinking(pl.Content.Text.Text)
				}
			}
		}
	}

	// Finish the assistant message.
	assistantMsg.Finish(component.FinishReasonEndTurn)

	// Update the assistant item with the final message.
	if a, ok := assistantItem.(*component.AssistantMessageItem); ok {
		a.SetMessage(assistantMsg)
	}

	// Set size and render. Strip ANSI codes for plain-text matching.
	chatView.SetSize(80, 24)
	rendered := chatView.Render()
	plain := ansi.Strip(rendered)
	t.Logf("Rendered chat:\n%s", rendered)

	// Verify: user message should appear.
	if !strings.Contains(plain, "hello from tui test") {
		t.Error("rendered output missing user message text")
	}

	// Verify: assistant response from mockagent should appear.
	// mockagent responds with "mock response".
	if !strings.Contains(plain, "mock response") {
		t.Error("rendered output missing assistant response text 'mock response'")
	}

	t.Log("Chat data flow verified ✓")
}

// TestChatStateTransitions verifies that the TUI state machine tracks
// agent state changes correctly through events.
func TestChatStateTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, client, _, cleanup := setupAgentRunForTUI(t)
	defer cleanup()

	// Watch events from beginning to catch state changes.
	fromSeq := 0
	watcher, err := client.WatchEvent(ctx, &runapi.SessionWatchEventParams{FromSeq: &fromSeq})
	if err != nil {
		t.Fatalf("WatchEvent: %v", err)
	}
	defer watcher.Stop()

	// Track state transitions from runtime_update events.
	type stateChange struct {
		previous string
		status   string
	}
	var stateChanges []stateChange

	// Collect bootstrap state changes first.
	bootstrapTimeout := time.After(5 * time.Second)
bootstrapLoop:
	for {
		select {
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatal("watcher closed during bootstrap")
			}
			if ev.Type == runapi.EventTypeRuntimeUpdate {
				if ru, ok := ev.Payload.(runapi.RuntimeUpdateEvent); ok && ru.Status != nil {
					sc := stateChange{previous: ru.Status.PreviousStatus, status: ru.Status.Status}
					stateChanges = append(stateChanges, sc)
					t.Logf("state change: %s → %s", sc.previous, sc.status)
					if ru.Status.Status == "idle" {
						break bootstrapLoop
					}
				}
			}
		case <-bootstrapTimeout:
			break bootstrapLoop
		}
	}

	// Verify agent reached idle.
	if len(stateChanges) == 0 || stateChanges[len(stateChanges)-1].status != "idle" {
		t.Fatalf("agent did not reach idle state during bootstrap (changes: %+v)", stateChanges)
	}
	t.Log("Agent bootstrap → idle ✓")

	// Send prompt to trigger running state.
	t.Log("Sending prompt to trigger state transitions")
	if err := client.SendPrompt(ctx, &runapi.SessionPromptParams{
		Prompt: []runapi.ContentBlock{runapi.TextBlock("state transition test")},
	}); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	// Collect events until turn_end, tracking state changes.
	turnTimeout := time.After(15 * time.Second)
turnLoop:
	for {
		select {
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatal("watcher closed during turn")
			}
			if ev.Type == runapi.EventTypeRuntimeUpdate {
				if ru, ok := ev.Payload.(runapi.RuntimeUpdateEvent); ok && ru.Status != nil {
					sc := stateChange{previous: ru.Status.PreviousStatus, status: ru.Status.Status}
					stateChanges = append(stateChanges, sc)
					t.Logf("state change: %s → %s", sc.previous, sc.status)
				}
			}
			if ev.Type == runapi.EventTypeTurnEnd {
				break turnLoop
			}
		case <-turnTimeout:
			t.Fatalf("timeout waiting for turn_end")
		}
	}

	// Verify we saw the expected state flow: idle → running → idle.
	states := make([]string, len(stateChanges))
	for i, sc := range stateChanges {
		states[i] = sc.status
	}
	t.Logf("All state transitions: %v", states)

	// Must have seen "running" at some point after bootstrap.
	sawRunning := false
	sawIdleAfterRunning := false
	for _, sc := range stateChanges {
		if sc.status == "running" {
			sawRunning = true
		}
		if sawRunning && sc.status == "idle" {
			sawIdleAfterRunning = true
		}
	}

	if !sawRunning {
		t.Error("never saw 'running' state during prompt turn")
	}
	if !sawIdleAfterRunning {
		t.Error("agent did not return to 'idle' after prompt turn completed")
	}

	t.Log("State transitions verified: idle → running → idle ✓")
}

// TestChatComponentRendering verifies the chat component renders different
// message types correctly (user, assistant, system, tool).
func TestChatComponentRendering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// This test uses the chat component directly (no agentrun needed).
	// It verifies that different message types render correctly.
	chatView := component.NewChat()
	sty := styles.DefaultStyles()

	// Add user message.
	userMsg := chat.NewFinishedStreamingMessage("u1", component.RoleUser, "What is 2+2?")
	chatView.AppendMessages(component.NewUserMessageItem(&sty, userMsg))

	// Add assistant message with thinking.
	assistantMsg := chat.NewStreamingMessage("a1", component.RoleAssistant)
	assistantMsg.AppendThinking("Let me calculate...")
	assistantMsg.AppendText("The answer is 4.")
	assistantMsg.Finish(component.FinishReasonEndTurn)
	assistantItem := component.NewAssistantMessageItem(&sty, assistantMsg)
	chatView.AppendMessages(assistantItem)

	// Add system message.
	chatView.AppendMessages(component.NewSystemItem("s1", "connected", lipgloss.NewStyle().Faint(true)))

	// Set size and render. Strip ANSI escape codes so plain-text matching works
	// even when styles inject color sequences mid-word.
	chatView.SetSize(80, 40)
	rendered := chatView.Render()
	plain := ansi.Strip(rendered)

	// Verify user message renders.
	if !strings.Contains(plain, "What is 2+2?") {
		t.Error("rendered output missing user message")
	}

	// Verify assistant message renders.
	if !strings.Contains(plain, "The answer is 4.") {
		t.Error("rendered output missing assistant response")
	}

	// Verify message count.
	if chatView.Len() != 3 {
		t.Errorf("expected 3 messages, got %d", chatView.Len())
	}

	t.Logf("Rendered output (%d chars):\n%s", len(rendered), rendered)
	t.Log("Component rendering verified ✓")
}
