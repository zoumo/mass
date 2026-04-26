// Package testutil provides shared helpers for integration tests.
// All helpers accept *testing.T and call t.Fatalf on errors.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// socketCounter provides unique socket paths for each test.
var socketCounter int64

// SetupMassTest starts mass daemon and returns context, client, and cleanup function.
// It registers the "mockagent" runtime via agent/create.
func SetupMassTest(t *testing.T) (context.Context, context.CancelFunc, ariclient.Client, func()) {
	t.Helper()
	counter := atomic.AddInt64(&socketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/mass-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	os.Remove(socketPath)

	massBin, err := filepath.Abs("../../../bin/mass")
	if err != nil {
		t.Fatalf("failed to get mass path: %v", err)
	}
	mockagentBin, err := filepath.Abs("../../../bin/mockagent")
	if err != nil {
		t.Fatalf("failed to get mockagent path: %v", err)
	}

	for _, bin := range []string{massBin, mockagentBin} {
		if _, err := os.Stat(bin); os.IsNotExist(err) {
			t.Fatalf("binary not found: %s (run: make build)", bin)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	massCmd := exec.CommandContext(ctx, massBin, "daemon", "start", "--root", rootDir)
	massCmd.Stdout = os.Stdout
	massCmd.Stderr = os.Stderr

	if err := massCmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start mass: %v", err)
	}
	t.Logf("mass started with PID %d (root=%s)", massCmd.Process.Pid, rootDir)

	if err := WaitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("socket not ready: %v", err)
	}

	client, err := ariclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "mockagent"},
		Spec:     pkgariapi.AgentSpec{Command: mockagentBin},
	}
	if err := client.Create(ctx, &ag); err != nil {
		cancel()
		client.Close()
		t.Fatalf("failed to register mockagent runtime: %v", err)
	}
	t.Logf("runtime registered: name=%s command=%s", ag.Metadata.Name, ag.Spec.Command)

	cleanup := func() {
		client.Close()
		if massCmd.Process != nil {
			_ = massCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- massCmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = massCmd.Process.Kill()
				<-done
			}
			t.Log("mass stopped")
		}
		_ = exec.Command("pkill", "-f", rootDir).Run()
		os.Remove(socketPath)
		os.RemoveAll(rootDir)
	}

	return ctx, cancel, client, cleanup
}

// SetupMassTestWithRuntimeClass creates a mass instance and registers a custom runtime.
func SetupMassTestWithRuntimeClass(
	t *testing.T,
	runtimeClassName string,
	spec pkgariapi.AgentSpec,
) (context.Context, context.CancelFunc, ariclient.Client, func()) {
	t.Helper()

	counter := atomic.AddInt64(&socketCounter, 1)
	rootDir := fmt.Sprintf("/tmp/mass-%d-%d", os.Getpid(), counter)
	socketPath := filepath.Join(rootDir, "mass.sock")

	os.Remove(socketPath)

	massBin, err := filepath.Abs("../../../bin/mass")
	if err != nil {
		t.Fatalf("failed to get mass path: %v", err)
	}

	if _, err := os.Stat(massBin); os.IsNotExist(err) {
		t.Fatalf("binary not found: %s (run: make build)", massBin)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)

	massCmd := exec.CommandContext(ctx, massBin, "daemon", "start", "--root", rootDir)
	massCmd.Stdout = os.Stdout
	massCmd.Stderr = os.Stderr

	if err := massCmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start mass: %v", err)
	}
	t.Logf("mass started with PID %d (root=%s)", massCmd.Process.Pid, rootDir)

	if err := WaitForSocket(socketPath, 10*time.Second); err != nil {
		cancel()
		_ = massCmd.Process.Kill()
		t.Fatalf("mass socket not ready: %v", err)
	}

	client, err := ariclient.Dial(ctx, socketPath)
	if err != nil {
		cancel()
		_ = massCmd.Process.Kill()
		t.Fatalf("failed to create ARI client: %v", err)
	}

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: runtimeClassName},
		Spec:     spec,
	}
	if err := client.Create(ctx, &ag); err != nil {
		cancel()
		client.Close()
		_ = massCmd.Process.Kill()
		t.Fatalf("failed to register runtime %q: %v", runtimeClassName, err)
	}
	t.Logf("runtime registered: name=%s command=%s", ag.Metadata.Name, ag.Spec.Command)

	cleanup := func() {
		client.Close()
		if massCmd.Process != nil {
			_ = massCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- massCmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = massCmd.Process.Kill()
				<-done
			}
			t.Log("mass stopped")
		}
		_ = exec.Command("pkill", "-f", rootDir).Run()
		os.Remove(socketPath)
		os.RemoveAll(rootDir)
	}

	return ctx, cancel, client, cleanup
}

// WaitForSocket waits for a Unix socket to be ready.
func WaitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socket %s not ready after %v", socketPath, timeout)
}

// CreateTestWorkspace calls workspace/create and polls until phase=="ready".
func CreateTestWorkspace(t *testing.T, ctx context.Context, client ariclient.Client, name string) string {
	t.Helper()
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.WorkspaceSpec{Source: json.RawMessage(`{"type":"emptyDir"}`)},
	}
	if err := client.Create(ctx, &ws); err != nil {
		t.Fatalf("workspace/create (name=%s): %v", name, err)
	}
	t.Logf("workspace create dispatched: name=%s phase=%s", ws.Metadata.Name, ws.Status.Phase)

	key := pkgariapi.ObjectKey{Name: name}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var got pkgariapi.Workspace
		if err := client.Get(ctx, key, &got); err != nil {
			t.Logf("workspace/get (%s): %v (retrying)", name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if got.Status.Phase == pkgariapi.WorkspacePhaseReady {
			t.Logf("workspace ready: name=%s", name)
			return name
		}
		if got.Status.Phase == pkgariapi.WorkspacePhaseError {
			t.Fatalf("workspace %s reached error phase", name)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("workspace %s did not reach phase=ready within 15s", name)
	return name
}

// DeleteTestWorkspace removes a workspace. Best-effort cleanup.
func DeleteTestWorkspace(t *testing.T, ctx context.Context, client ariclient.Client, name string) {
	t.Helper()
	if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: name}, &pkgariapi.Workspace{}); err != nil {
		t.Logf("workspace/delete (name=%s): %v (ignored)", name, err)
	}
}

// WaitForAgentState polls agentrun/get until the agent reaches the desired state.
func WaitForAgentState(
	t *testing.T,
	ctx context.Context,
	client ariclient.Client,
	workspace, name, wantState string,
	timeout time.Duration,
) pkgariapi.AgentRun {
	t.Helper()
	return WaitForAgentStateOneOf(t, ctx, client, workspace, name, []string{wantState}, timeout)
}

// WaitForAgentStateOneOf polls agentrun/get until the agent reaches any of the desired states.
func WaitForAgentStateOneOf(
	t *testing.T,
	ctx context.Context,
	client ariclient.Client,
	workspace, name string,
	wantStates []string,
	timeout time.Duration,
) pkgariapi.AgentRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	key := pkgariapi.ObjectKey{Workspace: workspace, Name: name}
	var ar pkgariapi.AgentRun
	for time.Now().Before(deadline) {
		if err := client.Get(ctx, key, &ar); err != nil {
			t.Logf("agentrun/get (%s/%s): %v (retrying)", workspace, name, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, want := range wantStates {
			if string(ar.Status.Status) == want {
				return ar
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("agent %s/%s did not reach state(s) %v within %v (last state: %q)",
		workspace, name, wantStates, timeout, ar.Status.Status)
	return ar
}

// CreateAgentAndWait calls agentrun/create and polls until state=="idle".
func CreateAgentAndWait(t *testing.T, ctx context.Context, client ariclient.Client, workspace, name, agentDef string) pkgariapi.AgentRun {
	t.Helper()
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: workspace, Name: name},
		Spec:     pkgariapi.AgentRunSpec{Agent: agentDef},
	}
	if err := client.Create(ctx, &ar); err != nil {
		t.Fatalf("agentrun/create (workspace=%s name=%s): %v", workspace, name, err)
	}
	t.Logf("agent create dispatched: workspace=%s name=%s state=%s",
		ar.Metadata.Workspace, ar.Metadata.Name, ar.Status.Status)
	return WaitForAgentState(t, ctx, client, workspace, name, "idle", 15*time.Second)
}

// StopAndDeleteAgent stops and then deletes an agent. Best-effort cleanup.
func StopAndDeleteAgent(t *testing.T, ctx context.Context, client ariclient.Client, workspace, name string) {
	t.Helper()
	key := pkgariapi.ObjectKey{Workspace: workspace, Name: name}
	if err := client.AgentRuns().Stop(ctx, key); err != nil {
		t.Logf("agentrun/stop (%s/%s): %v (ignored)", workspace, name, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ar pkgariapi.AgentRun
		if err := client.Get(ctx, key, &ar); err != nil {
			break
		}
		if ar.Status.Status == "stopped" || ar.Status.Status == "error" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
		t.Logf("agentrun/delete (%s/%s): %v (ignored)", workspace, name, err)
	}
}

// StartMass launches mass with --root rootDir, waits for the socket.
// Caller is responsible for cleanup.
func StartMass(t *testing.T, ctx context.Context, massBin, rootDir, socketPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, massBin, "daemon", "start", "--root", rootDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start mass: %v", err)
	}
	t.Logf("mass started with PID %d (root=%s)", cmd.Process.Pid, rootDir)

	if err := WaitForSocket(socketPath, 10*time.Second); err != nil {
		t.Fatalf("socket not ready: %v", err)
	}
	return cmd
}

// StopMass gracefully kills mass with SIGINT and waits for exit.
func StopMass(t *testing.T, cmd *exec.Cmd, socketPath string) {
	t.Helper()
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
		t.Log("mass stopped")
	}
	_ = exec.Command("pkill", "-f", filepath.Dir(socketPath)).Run()
	os.Remove(socketPath)
}

// MassBinPath returns the absolute path to the mass binary relative to a test subdirectory.
func MassBinPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../bin/mass")
	if err != nil {
		t.Fatalf("failed to get mass path: %v", err)
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		t.Fatalf("binary not found: %s (run: make build)", p)
	}
	return p
}

// MockagentBinPath returns the absolute path to the mockagent binary.
func MockagentBinPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../bin/mockagent")
	if err != nil {
		t.Fatalf("failed to get mockagent path: %v", err)
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		t.Fatalf("binary not found: %s (run: make build)", p)
	}
	return p
}

// NewSocketCounter returns a unique counter value for socket path generation.
func NewSocketCounter() int64 {
	return atomic.AddInt64(&socketCounter, 1)
}
