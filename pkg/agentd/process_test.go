// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file contains integration tests for the ProcessManager.
package agentd

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
			Phase: apiruntime.PhaseCreating,
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
		if updatedAgent.Status.Phase != apiruntime.PhaseCreating {
			break
		}
	}
	if updatedAgent.Status.Phase != apiruntime.PhaseIdle && updatedAgent.Status.Phase != apiruntime.PhaseRunning {
		t.Errorf("expected agent state 'idle' or 'running' after stateChange notification, got '%s'", updatedAgent.Status.Phase)
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
		state.ID, state.Phase, state.PID, state.Bundle, statusResult.Recovery.LastSeq)

	if state.Phase != apiruntime.PhaseIdle && state.Phase != apiruntime.PhaseRunning {
		t.Errorf("expected agent-run status 'idle' or 'running', got '%s'", state.Phase)
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
		if finalAgent.Status.Phase == apiruntime.PhaseStopped {
			break
		}
	}
	if finalAgent == nil || finalAgent.Status.Phase != apiruntime.PhaseStopped {
		got := apiruntime.Phase("")
		if finalAgent != nil {
			got = finalAgent.Status.Phase
		}
		t.Errorf("expected agent state 'stopped' after shutdown, got '%s'", got)
	}

	// Stop leaves the bundle on disk; explicit agent deletion owns bundle cleanup.
	if _, err := os.Stat(runProc.BundlePath); err != nil {
		t.Errorf("expected bundle directory to remain after stop, got %v", err)
	}

	t.Logf("Test complete: agent %s/%s lifecycle: creating → running → stopped", agentWorkspace, agentName)
}

// ── agentKey ──────────────────────────────────────────────────────────────────

func TestAgentKey(t *testing.T) {
	t.Parallel()
	if got := agentKey("ws1", "agent-a"); got != "ws1/agent-a" {
		t.Errorf("agentKey: expected ws1/agent-a, got %s", got)
	}
}

// ── BundlePath ───────────────────────────────────────────────────────────────

func TestProcessManager_BundlePath(t *testing.T) {
	t.Parallel()
	pm := &ProcessManager{bundleRoot: "/tmp/test-mass/runs"}
	got := pm.BundlePath("ws1", "agent-a")
	expected := "/tmp/test-mass/runs/ws1/agent-a"
	if got != expected {
		t.Errorf("BundlePath: expected %s, got %s", expected, got)
	}
}

// ── ValidateAgentSocketPath ──────────────────────────────────────────────────

func TestProcessManager_ValidateAgentSocketPath(t *testing.T) {
	t.Parallel()

	t.Run("short path passes", func(t *testing.T) {
		pm := &ProcessManager{bundleRoot: "/tmp/b"}
		err := pm.ValidateAgentSocketPath("ws", "a")
		if err != nil {
			t.Errorf("expected no error for short path, got: %v", err)
		}
	})

	t.Run("long path fails", func(t *testing.T) {
		// Create a bundle root long enough that the socket path exceeds 104 bytes.
		// Final path: longRoot/workspace-name-agent-name/agent-run.sock
		longRoot := "/tmp/" + strings.Repeat("x", 80)
		pm := &ProcessManager{bundleRoot: longRoot}
		err := pm.ValidateAgentSocketPath("workspace-name", "agent-name")
		if err == nil {
			t.Error("expected error for long socket path, got nil")
		}
	})
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

		cfg := pm.generateConfig(agent, rc, nil)

		if cfg.Metadata.Name != "my-agent" {
			t.Errorf("expected Name=my-agent, got %q", cfg.Metadata.Name)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "you are helpful") {
			t.Errorf("expected SystemPrompt to contain original prompt, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "<identity>") {
			t.Errorf("expected SystemPrompt to include identity section, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "You are my-agent") {
			t.Errorf("expected SystemPrompt to include agentrun name, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, `workspace "ws1"`) {
			t.Errorf("expected SystemPrompt to include workspace name, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "<"+pkgariapi.WorkspaceMeshName+">") {
			t.Errorf("expected SystemPrompt to include workspace mesh section, got %q", cfg.Session.SystemPrompt)
		}
		if cfg.Process.Command != "/usr/bin/mockagent" {
			t.Errorf("expected Command=/usr/bin/mockagent, got %q", cfg.Process.Command)
		}
		if len(cfg.Session.McpServers) != 1 {
			t.Errorf("expected 1 MCP server, got %d", len(cfg.Session.McpServers))
		} else if cfg.Session.McpServers[0].Name != pkgariapi.WorkspaceMeshName {
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

		cfg := pm.generateConfig(agent, rc, nil)
		if !strings.Contains(cfg.Session.SystemPrompt, "<identity>") {
			t.Errorf("expected identity section, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "You are bare-agent") {
			t.Errorf("expected identity to include agentrun name, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "<"+pkgariapi.WorkspaceMeshName+">") {
			t.Errorf("expected workspace mesh prompt, got %q", cfg.Session.SystemPrompt)
		}
	})

	t.Run("workspace feature overrides disable prompt and mcp injection", func(t *testing.T) {
		ws := &pkgariapi.Workspace{
			Spec: pkgariapi.WorkspaceSpec{
				Features: map[string]bool{
					FeatureWorkspaceMesh: false,
				},
			},
		}
		agent := &pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{
				Workspace: "ws1",
				Name:      "plain-agent",
			},
			Spec: pkgariapi.AgentRunSpec{
				Agent:        "mockagent",
				SystemPrompt: "keep it plain",
			},
		}

		cfg := pm.generateConfig(agent, rc, ws)
		if !strings.Contains(cfg.Session.SystemPrompt, "<identity>") {
			t.Errorf("expected identity section even with features disabled, got %q", cfg.Session.SystemPrompt)
		}
		if !strings.Contains(cfg.Session.SystemPrompt, "keep it plain") {
			t.Errorf("expected user prompt in SystemPrompt, got %q", cfg.Session.SystemPrompt)
		}
		if strings.Contains(cfg.Session.SystemPrompt, "<"+pkgariapi.WorkspaceMeshName+">") {
			t.Errorf("expected no workspace mesh when disabled, got %q", cfg.Session.SystemPrompt)
		}
		if len(cfg.Session.McpServers) != 0 {
			t.Errorf("expected no MCP servers when WorkspaceMesh disabled, got %d", len(cfg.Session.McpServers))
		}
	})
}

// TestKillRun_TerminatesProcessGroup verifies that killRun signals the entire
// process group spawned by an agent-run, not just the leader. This is the
// regression test for the leak that left bunx/cfuse/MCP descendants running
// after agentrun stop/delete (PPID reparented to 1).
func TestKillRun_TerminatesProcessGroup(t *testing.T) {
	// Spawn a parent that forks a long-running grandchild and waits on it.
	// `setsid` would also work; using Setpgid here mirrors forkRun exactly.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("/bin/sh", "-c",
		"sleep 600 & echo $! > "+pidFile+"; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}

	// Track parent exit so killRun can drain Done.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	// Wait for the child to write its PID.
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil && len(data) > 0 {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				childPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("child PID never written")
	}

	// Sanity-check pgid setup: child must share parent's pgid.
	parentPgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid parent: %v", err)
	}
	childPgid, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("getpgid child: %v", err)
	}
	if parentPgid != childPgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("pgid setup wrong: parent=%d child=%d", parentPgid, childPgid)
	}

	// Run the function under test.
	pm := &ProcessManager{logger: slog.Default()}
	pm.killRun(&RunProcess{
		AgentKey: "test/test",
		PID:      cmd.Process.Pid,
		Cmd:      cmd,
		Done:     done,
	})

	// Parent should be reaped (Done closed).
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("parent never exited after killRun")
	}

	// Child must also be gone — this is the regression: previously the leader
	// got SIGINT/SIGKILL but the child kept running until reboot.
	// Allow a brief moment for SIGKILL propagation through the group.
	for i := 0; i < 50; i++ {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatalf("child PID %d still alive after killRun — process group not signaled", childPID)
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
