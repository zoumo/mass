package acp_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	acpruntime "github.com/zoumo/oar/pkg/shim/runtime/acp"
	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
	spec "github.com/zoumo/oar/pkg/runtime-spec"
)

var mockAgentBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mockagent-bin-*")
	if err != nil {
		panic("failed to create temp dir for mock agent binary: " + err.Error())
	}
	// Determine repo root: tests run from pkg/shim/runtime/acp/, so go up four levels.
	_, filename, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..", "..")

	binPath := filepath.Join(tmpDir, "mockagent")
	cmd := exec.Command("go", "build", "-o", binPath,
		"github.com/zoumo/oar/internal/testutil/mockagent")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build mock agent binary: " + err.Error())
	}

	mockAgentBin = binPath
	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

// RuntimeSuite exercises Manager.Create, Kill, Delete, and GetState.
type RuntimeSuite struct {
	suite.Suite
}

func TestRuntimeSuite(t *testing.T) {
	suite.Run(t, new(RuntimeSuite))
}

// newTestConfig returns a Config with agentRoot.path = "workspace".
// The caller is responsible for creating the bundle dir and workspace subdir.
func newTestConfig(name string) apiruntime.Config {
	return apiruntime.Config{
		OarVersion: "0.1.0",
		Metadata: apiruntime.Metadata{Name: name},
		AgentRoot:  apiruntime.AgentRoot{Path: "workspace"},
		AcpAgent: apiruntime.AcpAgent{
			Process: apiruntime.AcpProcess{
				Command: mockAgentBin,
				Args:    []string{},
			},
		},
		Permissions: apiruntime.ApproveAll,
	}
}

// newManagerWithStateDir creates a bundle dir with a workspace subdir and a
// separate state dir, then returns a Manager wired to both plus the stateDir
// path. Dirs are cleaned up via t.Cleanup.
func newManagerWithStateDir(t *testing.T, cfg apiruntime.Config) (*acpruntime.Manager, string) {
	t.Helper()
	bundleDir, err := os.MkdirTemp("", "oad-bundle-")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(bundleDir, cfg.AgentRoot.Path), 0o755))

	stateDir, err := os.MkdirTemp("", "oad-state-")
	require.NoError(t, err)

	t.Cleanup(func() {
		os.RemoveAll(bundleDir)
		os.RemoveAll(stateDir)
	})
	return acpruntime.New(cfg, bundleDir, stateDir, slog.Default()), stateDir
}

// newManager creates a bundle dir with a workspace subdir and a separate state
// dir, then returns a Manager wired to both. Dirs are cleaned up via t.Cleanup.
func newManager(t *testing.T, cfg apiruntime.Config) *acpruntime.Manager {
	t.Helper()
	mgr, _ := newManagerWithStateDir(t, cfg)
	return mgr
}

func (s *RuntimeSuite) TestCreate_ReachesCreatedState() {
	mgr := newManager(s.T(), newTestConfig("test-create"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusIdle, state.Status)
	s.Positive(state.PID)

	// Kill process externally and verify state transitions to stopped.
	proc, err := os.FindProcess(state.PID)
	s.Require().NoError(err)
	_ = proc.Signal(syscall.SIGKILL)

	// Wait for background goroutine to write stopped state.
	s.Require().Eventually(func() bool {
		st, err := mgr.GetState()
		return err == nil && st.Status == apiruntime.StatusStopped
	}, 10*time.Second, 100*time.Millisecond, "expected status=stopped after SIGKILL")
}

func (s *RuntimeSuite) TestKill_TransitionsToStopped() {
	mgr := newManager(s.T(), newTestConfig("test-kill"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))
	s.Require().NoError(mgr.Kill(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusStopped, state.Status)
}

func (s *RuntimeSuite) TestDelete_RemovesStateDir() {
	bundleDir, err := os.MkdirTemp("", "oad-bundle-")
	s.Require().NoError(err)
	cfg := newTestConfig("test-delete")
	s.Require().NoError(os.MkdirAll(filepath.Join(bundleDir, cfg.AgentRoot.Path), 0o755))

	stateDir, err := os.MkdirTemp("", "oad-state-")
	s.Require().NoError(err)
	// Note: do NOT register cleanup for stateDir — we're testing that Delete() removes it.
	s.T().Cleanup(func() { os.RemoveAll(bundleDir) })

	mgr := acpruntime.New(cfg, bundleDir, stateDir, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))
	s.Require().NoError(mgr.Kill(ctx))
	s.Require().NoError(mgr.Delete())

	_, err = os.Stat(stateDir)
	s.True(os.IsNotExist(err), "expected stateDir to be removed after Delete()")
}

func (s *RuntimeSuite) TestPrompt_ReceivesSessionUpdates() {
	mgr := newManager(s.T(), newTestConfig("test-prompt"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	resp, err := mgr.Prompt(ctx, []acp.ContentBlock{
		{Text: &acp.ContentBlockText{Text: "hello"}},
	})
	s.Require().NoError(err)
	s.Equal(acp.StopReasonEndTurn, resp.StopReason)

	// Drain events emitted during the turn (mock agent sends 1).
	events := mgr.Events()
	var notifications []acp.SessionNotification
	timeout := time.After(5 * time.Second)
drain:
	for {
		select {
		case n := <-events:
			notifications = append(notifications, n)
			if len(notifications) >= 1 {
				break drain
			}
		case <-timeout:
			break drain
		}
	}

	s.Require().Len(notifications, 1)
	chunk := notifications[0].Update.AgentMessageChunk
	s.Require().NotNil(chunk)
	s.Equal("mock response", chunk.Content.Text.Text)

	s.Require().NoError(mgr.Kill(ctx))
}

func (s *RuntimeSuite) TestCancel_SendsCancelToAgent() {
	mgr := newManager(s.T(), newTestConfig("test-cancel"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	// Cancel must not error on a live session.
	err := mgr.Cancel(ctx)
	s.Require().NoError(err)

	s.Require().NoError(mgr.Kill(ctx))
}

func (s *RuntimeSuite) TestCreate_FailsWithBadCommand() {
	cfg := newTestConfig("test-bad-cmd")
	cfg.AcpAgent.Process.Command = "/nonexistent/agent/binary"
	mgr := newManager(s.T(), cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Create(ctx)
	s.Error(err, "expected error when command does not exist")
}

// writeSessionToStateDir injects a recognizable Session into the existing
// state.json via read-modify-write. This simulates what S05's
// bootstrap-capture will do — writing Session metadata before Kill/process-exit.
func writeSessionToStateDir(t *testing.T, stateDir string) {
	t.Helper()
	st, err := spec.ReadState(stateDir)
	require.NoError(t, err)
	st.Session = &apiruntime.SessionState{
		AgentInfo: &apiruntime.AgentInfo{
			Name:    "test-agent",
			Version: "1.0.0",
		},
		AvailableCommands: []apiruntime.AvailableCommand{
			{Name: "run", Description: "Run the agent"},
		},
	}
	require.NoError(t, spec.WriteState(stateDir, st))
}

func (s *RuntimeSuite) TestKill_PreservesSession() {
	mgr, stateDir := newManagerWithStateDir(s.T(), newTestConfig("test-kill-session"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusIdle, state.Status)

	// Inject Session into state.json (simulates bootstrap-capture from S05).
	writeSessionToStateDir(s.T(), stateDir)

	state, err = mgr.GetState()
	s.Require().NoError(err)
	s.Require().NotNil(state.Session, "Session should be present after injection")

	// Kill should transition to stopped without clobbering Session.
	s.Require().NoError(mgr.Kill(ctx))

	state, err = mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusStopped, state.Status)
	s.Require().NotNil(state.Session, "Session must survive Kill()")
	s.Equal("test-agent", state.Session.AgentInfo.Name)
	s.NotEmpty(state.UpdatedAt, "UpdatedAt must be set after Kill")
	_, parseErr := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	s.NoError(parseErr, "UpdatedAt must be valid RFC3339Nano")
}

func (s *RuntimeSuite) TestProcessExit_PreservesSession() {
	mgr, stateDir := newManagerWithStateDir(s.T(), newTestConfig("test-exit-session"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	// Inject Session into state.json.
	writeSessionToStateDir(s.T(), stateDir)

	// Kill process externally with SIGKILL (same pattern as TestCreate_ReachesCreatedState).
	state, err := mgr.GetState()
	s.Require().NoError(err)
	proc, err := os.FindProcess(state.PID)
	s.Require().NoError(err)
	_ = proc.Signal(syscall.SIGKILL)

	// Wait for background goroutine to write stopped state.
	s.Require().Eventually(func() bool {
		st, err := mgr.GetState()
		return err == nil && st.Status == apiruntime.StatusStopped
	}, 10*time.Second, 100*time.Millisecond, "expected status=stopped after SIGKILL")

	state, err = mgr.GetState()
	s.Require().NoError(err)
	s.Require().NotNil(state.Session, "Session must survive external SIGKILL / process-exit")
	s.Equal("test-agent", state.Session.AgentInfo.Name)
	s.NotEmpty(state.UpdatedAt, "UpdatedAt must be set after process-exit")
}

func (s *RuntimeSuite) TestCreate_PopulatesSession() {
	mgr := newManager(s.T(), newTestConfig("test-session-capture"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusIdle, state.Status)

	// Verify Session was populated from InitializeResponse at bootstrap-complete.
	s.Require().NotNil(state.Session, "Session must be populated after Create()")

	// AgentInfo assertions — mockagent returns Name="mockagent", Version="0.1.0".
	s.Require().NotNil(state.Session.AgentInfo, "Session.AgentInfo must not be nil")
	s.Equal("mockagent", state.Session.AgentInfo.Name)
	s.Equal("0.1.0", state.Session.AgentInfo.Version)

	// Capabilities assertions — mockagent returns LoadSession=true, Sse=true, Image=true.
	s.Require().NotNil(state.Session.Capabilities, "Session.Capabilities must not be nil")
	s.True(state.Session.Capabilities.LoadSession, "LoadSession should be true")
	s.True(state.Session.Capabilities.McpCapabilities.Sse, "McpCapabilities.Sse should be true")
	s.True(state.Session.Capabilities.PromptCapabilities.Image, "PromptCapabilities.Image should be true")

	// Kill and verify Session survives (leverages S03's closure pattern).
	s.Require().NoError(mgr.Kill(ctx))

	state, err = mgr.GetState()
	s.Require().NoError(err)
	s.Equal(apiruntime.StatusStopped, state.Status)
	s.Require().NotNil(state.Session, "Session must survive Kill()")
	s.Equal("mockagent", state.Session.AgentInfo.Name)
	s.True(state.Session.Capabilities.LoadSession, "LoadSession must survive Kill()")
}

func (s *RuntimeSuite) TestWriteState_SetsUpdatedAt() {
	mgr := newManager(s.T(), newTestConfig("test-updatedat"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.NotEmpty(state.UpdatedAt, "UpdatedAt must be set after Create")
	createTime, parseErr := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	s.Require().NoError(parseErr, "UpdatedAt must be valid RFC3339Nano after Create")

	s.Require().NoError(mgr.Kill(ctx))

	state, err = mgr.GetState()
	s.Require().NoError(err)
	s.NotEmpty(state.UpdatedAt, "UpdatedAt must be set after Kill")
	killTime, parseErr := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	s.Require().NoError(parseErr, "UpdatedAt must be valid RFC3339Nano after Kill")

	s.True(killTime.After(createTime) || killTime.Equal(createTime),
		"UpdatedAt after Kill (%s) must be >= after Create (%s)", killTime, createTime)
}
