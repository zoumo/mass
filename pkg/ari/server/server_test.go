// Package server_test tests the ARI JSON-RPC server via a real Unix socket.
package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/agentd"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	ariserver "github.com/zoumo/mass/pkg/ari/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
	apishim "github.com/zoumo/mass/pkg/shim/api"
	shimclient "github.com/zoumo/mass/pkg/shim/client"
	"github.com/zoumo/mass/pkg/agentd/store"
	"github.com/zoumo/mass/pkg/workspace"
)

// ────────────────────────────────────────────────────────────────────────────
// Test harness helpers
// ────────────────────────────────────────────────────────────────────────────

// testEnv holds all components for a running ARI test server.
type testEnv struct {
	srv       *jsonrpc.Server
	client    *ariclient.RawClient
	store     *store.Store
	processes *agentd.ProcessManager
	agents    *agentd.AgentRunManager
}

// shortSockPath returns a process-unique Unix socket path safe for macOS.
// macOS has a 104-char limit; /tmp/mass-<pid>-ari.sock is well within that.
func shortSockPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("/tmp/mass-%d-%d-ari.sock", os.Getpid(), time.Now().UnixNano()%100000)
}

// newTestServer creates a full ARI test server and returns the env.
// The server is started in a goroutine; cleanup shuts it down.
func newTestServer(t *testing.T) *testEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("/tmp", "mass-ari-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := store.NewStore(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	mgr := workspace.NewWorkspaceManager()

	agents := agentd.NewAgentRunManager(store, slog.Default())
	processes := agentd.NewProcessManager(agents, store, filepath.Join(tmpDir, "mass.sock"), filepath.Join(tmpDir, "bundles"), slog.Default(), "info", "pretty")

	sockPath := shortSockPath(t)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	svc := ariserver.New(mgr, agents, processes, store, tmpDir, slog.Default())
	srv := jsonrpc.NewServer(slog.Default())
	ariserver.Register(srv, svc)

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Wait for socket to appear.
	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "server socket did not appear")

	client, err := ariclient.NewRawClient(sockPath)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
		_ = ln.Close()
		_ = srv.Shutdown(context.Background())
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
		}
	})

	return &testEnv{
		srv:       srv,
		client:    client,
		store:     store,
		processes: processes,
		agents:    agents,
	}
}

// callRaw calls a JSON-RPC method and returns the raw result bytes and error.
// An RPC error is returned as an error value wrapping the code + message.
func callRaw(t *testing.T, client *ariclient.RawClient, method string, params any) (json.RawMessage, error) {
	t.Helper()
	var result json.RawMessage
	err := client.Call(method, params, &result)
	return result, err
}

// waitUntilWorkspaceReady polls workspace/get until phase == "ready".
func waitUntilWorkspaceReady(t *testing.T, client *ariclient.RawClient, wsName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var ws pkgariapi.Workspace
		err := client.Call("workspace/get", pkgariapi.ObjectKey{Name: wsName}, &ws)
		return err == nil && ws.Status.Phase == "ready"
	}, 5*time.Second, 50*time.Millisecond, "workspace %s did not become ready", wsName)
}

// createAndWaitWorkspace creates a workspace with emptyDir source and polls until ready.
func createAndWaitWorkspace(t *testing.T, client *ariclient.RawClient, name string) pkgariapi.Workspace {
	t.Helper()
	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.WorkspaceSpec{Source: json.RawMessage(`{"type":"emptyDir"}`)},
	}
	var createResult pkgariapi.Workspace
	require.NoError(t, client.Call("workspace/create", ws, &createResult))
	assert.Equal(t, pkgariapi.WorkspacePhasePending, createResult.Status.Phase)

	waitUntilWorkspaceReady(t, client, name)

	var getResult pkgariapi.Workspace
	require.NoError(t, client.Call("workspace/get", pkgariapi.ObjectKey{Name: name}, &getResult))
	return getResult
}

// seedAgent inserts a raw agent record directly into the store, bypassing the
// background shim start. Used to prime DB state for handler-only tests.
func seedAgent(t *testing.T, store *store.Store, wsName, name string, state apiruntime.Status) {
	t.Helper()
	err := store.CreateAgentRun(context.Background(), &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Name:      name,
			Workspace: wsName,
		},
		Spec: pkgariapi.AgentRunSpec{Agent: "default"},
		Status: pkgariapi.AgentRunStatus{
			State: state,
		},
	})
	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Workspace tests
// ────────────────────────────────────────────────────────────────────────────

func TestWorkspaceCreatePending(t *testing.T) {
	env := newTestServer(t)

	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "w1"},
		Spec:     pkgariapi.WorkspaceSpec{Source: json.RawMessage(`{"type":"emptyDir"}`)},
	}
	var result pkgariapi.Workspace
	err := env.client.Call("workspace/create", ws, &result)
	require.NoError(t, err)
	assert.Equal(t, "w1", result.Metadata.Name)
	assert.Equal(t, pkgariapi.WorkspacePhasePending, result.Status.Phase)
}

func TestWorkspaceGetReady(t *testing.T) {
	env := newTestServer(t)

	getResult := createAndWaitWorkspace(t, env.client, "w-ready")

	assert.Equal(t, pkgariapi.WorkspacePhaseReady, getResult.Status.Phase)
	assert.NotEmpty(t, getResult.Status.Path, "ready workspace must have a path")
}

func TestWorkspaceList(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "wl-1")
	createAndWaitWorkspace(t, env.client, "wl-2")

	var listResult pkgariapi.WorkspaceList
	require.NoError(t, env.client.Call("workspace/list", nil, &listResult))
	assert.GreaterOrEqual(t, len(listResult.Items), 2)

	names := make([]string, 0, len(listResult.Items))
	for _, w := range listResult.Items {
		names = append(names, w.Metadata.Name)
	}
	assert.Contains(t, names, "wl-1")
	assert.Contains(t, names, "wl-2")
}

func TestWorkspaceDelete(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "w-del")

	err := env.client.Call("workspace/delete", pkgariapi.ObjectKey{Name: "w-del"}, nil)
	require.NoError(t, err)

	// After delete: get should return an error (not found → -32602 or phase error).
	var getResult pkgariapi.Workspace
	getErr := env.client.Call("workspace/get", pkgariapi.ObjectKey{Name: "w-del"}, &getResult)
	// Either an RPC error (not found) or phase=="error" are acceptable.
	if getErr == nil {
		assert.Equal(t, pkgariapi.WorkspacePhaseError, getResult.Status.Phase)
	} else {
		assert.Contains(t, getErr.Error(), "-32602")
	}
}

func TestWorkspaceDeleteBlockedByAgent(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "w-blocked")
	seedAgent(t, env.store, "w-blocked", "blocker", apiruntime.StatusIdle)

	err := env.client.Call("workspace/delete", pkgariapi.ObjectKey{Name: "w-blocked"}, nil)
	require.Error(t, err, "workspace/delete must fail when an agent is present")
}

// ────────────────────────────────────────────────────────────────────────────
// Agent tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgentCreateReturnsCreating(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "ac-ws")

	// Call via raw to inspect JSON for absence of "agentId".
	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "ac-ws", Name: "my-agent"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
	}
	raw, err := callRaw(t, env.client, "agentrun/create", ar)
	require.NoError(t, err)

	var result pkgariapi.AgentRun
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, apiruntime.StatusCreating, result.Status.State)
	assert.Equal(t, "ac-ws", result.Metadata.Workspace)
	assert.Equal(t, "my-agent", result.Metadata.Name)

	// No agentId key in the response JSON.
	var rawMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &rawMap))
	_, hasAgentID := rawMap["agentId"]
	assert.False(t, hasAgentID, "agentId must not appear in agentrun/create response")
}

func TestAgentListAndGet(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "als-ws")

	seedAgent(t, env.store, "als-ws", "agent-idle", apiruntime.StatusIdle)
	seedAgent(t, env.store, "als-ws", "agent-stopped", apiruntime.StatusStopped)

	var listResult pkgariapi.AgentRunList
	require.NoError(t, env.client.Call("agentrun/list",
		pkgariapi.ListOptions{FieldSelector: map[string]string{"workspace": "als-ws"}},
		&listResult))
	assert.Len(t, listResult.Items, 2)

	// Verify agentrun/get returns correct state.
	var getResult pkgariapi.AgentRun
	require.NoError(t, env.client.Call("agentrun/get", pkgariapi.ObjectKey{
		Workspace: "als-ws",
		Name:      "agent-idle",
	}, &getResult))
	assert.Equal(t, apiruntime.StatusIdle, getResult.Status.State)
	assert.Equal(t, "als-ws", getResult.Metadata.Workspace)
	assert.Equal(t, "agent-idle", getResult.Metadata.Name)
}

func TestAgentPromptRejectedForBadState(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "pr-ws")

	seedAgent(t, env.store, "pr-ws", "agent-stopped", apiruntime.StatusStopped)
	seedAgent(t, env.store, "pr-ws", "agent-error", apiruntime.StatusError)
	seedAgent(t, env.store, "pr-ws", "agent-creating", apiruntime.StatusCreating)

	for _, name := range []string{"agent-stopped", "agent-error", "agent-creating"} {
		err := env.client.Call("agentrun/prompt", pkgariapi.AgentRunPromptParams{
			Workspace: "pr-ws",
			Name:      name,
			Prompt:    "hello",
		}, nil)
		require.Error(t, err, "agentrun/prompt for %s must return an error", name)
		assert.Contains(t, err.Error(), "not in idle state",
			"error for %s must mention 'not in idle state'", name)
	}
}

func TestAgentPromptRejectsEmptyPrompt(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "empty-prompt-ws")
	seedAgent(t, env.store, "empty-prompt-ws", "agent-idle", apiruntime.StatusIdle)

	err := env.client.Call("agentrun/prompt", pkgariapi.AgentRunPromptParams{
		Workspace: "empty-prompt-ws",
		Name:      "agent-idle",
		Prompt:    "",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-32602")

	agent, getErr := env.store.GetAgentRun(context.Background(), "empty-prompt-ws", "agent-idle")
	require.NoError(t, getErr)
	require.NotNil(t, agent)
	assert.Equal(t, apiruntime.StatusIdle, agent.Status.State)
}

func TestAgentPromptReservesBeforeAccepted(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "reserve-ws")

	agentName := "agent-reserve"
	seedAgent(t, env.store, "reserve-ws", agentName, apiruntime.StatusIdle)

	shimSrv, shimSock := newMiniShimServer(t)
	_ = shimSrv

	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", shimSock)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "mini shim socket not ready")

	shimClient, err := shimclient.Dial(context.Background(), shimSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shimClient.Close() })

	env.processes.InjectProcess("reserve-ws/"+agentName, &agentd.ShimProcess{
		AgentKey:   "reserve-ws/" + agentName,
		SocketPath: shimSock,
		Client:     shimClient,
		Events:     make(chan apishim.ShimEvent, 1024),
		Done:       make(chan struct{}),
	})

	var result pkgariapi.AgentRunPromptResult
	require.NoError(t, env.client.Call("agentrun/prompt", pkgariapi.AgentRunPromptParams{
		Workspace: "reserve-ws",
		Name:      agentName,
		Prompt:    "hello",
	}, &result))
	require.True(t, result.Accepted)

	var getResult pkgariapi.AgentRun
	require.NoError(t, env.client.Call("agentrun/get", pkgariapi.ObjectKey{
		Workspace: "reserve-ws",
		Name:      agentName,
	}, &getResult))
	assert.Equal(t, apiruntime.StatusRunning, getResult.Status.State)

	err = env.client.Call("agentrun/prompt", pkgariapi.AgentRunPromptParams{
		Workspace: "reserve-ws",
		Name:      agentName,
		Prompt:    "second",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in idle state")
}

// ────────────────────────────────────────────────────────────────────────────
// agentrun/restart tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgentRunRestartFromIdle(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "restart-idle-ws")
	seedAgent(t, env.store, "restart-idle-ws", "idle-agent", apiruntime.StatusIdle)

	var result pkgariapi.AgentRun
	err := env.client.Call("agentrun/restart", pkgariapi.ObjectKey{
		Workspace: "restart-idle-ws",
		Name:      "idle-agent",
	}, &result)
	require.NoError(t, err, "agentrun/restart from idle state must succeed")
	assert.Equal(t, apiruntime.StatusCreating, result.Status.State)
}

func TestAgentRunRestartFromRunning(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "restart-running-ws")
	seedAgent(t, env.store, "restart-running-ws", "running-agent", apiruntime.StatusRunning)

	var result pkgariapi.AgentRun
	err := env.client.Call("agentrun/restart", pkgariapi.ObjectKey{
		Workspace: "restart-running-ws",
		Name:      "running-agent",
	}, &result)
	require.NoError(t, err, "agentrun/restart from running state must succeed")
	assert.Equal(t, apiruntime.StatusCreating, result.Status.State)
}

func TestAgentDeleteRejectedForNonTerminal(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "del-ws")

	seedAgent(t, env.store, "del-ws", "active-agent", apiruntime.StatusIdle)

	err := env.client.Call("agentrun/delete", pkgariapi.ObjectKey{
		Workspace: "del-ws",
		Name:      "active-agent",
	}, nil)
	require.Error(t, err, "agentrun/delete for non-terminal agent must return an error")
}

// TestAgentCreateSocketPathTooLong verifies that agentrun/create returns -32602
// (CodeInvalidParams) and writes no DB record when the combined socket path
// would exceed the OS limit.
func TestAgentCreateSocketPathTooLong(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "sock-ws")

	// 70 'a' chars: combined with any realistic tmpdir bundleRoot this will
	// exceed 104 bytes (macOS) / 108 bytes (Linux).
	longName := strings.Repeat("a", 70)

	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "sock-ws", Name: longName},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
	}
	_, err := callRaw(t, env.client, "agentrun/create", ar)
	require.Error(t, err, "agentrun/create with too-long name must return an error")

	// RawClient.Call surfaces RPC errors as "rpc error <code>: <msg>" strings;
	// verify the code is -32602 (CodeInvalidParams).
	assert.Contains(t, err.Error(), "-32602",
		"error must carry code -32602 (CodeInvalidParams)")

	// No agent record must have been written to DB.
	var listResult pkgariapi.AgentRunList
	require.NoError(t, env.client.Call("agentrun/list",
		pkgariapi.ListOptions{FieldSelector: map[string]string{"workspace": "sock-ws"}},
		&listResult))
	for _, ag := range listResult.Items {
		assert.NotEqual(t, longName, ag.Metadata.Name,
			"agent with too-long name must not appear in agentrun/list")
	}
}

// TestAgentRunCreateRestartPolicyValidation verifies that agentrun/create
// rejects unknown restartPolicy values with -32602 (CodeInvalidParams) and
// accepts valid values ("", "try_reload", "always_new").
func TestAgentRunCreateRestartPolicyValidation(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "rp-ws")

	// Invalid values must be rejected.
	for _, bad := range []string{"on-failure", "never", "always", "bad-value"} {
		ar := pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{Workspace: "rp-ws", Name: "rp-agent-bad"},
			Spec:     pkgariapi.AgentRunSpec{Agent: "default", RestartPolicy: bad},
		}
		_, err := callRaw(t, env.client, "agentrun/create", ar)
		require.Error(t, err, "restartPolicy=%q must be rejected", bad)
		assert.Contains(t, err.Error(), "-32602",
			"restartPolicy=%q must return CodeInvalidParams", bad)
	}

	// Valid values must not be rejected at the validation layer.
	// (The agent goes into "creating" state; shim start will fail in test env — that's OK.)
	for _, good := range []string{"", "try_reload", "always_new"} {
		ar := pkgariapi.AgentRun{
			Metadata: pkgariapi.ObjectMeta{Workspace: "rp-ws", Name: "rp-agent-" + good},
			Spec:     pkgariapi.AgentRunSpec{Agent: "default", RestartPolicy: good},
		}
		_, err := callRaw(t, env.client, "agentrun/create", ar)
		// The call may succeed (state=creating) or fail with an internal error
		// (shim start fails in test env) — but must NOT fail with -32602 for
		// the restartPolicy field itself.
		if err != nil {
			assert.NotContains(t, err.Error(), "invalid restartPolicy",
				"restartPolicy=%q should not be rejected as invalid", good)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Mock shim for workspace/send and agentrun/prompt delivery tests
// ────────────────────────────────────────────────────────────────────────────

// miniShimServer is a lightweight in-process shim that accepts session/prompt
// and records received prompts. Used to verify workspace/send delivery.
type miniShimServer struct {
	listener net.Listener
	done     chan struct{}
	once     sync.Once

	mu      sync.Mutex
	prompts []string
}

func newMiniShimServer(t *testing.T) (*miniShimServer, string) {
	t.Helper()
	sockPath := fmt.Sprintf("/tmp/mass-mini-shim-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000)
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	s := &miniShimServer{
		listener: ln,
		done:     make(chan struct{}),
	}
	go s.serve()

	t.Cleanup(func() {
		s.close()
		_ = os.Remove(sockPath)
	})

	return s, sockPath
}

func (s *miniShimServer) serve() {
	for {
		nc, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
			default:
			}
			return
		}
		go s.handleConn(nc)
	}
}

func (s *miniShimServer) handleConn(nc net.Conn) {
	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&miniShimHandler{srv: s})
	conn := jsonrpc2.NewConn(context.Background(), stream, h)
	<-conn.DisconnectNotify()
}

func (s *miniShimServer) close() {
	s.once.Do(func() { close(s.done) })
	_ = s.listener.Close()
}

func (s *miniShimServer) receivedPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.prompts))
	copy(cp, s.prompts)
	return cp
}

type miniShimHandler struct {
	srv *miniShimServer
}

func (h *miniShimHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}
	switch req.Method {
	case "session/prompt":
		var params struct {
			Prompt string `json:"prompt"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &params)
		}
		h.srv.mu.Lock()
		h.srv.prompts = append(h.srv.prompts, params.Prompt)
		h.srv.mu.Unlock()
		_ = conn.Reply(ctx, req.ID, map[string]string{"stopReason": "end_turn"})
	case "session/subscribe":
		_ = conn.Reply(ctx, req.ID, map[string]any{"nextSeq": 0})
	case "runtime/status":
		_ = conn.Reply(ctx, req.ID, map[string]any{
			"state":    map[string]string{"status": "idle"},
			"recovery": map[string]int{"lastSeq": -1},
		})
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// workspace/send tests
// ────────────────────────────────────────────────────────────────────────────

// injectMockShim starts a mini shim server, waits for it to be ready, and
// injects it into the process manager under "wsName/agentName".
// Returns the shim server so callers can inspect received prompts.
func injectMockShim(t *testing.T, env *testEnv, wsName, agentName string) *miniShimServer {
	t.Helper()
	shimSrv, shimSock := newMiniShimServer(t)

	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", shimSock)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "mini shim socket not ready")

	shimClient, err := shimclient.Dial(context.Background(), shimSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shimClient.Close() })

	env.processes.InjectProcess(wsName+"/"+agentName, &agentd.ShimProcess{
		AgentKey:   wsName + "/" + agentName,
		SocketPath: shimSock,
		Client:     shimClient,
		Events:     make(chan apishim.ShimEvent, 1024),
		Done:       make(chan struct{}),
	})
	return shimSrv
}

func TestWorkspaceSendDelivered(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "send-ws")

	agentName := "recv-agent"
	seedAgent(t, env.store, "send-ws", agentName, apiruntime.StatusIdle)
	shimSrv := injectMockShim(t, env, "send-ws", agentName)

	var sendResult pkgariapi.WorkspaceSendResult
	require.NoError(t, env.client.Call("workspace/send", pkgariapi.WorkspaceSendParams{
		Workspace: "send-ws",
		From:      "sender",
		To:        agentName,
		Message:   "hello",
	}, &sendResult))
	assert.True(t, sendResult.Delivered)

	require.Eventually(t, func() bool {
		return len(shimSrv.receivedPrompts()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "mock shim did not receive prompt")

	// Delivered prompt must include the sender envelope and the original message.
	prompt := shimSrv.receivedPrompts()[0]
	assert.Contains(t, prompt, "[workspace-message from=sender]")
	assert.Contains(t, prompt, "hello")
}

func TestWorkspaceSendNeedsReplyAddsReplyHeader(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "reply-ws")

	agentName := "reply-agent"
	seedAgent(t, env.store, "reply-ws", agentName, apiruntime.StatusIdle)
	shimSrv := injectMockShim(t, env, "reply-ws", agentName)

	var sendResult pkgariapi.WorkspaceSendResult
	require.NoError(t, env.client.Call("workspace/send", pkgariapi.WorkspaceSendParams{
		Workspace:  "reply-ws",
		From:       "codex",
		To:         agentName,
		Message:    "please review",
		NeedsReply: true,
	}, &sendResult))
	assert.True(t, sendResult.Delivered)

	require.Eventually(t, func() bool {
		return len(shimSrv.receivedPrompts()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "mock shim did not receive prompt")

	// When needsReply=true the envelope must include reply-to and reply-requested=true.
	prompt := shimSrv.receivedPrompts()[0]
	assert.Contains(t, prompt, "reply-to=codex")
	assert.Contains(t, prompt, "reply-requested=true")
	assert.Contains(t, prompt, "please review")
}

func TestWorkspaceSendRejectedForErrorAgent(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "serr-ws")
	seedAgent(t, env.store, "serr-ws", "err-agent", apiruntime.StatusError)

	err := env.client.Call("workspace/send", pkgariapi.WorkspaceSendParams{
		Workspace: "serr-ws",
		From:      "sender",
		To:        "err-agent",
		Message:   "hi",
	}, nil)
	require.Error(t, err, "workspace/send to error-state agent must fail")
}

// ────────────────────────────────────────────────────────────────────────────
// No agentId field in responses
// ────────────────────────────────────────────────────────────────────────────

// hasAgentIDKey recursively checks whether a decoded JSON value (map/slice/scalar)
// contains a key named "agentId" at any nesting level.
func hasAgentIDKey(v any) bool {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			if k == "agentId" {
				return true
			}
			if hasAgentIDKey(child) {
				return true
			}
		}
	case []any:
		for _, item := range val {
			if hasAgentIDKey(item) {
				return true
			}
		}
	}
	return false
}

func TestNoAgentIDInResponses(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "noid-ws")

	seedAgent(t, env.store, "noid-ws", "a1", apiruntime.StatusIdle)
	seedAgent(t, env.store, "noid-ws", "a2", apiruntime.StatusStopped)

	// agentrun/list
	listRaw, err := callRaw(t, env.client, "agentrun/list",
		pkgariapi.ListOptions{FieldSelector: map[string]string{"workspace": "noid-ws"}})
	require.NoError(t, err)
	var listMap any
	require.NoError(t, json.Unmarshal(listRaw, &listMap))
	assert.False(t, hasAgentIDKey(listMap), "agentrun/list response must not contain agentId")

	// agentrun/get for each agent
	for _, name := range []string{"a1", "a2"} {
		getRaw, err := callRaw(t, env.client, "agentrun/get", pkgariapi.ObjectKey{
			Workspace: "noid-ws",
			Name:      name,
		})
		require.NoError(t, err)
		var getMap any
		require.NoError(t, json.Unmarshal(getRaw, &getMap))
		assert.False(t, hasAgentIDKey(getMap),
			"agentrun/get response for %s must not contain agentId", name)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// TestServerServeShutdown — basic lifecycle smoke test (kept from T01)
// ────────────────────────────────────────────────────────────────────────────

func TestServerServeShutdown(t *testing.T) {
	env := newTestServer(t)
	// env.client is already connected; just verify it can call a no-op method.
	err := env.client.Call("workspace/list", nil, nil)
	require.NoError(t, err)
}
