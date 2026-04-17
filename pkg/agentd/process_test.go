// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file contains integration tests for the ProcessManager.
package agentd

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zoumo/mass/pkg/agentd/store"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// TestProcessManagerStart tests the full Start workflow:
// get AgentRun → resolve Agent definition from DB → generate config.json → create bundle
// → fork agent-run → wait for socket → connect Client → subscribe events
// → transition agent status to "running".
func TestProcessManagerStart(t *testing.T) {
	// Build and find binaries.
	runBinary := findRunBinary(t)
	mockagentBinary := findMockagentBinary(t)

	// Setup test environment.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create temp directories.
	tmpDir, err := os.MkdirTemp("/tmp", "mass-pm-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	dbPath := filepath.Join(tmpDir, "meta.db")
	workspaceRoot := filepath.Join(tmpDir, "workspaces")
	bundleRoot := filepath.Join(tmpDir, "bundles")

	for _, dir := range []string{workspaceRoot, bundleRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Create meta store.
	metaStore, err := store.NewStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer metaStore.Close()

	// Persist runtime record for "mockagent" to the DB store.
	if err := metaStore.SetAgent(ctx, &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec: pkgariapi.AgentSpec{
			Command: mockagentBinary,
			Args:    []string{},
		},
	}); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}

	socketPath := filepath.Join(tmpDir, "mass.sock")

	// Create AgentManager and ProcessManager.
	agentMgr := NewAgentRunManager(metaStore, slog.Default())
	procMgr := NewProcessManager(agentMgr, metaStore, socketPath, bundleRoot, slog.Default(), "info", "pretty")
	procMgr.RunBinary = runBinary

	// Create a workspace with a ready path.
	workspacePath := filepath.Join(workspaceRoot, "test-workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspacePath: %v", err)
	}
	ws := &pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "test-ws"},
		Status: pkgariapi.WorkspaceStatus{
			Phase: pkgariapi.WorkspacePhaseReady,
			Path:  workspacePath,
		},
	}
	if err := metaStore.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Create an agent in "creating" state (required by Start).
	agentWorkspace := "test-ws"
	agentName := "test-agent"
	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: agentWorkspace,
			Name:      agentName,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent: "mockagent",
		},
		Status: pkgariapi.AgentRunStatus{
			State: apiruntime.StatusCreating,
		},
	}
	if err := metaStore.CreateAgentRun(ctx, agent); err != nil {
		t.Fatalf("CreateAgentRun: %v", err)
	}

	// Call ProcessManager.Start.
	runProc, err := procMgr.Start(ctx, agentWorkspace, agentName)
	if err != nil {
		key := agentKey(agentWorkspace, agentName)
		// Print state directory content for debugging.
		stateDirPath := filepath.Join(os.TempDir(), "mass-run", key)
		t.Logf("State directory %s contents:", stateDirPath)
		if entries, readErr := os.ReadDir(stateDirPath); readErr == nil {
			for _, entry := range entries {
				t.Logf("  %s", entry.Name())
				if entry.Name() == "state.json" {
					if data, readErr := os.ReadFile(filepath.Join(stateDirPath, "state.json")); readErr == nil {
						t.Logf("state.json content:\n%s", string(data))
					}
				}
			}
		}
		t.Fatalf("ProcessManager.Start: %v", err)
	}

	// Verify agent status transitions to idle/running via runtime/state_change
	// notification (D088 — direct StatusRunning write removed from Start).
	// Poll until the agent-run emits its first stateChange notification.
	var updatedAgent *pkgariapi.AgentRun
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		updatedAgent, err = agentMgr.Get(ctx, agentWorkspace, agentName)
		if err != nil {
			t.Fatalf("Get agent after Start: %v", err)
		}
		if updatedAgent.Status.State != apiruntime.StatusCreating {
			break
		}
	}
	if updatedAgent.Status.State != apiruntime.StatusIdle && updatedAgent.Status.State != apiruntime.StatusRunning {
		t.Errorf("expected agent state 'idle' or 'running' after stateChange notification, got '%s'", updatedAgent.Status.State)
	}

	// Verify PID > 0.
	if runProc.PID <= 0 {
		t.Errorf("expected PID > 0, got PID=%d", runProc.PID)
	}

	// Verify Client is connected.
	if runProc.Client == nil {
		t.Fatal("expected Client to be connected, got nil")
	}

	// Verify agent-run state via runtime/status RPC.
	statusResult, err := runProc.Client.Status(ctx)
	if err != nil {
		t.Fatalf("runtime/status RPC: %v", err)
	}
	state := statusResult.State
	t.Logf("Agent-run state: ID=%s, Status=%s, PID=%d, Bundle=%s, recovery.lastSeq=%d",
		state.ID, state.Status, state.PID, state.Bundle, statusResult.Recovery.LastSeq)

	if state.Status != apiruntime.StatusIdle && state.Status != apiruntime.StatusRunning {
		t.Errorf("expected agent-run status 'idle' or 'running', got '%s'", state.Status)
	}

	// Stop the default drain goroutine so the test can read events.
	runProc.StopDrain()

	// Send a Prompt to trigger events (session/prompt).
	promptResult, err := runProc.Client.Prompt(ctx, &runapi.SessionPromptParams{Prompt: []runapi.ContentBlock{runapi.TextBlock("hello mockagent")}})
	if err != nil {
		t.Fatalf("Prompt RPC: %v", err)
	}
	t.Logf("Prompt result: stopReason=%s", promptResult.StopReason)

	// Verify events were received.
	eventCount := 0
	timeout := time.After(5 * time.Second)

	for {
		select {
		case update, ok := <-runProc.Events:
			if !ok {
				t.Log("Events channel closed")
				goto done
			}
			eventCount++
			t.Logf("Received event #%d: seq=%d type=%s",
				eventCount, update.Seq, update.Type)
			if _, ok := update.Payload.(runapi.ContentEvent); ok {
				t.Logf("ContentEvent received")
			}
			if _, ok := update.Payload.(runapi.TurnEndEvent); ok {
				t.Logf("TurnEndEvent received, prompt complete")
				goto done
			}
		case <-timeout:
			if eventCount == 0 {
				t.Error("expected to receive events, got none within timeout")
			}
			goto done
		}
	}
done:

	if eventCount == 0 {
		t.Error("expected to receive at least one event, got none")
	} else {
		t.Logf("Received %d events total", eventCount)
	}

	// Clean up: stop the agent-run (runtime/stop).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	if err := runProc.Client.Stop(stopCtx); err != nil {
		t.Logf("runtime/stop RPC (non-fatal): %v", err)
	}

	// Wait for agent-run process to exit.
	select {
	case <-runProc.Done:
		t.Log("Agent-run process exited gracefully")
	case <-time.After(3 * time.Second):
		t.Log("Timeout waiting for agent-run exit, killing process")
		_ = runProc.Cmd.Process.Kill()
	}

	// Verify agent status transitioned to "stopped".
	var finalAgent *pkgariapi.AgentRun
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		finalAgent, err = agentMgr.Get(ctx, agentWorkspace, agentName)
		if err != nil {
			t.Fatalf("Get agent after shutdown: %v", err)
		}
		if finalAgent.Status.State == apiruntime.StatusStopped {
			break
		}
	}
	if finalAgent == nil || finalAgent.Status.State != apiruntime.StatusStopped {
		got := apiruntime.Status("")
		if finalAgent != nil {
			got = finalAgent.Status.State
		}
		t.Errorf("expected agent state 'stopped' after shutdown, got '%s'", got)
	}

	// Stop leaves the bundle on disk; explicit agent deletion owns bundle cleanup.
	if _, err := os.Stat(runProc.BundlePath); err != nil {
		t.Errorf("expected bundle directory to remain after stop, got %v", err)
	}

	t.Logf("Test complete: agent %s/%s lifecycle: creating → running → stopped", agentWorkspace, agentName)
}

// ── generateConfig ────────────────────────────────────────────────────────────

// TestGenerateConfig verifies that generateConfig produces correct config from an Agent.
func TestGenerateConfig(t *testing.T) {
	pm := &ProcessManager{
		socketPath: "/tmp/test-mass.sock",
	}
	rc := &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec: pkgariapi.AgentSpec{
			Command: "/usr/bin/mockagent",
			Args:    []string{},
			Env: []apiruntime.EnvVar{
				{Name: "SOME_VAR", Value: "value"},
			},
		},
	}

	t.Run("basic agent config", func(t *testing.T) {
		agent := &pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{
				Workspace: "ws1",
				Name:      "my-agent",
				Labels:    map[string]string{"team": "platform"},
			},
			Spec: pkgariapi.AgentRunSpec{
				Agent:        "mockagent",
				SystemPrompt: "you are helpful",
			},
		}

		cfg := pm.generateConfig(agent, rc)

		if cfg.Metadata.Name != "my-agent" {
			t.Errorf("expected Name=my-agent, got %q", cfg.Metadata.Name)
		}
		if cfg.Session.SystemPrompt != "you are helpful" {
			t.Errorf("expected SystemPrompt='you are helpful', got %q", cfg.Session.SystemPrompt)
		}
		if cfg.Process.Command != "/usr/bin/mockagent" {
			t.Errorf("expected Command=/usr/bin/mockagent, got %q", cfg.Process.Command)
		}
		if len(cfg.Session.McpServers) != 1 {
			t.Errorf("expected 1 MCP server, got %d", len(cfg.Session.McpServers))
		} else if cfg.Session.McpServers[0].Name != "workspace" {
			t.Errorf("expected workspace MCP server, got %q", cfg.Session.McpServers[0].Name)
		}
		// Verify annotations include runtimeClass.
		if cfg.Metadata.Annotations["agent"] != "mockagent" {
			t.Errorf("expected annotations.runtimeClass=mockagent, got %q", cfg.Metadata.Annotations["agent"])
		}
		// Verify team label propagated.
		if cfg.Metadata.Annotations["team"] != "platform" {
			t.Errorf("expected annotations.team=platform, got %q", cfg.Metadata.Annotations["team"])
		}
		// Verify env var.
		found := false
		for _, e := range cfg.Process.Env {
			if e == "SOME_VAR=value" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected SOME_VAR=value in env, not found")
		}
	})

	t.Run("agent without system prompt", func(t *testing.T) {
		agent := &pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{
				Workspace: "ws1",
				Name:      "bare-agent",
			},
			Spec: pkgariapi.AgentRunSpec{
				Agent: "mockagent",
			},
		}

		cfg := pm.generateConfig(agent, rc)
		if cfg.Session.SystemPrompt != "" {
			t.Errorf("expected empty SystemPrompt, got %q", cfg.Session.SystemPrompt)
		}
	})
}

// findRunBinary finds the mass binary for testing (it contains the run subcommand).
func findRunBinary(t *testing.T) string {
	projectRoot := findProjectRoot(t)
	builtPath := filepath.Join(projectRoot, "bin", "mass")

	binDir := filepath.Join(projectRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", builtPath, "./cmd/mass")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("build mass: %v", err)
	}

	return builtPath
}

// findMockagentBinary finds the mockagent binary for testing.
func findMockagentBinary(t *testing.T) string {
	projectRoot := findProjectRoot(t)
	builtPath := filepath.Join(projectRoot, "bin", "mockagent")
	if _, err := os.Stat(builtPath); err == nil {
		return builtPath
	}

	binDir := filepath.Join(projectRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", builtPath, "./internal/testutil/mockagent")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("build mockagent: %v", err)
	}

	return builtPath
}

// findProjectRoot finds the project root directory by walking up to go.mod.
func findProjectRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}
