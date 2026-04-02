package rpc_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	pkgruntime "github.com/open-agent-d/open-agent-d/pkg/runtime"
	"github.com/open-agent-d/open-agent-d/pkg/rpc"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"
)

// ────────────────────────────────────────────────────────────────────────────
// TestMain: build the mockagent binary once for all tests.
// ────────────────────────────────────────────────────────────────────────────

var mockAgentBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mockagent-rpc-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	defer os.RemoveAll(tmpDir)

	// Determine repo root: tests run from pkg/rpc/, so go up two levels.
	_, filename, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..")

	binPath := filepath.Join(tmpDir, "mockagent")
	cmd := exec.Command("go", "build", "-o", binPath,
		"github.com/open-agent-d/open-agent-d/internal/testutil/mockagent")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build mock agent binary: " + err.Error())
	}

	mockAgentBin = binPath
	os.Exit(m.Run())
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// testConfig returns a minimal spec.Config pointing at mockAgentBin.
func testConfig(name string) spec.Config {
	return spec.Config{
		OarVersion: "0.1.0",
		Metadata:   spec.Metadata{Name: name},
		AgentRoot:  spec.AgentRoot{Path: "workspace"},
		AcpAgent: spec.AcpAgent{
			Process: spec.AcpProcess{
				Command: mockAgentBin,
				Args:    []string{},
			},
		},
		Permissions: spec.ApproveAll,
	}
}

// notifHandler is a jsonrpc2 handler that collects inbound $/event
// notifications into a channel so the test can assert on them.
type notifHandler struct {
	mu   sync.Mutex
	evts []rpc.EventNotification
	ch   chan rpc.EventNotification
}

func newNotifHandler() *notifHandler {
	return &notifHandler{ch: make(chan rpc.EventNotification, 128)}
}

// Handle implements jsonrpc2.Handler.  Only $/event notifications are
// meaningful here; everything else is silently dropped.
func (h *notifHandler) Handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif || req.Method != "$/event" {
		return
	}
	var n rpc.EventNotification
	if req.Params == nil {
		return
	}
	if err := json.Unmarshal(*req.Params, &n); err != nil {
		return
	}
	h.mu.Lock()
	h.evts = append(h.evts, n)
	h.mu.Unlock()
	select {
	case h.ch <- n:
	default:
	}
}

// collect drains up to want notifications within timeout, then returns them.
func (h *notifHandler) collect(want int, timeout time.Duration) []rpc.EventNotification {
	var out []rpc.EventNotification
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case n := <-h.ch:
			out = append(out, n)
		case <-deadline:
			return out
		}
	}
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// serverHarness brings up Manager + Translator + Server for one test.
// ────────────────────────────────────────────────────────────────────────────

type serverHarness struct {
	mgr    *pkgruntime.Manager
	trans  *events.Translator
	srv    *rpc.Server
	socket string

	// serveErr receives the error from srv.Serve when the server exits.
	serveErr chan error

	// stateDir and bundleDir are managed manually to control cleanup ordering.
	// The socket lives inside stateDir (agent-shim.sock) — no separate sockDir.
	stateDir  string
	bundleDir string
}

func newServerHarness(t *testing.T) *serverHarness {
	t.Helper()

	// Use os.MkdirTemp with short prefixes so that:
	//   (a) socket path stays under macOS's 104-byte sun_path limit, and
	//   (b) we control cleanup ordering — kill the agent before removing
	//       stateDir to avoid a race where the runtime background goroutine
	//       writes state.json while t.TempDir's RemoveAll is running.
	bundleDir, err := os.MkdirTemp("", "oad-bnd-")
	require.NoError(t, err, "failed to create bundle dir")
	require.NoError(t, os.MkdirAll(filepath.Join(bundleDir, "workspace"), 0o755))

	stateDir, err := os.MkdirTemp("", "oad-st-")
	require.NoError(t, err, "failed to create state dir")

	// Socket lives inside stateDir — mirrors production layout and keeps
	// the path short enough to satisfy macOS's 104-byte sun_path limit.
	socketPath := spec.ShimSocketPath(stateDir)

	mgr := pkgruntime.New(testConfig(t.Name()), bundleDir, stateDir)

	// agentCtx drives exec.CommandContext — it must stay alive for the whole
	// test, otherwise the agent process is killed when Create's ctx is cancelled.
	agentCtx, agentCancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(agentCancel)

	require.NoError(t, mgr.Create(agentCtx))

	trans := events.NewTranslator(mgr.Events(), nil)
	trans.Start()

	srv := rpc.New(mgr, trans, socketPath, "" /* no log in tests */)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	// Wait until the socket file exists (server is accepting).
	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "server socket did not appear")

	h := &serverHarness{
		mgr:       mgr,
		trans:     trans,
		srv:       srv,
		socket:    socketPath,
		serveErr:  serveErr,
		stateDir:  stateDir,
		bundleDir: bundleDir,
	}

	// Cleanup: shut down the server and kill the agent, then wait for the
	// background cmd.Wait goroutine (runtime) to finish writing stopped state
	// before removing stateDir.  This prevents the TempDir-cleanup race on
	// macOS where RemoveAll returns ENOTEMPTY because the goroutine is still
	// writing state.json.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = srv.Shutdown(cleanupCtx)
		// Wait for Serve to exit so we know the socket is closed.
		select {
		case <-serveErr:
		case <-cleanupCtx.Done():
		}
		// Give the runtime background goroutine time to finish writing
		// state.json (it runs after cmd.Wait returns, which happens shortly
		// after Kill).
		require.Eventually(t, func() bool {
			st, err := mgr.GetState()
			return err == nil && st.Status == spec.StatusStopped
		}, 5*time.Second, 20*time.Millisecond,
			"expected agent status=stopped before cleanup")

		_ = os.RemoveAll(stateDir)
		_ = os.RemoveAll(bundleDir)
	})

	return h
}

// dial opens a jsonrpc2 client connection to the server.  The handler receives
// inbound notifications from the server.
func (h *serverHarness) dial(t *testing.T, handler jsonrpc2.Handler) *jsonrpc2.Conn {
	t.Helper()
	nc, err := net.Dial("unix", h.socket)
	require.NoError(t, err)
	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(handler))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// ────────────────────────────────────────────────────────────────────────────
// Integration test: all 5 RPC methods
// ────────────────────────────────────────────────────────────────────────────

// TestRPCServer_AllMethods exercises Subscribe, Prompt, GetState, Cancel, and
// Shutdown over the Unix socket in a single connected session.
func TestRPCServer_AllMethods(t *testing.T) {
	h := newServerHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	notifH := newNotifHandler()
	client := h.dial(t, notifH)

	// ── Subscribe ──────────────────────────────────────────────────────────
	// Subscribe must return nil result immediately; events arrive later.
	t.Run("Subscribe", func(t *testing.T) {
		var subResult interface{}
		err := client.Call(ctx, "Subscribe", nil, &subResult)
		require.NoError(t, err, "Subscribe should succeed")
		// nil result is expected
	})

	// ── Prompt ────────────────────────────────────────────────────────────
	// Prompt forwards text to the mock agent and returns stopReason="end_turn".
	// The mock agent emits two SessionNotifications which the Translator
	// converts to TextEvents and the server streams as $/event notifications.
	t.Run("Prompt", func(t *testing.T) {
		var promptResult rpc.PromptResult
		err := client.Call(ctx, "Prompt", rpc.PromptParams{Text: "hello"}, &promptResult)
		require.NoError(t, err, "Prompt should succeed")
		require.Equal(t, "end_turn", promptResult.StopReason, "expected end_turn stop reason")

		// Collect the two $/event notifications emitted by the mock agent.
		// Allow some extra time for the goroutine to fan-out.
		notifs := notifH.collect(2, 10*time.Second)
		require.GreaterOrEqual(t, len(notifs), 2,
			"expected at least 2 $/event notifications after Prompt, got %d", len(notifs))

		// Both should be text events.
		for i, n := range notifs[:2] {
			require.Equal(t, "text", n.Type,
				"notification %d: expected type=text, got %q", i, n.Type)
		}
	})

	// ── GetState ──────────────────────────────────────────────────────────
	// GetState returns the persisted state; status should be "created" because
	// the agent is idle after the Prompt completed.
	t.Run("GetState", func(t *testing.T) {
		var stateResult rpc.GetStateResult
		err := client.Call(ctx, "GetState", nil, &stateResult)
		require.NoError(t, err, "GetState should succeed")
		require.Equal(t, "created", stateResult.Status,
			"expected status=created, got %q", stateResult.Status)
		require.NotEmpty(t, stateResult.ID, "expected non-empty ID")
	})

	// ── Cancel ────────────────────────────────────────────────────────────
	// Cancel must not error on an idle session (mock agent accepts the notification).
	t.Run("Cancel", func(t *testing.T) {
		var cancelResult interface{}
		err := client.Call(ctx, "Cancel", nil, &cancelResult)
		require.NoError(t, err, "Cancel should succeed on idle session")
	})

	// ── Shutdown ──────────────────────────────────────────────────────────
	// Shutdown replies nil then closes the server.  After Shutdown, the server's
	// Serve goroutine should exit and the socket should become undiallable.
	t.Run("Shutdown", func(t *testing.T) {
		var shutdownResult interface{}
		err := client.Call(ctx, "Shutdown", nil, &shutdownResult)
		// Shutdown may return an error or ErrClosed as the server closes the
		// connection immediately after replying.  Either is acceptable.
		if err != nil {
			// ErrClosed or io.EOF is expected as server tears down the connection.
			t.Logf("Shutdown call returned (expected) error: %v", err)
		}

		// Serve should exit cleanly within a few seconds.
		select {
		case serveErr := <-h.serveErr:
			require.NoError(t, serveErr, "Server.Serve should exit without error after Shutdown")
		case <-time.After(10 * time.Second):
			t.Fatal("Server.Serve did not exit within 10s after Shutdown")
		}

		// The socket should no longer be diallable.
		require.Eventually(t, func() bool {
			c, err := net.Dial("unix", h.socket)
			if err != nil {
				return true
			}
			_ = c.Close()
			return false
		}, 3*time.Second, 50*time.Millisecond, "expected socket to be unavailable after Shutdown")
	})
}

// TestRPCServer_UnknownMethod verifies that an unknown method returns a
// JSON-RPC CodeMethodNotFound error rather than hanging.
func TestRPCServer_UnknownMethod(t *testing.T) {
	h := newServerHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, newNotifHandler())

	var result interface{}
	err := client.Call(ctx, "DoesNotExist", nil, &result)
	require.Error(t, err, "expected error for unknown method")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeMethodNotFound), int64(rpcErr.Code),
		"expected CodeMethodNotFound, got %d", rpcErr.Code)
}

// TestRPCServer_PromptMissingParams verifies that Prompt with nil params
// returns CodeInvalidParams.
func TestRPCServer_PromptMissingParams(t *testing.T) {
	h := newServerHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, newNotifHandler())

	// Send Prompt with no params (nil).
	var result rpc.PromptResult
	err := client.Call(ctx, "Prompt", nil, &result)
	require.Error(t, err, "expected error for missing Prompt params")

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(jsonrpc2.CodeInvalidParams), int64(rpcErr.Code),
		"expected CodeInvalidParams, got %d", rpcErr.Code)
}

// _ suppresses "declared and not used" for the harness field.
var _ = (*serverHarness)(nil)
