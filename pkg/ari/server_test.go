// Package ari_test tests the ARI JSON-RPC server via a real Unix socket.
package ari_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/agentd"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// ────────────────────────────────────────────────────────────────────────────
// Test harness helpers
// ────────────────────────────────────────────────────────────────────────────

// testEnv holds all components for a running ARI test server.
type testEnv struct {
	srv       *ari.Server
	client    *ari.Client
	store     *meta.Store
	processes *agentd.ProcessManager
	agents    *agentd.AgentManager
}

// shortSockPath returns a process-unique Unix socket path safe for macOS.
// macOS has a 104-char limit; /tmp/oar-<pid>-ari.sock is well within that.
func shortSockPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("/tmp/oar-%d-%d-ari.sock", os.Getpid(), time.Now().UnixNano()%100000)
}

// newTestServer creates a full ARI test server and returns the env.
// The server is started in a goroutine; cleanup shuts it down.
func newTestServer(t *testing.T) *testEnv {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := meta.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	mgr := workspace.NewWorkspaceManager()
	registry := ari.NewRegistry()

	runtimeClasses, err := agentd.NewRuntimeClassRegistry(nil)
	require.NoError(t, err)

	agents := agentd.NewAgentManager(store)
	cfg := agentd.Config{WorkspaceRoot: filepath.Join(tmpDir, "ws-root")}
	processes := agentd.NewProcessManager(runtimeClasses, agents, store, cfg)

	sockPath := shortSockPath(t)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	srv := ari.New(mgr, registry, agents, processes, runtimeClasses, cfg, store,
		sockPath, tmpDir)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	// Wait for socket to appear.
	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "server socket did not appear")

	client, err := ari.NewClient(sockPath)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
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
func callRaw(t *testing.T, client *ari.Client, method string, params any) (json.RawMessage, error) {
	t.Helper()
	var result json.RawMessage
	err := client.Call(method, params, &result)
	return result, err
}

// waitUntilWorkspaceReady polls workspace/status until phase == "ready".
func waitUntilWorkspaceReady(t *testing.T, client *ari.Client, wsName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var res ari.WorkspaceStatusResult
		err := client.Call("workspace/status", map[string]string{"name": wsName}, &res)
		return err == nil && res.Phase == "ready"
	}, 5*time.Second, 50*time.Millisecond, "workspace %s did not become ready", wsName)
}

// createAndWaitWorkspace creates a workspace with emptyDir source and polls until ready.
func createAndWaitWorkspace(t *testing.T, client *ari.Client, name string) ari.WorkspaceStatusResult {
	t.Helper()
	var createResult ari.WorkspaceCreateResult
	require.NoError(t, client.Call("workspace/create", map[string]any{
		"name":   name,
		"source": json.RawMessage(`{"type":"emptyDir"}`),
	}, &createResult))
	assert.Equal(t, "pending", createResult.Phase)

	waitUntilWorkspaceReady(t, client, name)

	var statusResult ari.WorkspaceStatusResult
	require.NoError(t, client.Call("workspace/status", map[string]string{"name": name}, &statusResult))
	return statusResult
}

// seedAgent inserts a raw agent record directly into the store, bypassing the
// background shim start. Used to prime DB state for handler-only tests.
func seedAgent(t *testing.T, store *meta.Store, wsName, name string, state spec.Status) {
	t.Helper()
	err := store.CreateAgent(context.Background(), &meta.Agent{
		Metadata: meta.ObjectMeta{
			Name:      name,
			Workspace: wsName,
		},
		Spec: meta.AgentSpec{RuntimeClass: "default"},
		Status: meta.AgentStatus{
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

	var result ari.WorkspaceCreateResult
	err := env.client.Call("workspace/create", map[string]any{
		"name":   "w1",
		"source": json.RawMessage(`{"type":"emptyDir"}`),
	}, &result)
	require.NoError(t, err)
	assert.Equal(t, "w1", result.Name)
	assert.Equal(t, "pending", result.Phase)
}

func TestWorkspaceStatusReady(t *testing.T) {
	env := newTestServer(t)

	statusResult := createAndWaitWorkspace(t, env.client, "w-ready")

	assert.Equal(t, "ready", statusResult.Phase)
	assert.NotEmpty(t, statusResult.Path, "ready workspace must have a path")
}

func TestWorkspaceStatusMembers(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "ws-members")

	// Seed two agents directly into the store (bypasses shim start).
	seedAgent(t, env.store, "ws-members", "checker", spec.StatusIdle)
	seedAgent(t, env.store, "ws-members", "reviewer", spec.StatusRunning)

	var result ari.WorkspaceStatusResult
	require.NoError(t, env.client.Call("workspace/status",
		map[string]string{"name": "ws-members"}, &result))

	assert.Equal(t, "ready", result.Phase)
	require.Len(t, result.Members, 2)

	names := make([]string, 0, len(result.Members))
	for _, m := range result.Members {
		names = append(names, m.Name)
	}
	assert.ElementsMatch(t, []string{"checker", "reviewer"}, names)
}

func TestWorkspaceStatusMembersEmpty(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "ws-empty")

	var result ari.WorkspaceStatusResult
	require.NoError(t, env.client.Call("workspace/status",
		map[string]string{"name": "ws-empty"}, &result))

	assert.Equal(t, "ready", result.Phase)
	assert.Empty(t, result.Members)
}

func TestWorkspaceList(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "wl-1")
	createAndWaitWorkspace(t, env.client, "wl-2")

	var listResult ari.WorkspaceListResult
	require.NoError(t, env.client.Call("workspace/list", nil, &listResult))
	assert.GreaterOrEqual(t, len(listResult.Workspaces), 2)

	names := make([]string, 0, len(listResult.Workspaces))
	for _, w := range listResult.Workspaces {
		names = append(names, w.Name)
	}
	assert.Contains(t, names, "wl-1")
	assert.Contains(t, names, "wl-2")
}

func TestWorkspaceDelete(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "w-del")

	err := env.client.Call("workspace/delete", map[string]string{"name": "w-del"}, nil)
	require.NoError(t, err)

	// After delete: status should return an error (not found → -32602 or phase error).
	var statusResult ari.WorkspaceStatusResult
	statusErr := env.client.Call("workspace/status", map[string]string{"name": "w-del"}, &statusResult)
	// Either an RPC error (not found) or phase=="error" are acceptable.
	if statusErr == nil {
		assert.Equal(t, "error", statusResult.Phase)
	} else {
		assert.Contains(t, statusErr.Error(), "-32602")
	}
}

func TestWorkspaceDeleteBlockedByAgent(t *testing.T) {
	env := newTestServer(t)

	createAndWaitWorkspace(t, env.client, "w-blocked")
	seedAgent(t, env.store, "w-blocked", "blocker", spec.StatusIdle)

	err := env.client.Call("workspace/delete", map[string]string{"name": "w-blocked"}, nil)
	require.Error(t, err, "workspace/delete must fail when an agent is present")
}

// ────────────────────────────────────────────────────────────────────────────
// Agent tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgentCreateReturnsCreating(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "ac-ws")

	// Call via raw to inspect JSON for absence of "agentId".
	raw, err := callRaw(t, env.client, "agent/create", map[string]any{
		"workspace":    "ac-ws",
		"name":         "my-agent",
		"runtimeClass": "default",
	})
	require.NoError(t, err)

	var result ari.AgentCreateResult
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, "creating", result.State)
	assert.Equal(t, "ac-ws", result.Workspace)
	assert.Equal(t, "my-agent", result.Name)

	// No agentId key in the response JSON.
	var rawMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &rawMap))
	_, hasAgentID := rawMap["agentId"]
	assert.False(t, hasAgentID, "agentId must not appear in agent/create response")
}

func TestAgentListAndStatus(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "als-ws")

	seedAgent(t, env.store, "als-ws", "agent-idle", spec.StatusIdle)
	seedAgent(t, env.store, "als-ws", "agent-stopped", spec.StatusStopped)

	var listResult ari.AgentListResult
	require.NoError(t, env.client.Call("agent/list", map[string]string{"workspace": "als-ws"}, &listResult))
	assert.Len(t, listResult.Agents, 2)

	// Verify agent/status returns correct state.
	var statusResult ari.AgentStatusResult
	require.NoError(t, env.client.Call("agent/status", map[string]string{
		"workspace": "als-ws",
		"name":      "agent-idle",
	}, &statusResult))
	assert.Equal(t, "idle", statusResult.Agent.State)
	assert.Equal(t, "als-ws", statusResult.Agent.Workspace)
	assert.Equal(t, "agent-idle", statusResult.Agent.Name)
}

func TestAgentPromptRejectedForBadState(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "pr-ws")

	seedAgent(t, env.store, "pr-ws", "agent-stopped", spec.StatusStopped)
	seedAgent(t, env.store, "pr-ws", "agent-error", spec.StatusError)
	seedAgent(t, env.store, "pr-ws", "agent-creating", spec.StatusCreating)

	for _, name := range []string{"agent-stopped", "agent-error", "agent-creating"} {
		err := env.client.Call("agent/prompt", map[string]any{
			"workspace": "pr-ws",
			"name":      name,
			"prompt":    "hello",
		}, nil)
		require.Error(t, err, "agent/prompt for %s must return an error", name)
		assert.Contains(t, err.Error(), "not in idle state",
			"error for %s must mention 'not in idle state'", name)
	}
}

func TestAgentDeleteRejectedForNonTerminal(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "del-ws")

	seedAgent(t, env.store, "del-ws", "active-agent", spec.StatusIdle)

	err := env.client.Call("agent/delete", map[string]string{
		"workspace": "del-ws",
		"name":      "active-agent",
	}, nil)
	require.Error(t, err, "agent/delete for non-terminal agent must return an error")
}

// ────────────────────────────────────────────────────────────────────────────
// Mock shim for workspace/send and agent/prompt delivery tests
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
	sockPath := fmt.Sprintf("/tmp/oar-mini-shim-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000)
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

func TestWorkspaceSendDelivered(t *testing.T) {
	env := newTestServer(t)
	wsStatus := createAndWaitWorkspace(t, env.client, "send-ws")
	_ = wsStatus

	agentName := "recv-agent"
	seedAgent(t, env.store, "send-ws", agentName, spec.StatusIdle)

	// Start in-process mock shim.
	shimSrv, shimSock := newMiniShimServer(t)

	// Wait for shim socket to be ready.
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", shimSock)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "mini shim socket not ready")

	// Build a ShimClient connected to the mock shim.
	shimClient, err := agentd.Dial(context.Background(), shimSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shimClient.Close() })

	// Build a ShimProcess with the mock client and inject into ProcessManager.
	shimProc := &agentd.ShimProcess{
		AgentKey:   "send-ws/" + agentName,
		SocketPath: shimSock,
		Client:     shimClient,
		Events:     make(chan events.Event, 1024),
		Done:       make(chan struct{}),
	}
	env.processes.InjectProcess("send-ws/"+agentName, shimProc)

	// Call workspace/send.
	var sendResult ari.WorkspaceSendResult
	require.NoError(t, env.client.Call("workspace/send", map[string]string{
		"workspace": "send-ws",
		"from":      "sender",
		"to":        agentName,
		"message":   "hello",
	}, &sendResult))

	assert.True(t, sendResult.Delivered)

	// Wait for the async prompt to reach the mock shim.
	require.Eventually(t, func() bool {
		return len(shimSrv.receivedPrompts()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "mock shim did not receive prompt")

	prompts := shimSrv.receivedPrompts()
	assert.Equal(t, "hello", prompts[0])
}

func TestWorkspaceSendRejectedForErrorAgent(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "serr-ws")
	seedAgent(t, env.store, "serr-ws", "err-agent", spec.StatusError)

	err := env.client.Call("workspace/send", map[string]string{
		"workspace": "serr-ws",
		"from":      "sender",
		"to":        "err-agent",
		"message":   "hi",
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

	seedAgent(t, env.store, "noid-ws", "a1", spec.StatusIdle)
	seedAgent(t, env.store, "noid-ws", "a2", spec.StatusStopped)

	// agent/list
	listRaw, err := callRaw(t, env.client, "agent/list", map[string]string{"workspace": "noid-ws"})
	require.NoError(t, err)
	var listMap any
	require.NoError(t, json.Unmarshal(listRaw, &listMap))
	assert.False(t, hasAgentIDKey(listMap), "agent/list response must not contain agentId")

	// agent/status for each agent
	for _, name := range []string{"a1", "a2"} {
		statusRaw, err := callRaw(t, env.client, "agent/status", map[string]string{
			"workspace": "noid-ws",
			"name":      name,
		})
		require.NoError(t, err)
		var statusMap any
		require.NoError(t, json.Unmarshal(statusRaw, &statusMap))
		assert.False(t, hasAgentIDKey(statusMap),
			"agent/status response for %s must not contain agentId", name)
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
