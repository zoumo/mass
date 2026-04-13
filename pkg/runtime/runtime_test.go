package runtime_test

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

	pkgruntime "github.com/zoumo/oar/pkg/runtime"
	"github.com/zoumo/oar/api"
	apiruntime "github.com/zoumo/oar/api/runtime"
)

var mockAgentBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mockagent-bin-*")
	if err != nil {
		panic("failed to create temp dir for mock agent binary: " + err.Error())
	}
	// Determine repo root: tests run from pkg/runtime/, so go up two levels.
	_, filename, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..")

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
		Metadata:   apiruntime.Metadata{Name: name},
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

// newManager creates a bundle dir with a workspace subdir and a separate state
// dir, then returns a Manager wired to both. Dirs are cleaned up via t.Cleanup.
func newManager(t *testing.T, cfg apiruntime.Config) *pkgruntime.Manager {
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
	return pkgruntime.New(cfg, bundleDir, stateDir, slog.Default())
}

func (s *RuntimeSuite) TestCreate_ReachesCreatedState() {
	mgr := newManager(s.T(), newTestConfig("test-create"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.Require().NoError(mgr.Create(ctx))

	state, err := mgr.GetState()
	s.Require().NoError(err)
	s.Equal(api.StatusIdle, state.Status)
	s.Positive(state.PID)

	// Kill process externally and verify state transitions to stopped.
	proc, err := os.FindProcess(state.PID)
	s.Require().NoError(err)
	_ = proc.Signal(syscall.SIGKILL)

	// Wait for background goroutine to write stopped state.
	s.Require().Eventually(func() bool {
		st, err := mgr.GetState()
		return err == nil && st.Status == api.StatusStopped
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
	s.Equal(api.StatusStopped, state.Status)
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

	mgr := pkgruntime.New(cfg, bundleDir, stateDir, slog.Default())

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
