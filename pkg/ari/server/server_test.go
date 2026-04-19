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

	"github.com/zoumo/mass/pkg/agentd"
	"github.com/zoumo/mass/pkg/agentd/store"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
	ariserver "github.com/zoumo/mass/pkg/ari/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
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
// macOS has a 104-char limit; /tmp/mass-<pid>-mass.sock is well within that.
func shortSockPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("/tmp/mass-%d-%d-mass.sock", os.Getpid(), time.Now().UnixNano()%100000)
}

// newTestServer creates a full ARI test server and returns the env.
// The server is started in a goroutine; cleanup shuts it down.
func newTestServer(t *testing.T) *testEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("/tmp", "mass-ari-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	dbPath := filepath.Join(tmpDir, "test.db")
	metaStore, err := store.NewStore(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = metaStore.Close() })

	mgr := workspace.NewWorkspaceManager()

	agents := agentd.NewAgentRunManager(metaStore, slog.Default())
	processes := agentd.NewProcessManager(agents, metaStore, filepath.Join(tmpDir, "mass.sock"), filepath.Join(tmpDir, "bundles"), slog.Default(), "info", "pretty")

	sockPath := shortSockPath(t)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	svc := ariserver.New(mgr, agents, processes, metaStore, tmpDir, slog.Default())
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
		store:     metaStore,
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
// background agent-run start. Used to prime DB state for handler-only tests.
func seedAgent(t *testing.T, metaStore *store.Store, wsName, name string, state apiruntime.Status) {
	t.Helper()
	err := metaStore.CreateAgentRun(context.Background(), &pkgariapi.AgentRun{
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

// seedAgentDef inserts a minimal agent definition into the store so that
// agentrun/create validation (agent-exists + not-disabled) passes.
func seedAgentDef(t *testing.T, metaStore *store.Store, name string) {
	t.Helper()
	_ = metaStore.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.AgentSpec{Command: "echo"},
	})
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

func TestWorkspaceCreateDuplicate(t *testing.T) {
	env := newTestServer(t)

	ws := pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: "dup-ws"},
		Spec:     pkgariapi.WorkspaceSpec{Source: json.RawMessage(`{"type":"emptyDir"}`)},
	}
	var result pkgariapi.Workspace
	require.NoError(t, env.client.Call("workspace/create", ws, &result))

	// Second create of the same workspace should fail with InvalidParams (-32602).
	err := env.client.Call("workspace/create", ws, &result)
	require.Error(t, err, "duplicate workspace/create should fail")
	assert.Contains(t, err.Error(), "-32602", "error code should be InvalidParams")
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorkspaceDeleteNotFound(t *testing.T) {
	env := newTestServer(t)

	err := env.client.Call("workspace/delete", pkgariapi.ObjectKey{Name: "no-such-ws"}, nil)
	require.Error(t, err, "deleting non-existent workspace should fail")
	assert.Contains(t, err.Error(), "-32602", "error code should be InvalidParams")
}

// ────────────────────────────────────────────────────────────────────────────
// Agent tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgentCreateReturnsCreating(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "ac-ws")
	seedAgentDef(t, env.store, "default")

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
			Prompt:    []pkgariapi.ContentBlock{pkgariapi.TextBlock("hello")},
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

	runSrv, runSock := newMiniRunServer(t)
	_ = runSrv

	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", runSock)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "mini agent-run socket not ready")

	runClient, err := runclient.Dial(context.Background(), runSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = runClient.Close() })

	env.processes.InjectProcess("reserve-ws/"+agentName, &agentd.RunProcess{
		AgentKey:   "reserve-ws/" + agentName,
		SocketPath: runSock,
		Client:     runClient,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	})

	var result pkgariapi.AgentRunPromptResult
	require.NoError(t, env.client.Call("agentrun/prompt", pkgariapi.AgentRunPromptParams{
		Workspace: "reserve-ws",
		Name:      agentName,
		Prompt:    []pkgariapi.ContentBlock{pkgariapi.TextBlock("hello")},
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
		Prompt:    []pkgariapi.ContentBlock{pkgariapi.TextBlock("second")},
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
	seedAgentDef(t, env.store, "default")

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

// ────────────────────────────────────────────────────────────────────────────
// Mock agent-run for workspace/send and agentrun/prompt delivery tests
// ────────────────────────────────────────────────────────────────────────────

// miniRunServer is a lightweight in-process agent-run that accepts session/prompt
// and records received prompts. Used to verify workspace/send delivery.
type miniRunServer struct {
	listener net.Listener
	done     chan struct{}
	once     sync.Once

	mu      sync.Mutex
	prompts []string
}

func newMiniRunServer(t *testing.T) (*miniRunServer, string) {
	t.Helper()
	sockPath := fmt.Sprintf("/tmp/mass-mini-run-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000)
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	s := &miniRunServer{
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

func (s *miniRunServer) serve() {
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

func (s *miniRunServer) handleConn(nc net.Conn) {
	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&miniRunHandler{srv: s})
	conn := jsonrpc2.NewConn(context.Background(), stream, h)
	<-conn.DisconnectNotify()
}

func (s *miniRunServer) close() {
	s.once.Do(func() { close(s.done) })
	_ = s.listener.Close()
}

func (s *miniRunServer) receivedPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.prompts))
	copy(cp, s.prompts)
	return cp
}

type miniRunHandler struct {
	srv *miniRunServer
}

func (h *miniRunHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}
	switch req.Method {
	case "session/prompt":
		var params struct {
			Prompt []runapi.ContentBlock `json:"prompt"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &params)
		}
		// Extract all text from content blocks for assertion convenience.
		var parts []string
		for _, b := range params.Prompt {
			if b.Text != nil {
				parts = append(parts, b.Text.Text)
			}
		}
		h.srv.mu.Lock()
		h.srv.prompts = append(h.srv.prompts, strings.Join(parts, "\n"))
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

// injectMockRun starts a mini agent-run server, waits for it to be ready, and
// injects it into the process manager under "wsName/agentName".
// Returns the agent-run server so callers can inspect received prompts.
func injectMockRun(t *testing.T, env *testEnv, wsName, agentName string) *miniRunServer {
	t.Helper()
	runSrv, runSock := newMiniRunServer(t)

	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", runSock)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "mini agent-run socket not ready")

	runClient, err := runclient.Dial(context.Background(), runSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = runClient.Close() })

	env.processes.InjectProcess(wsName+"/"+agentName, &agentd.RunProcess{
		AgentKey:   wsName + "/" + agentName,
		SocketPath: runSock,
		Client:     runClient,
		Events:     make(chan runapi.AgentRunEvent, 1024),
		Done:       make(chan struct{}),
	})
	return runSrv
}

func TestWorkspaceSendDelivered(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "send-ws")

	agentName := "recv-agent"
	seedAgent(t, env.store, "send-ws", agentName, apiruntime.StatusIdle)
	runSrv := injectMockRun(t, env, "send-ws", agentName)

	var sendResult pkgariapi.WorkspaceSendResult
	require.NoError(t, env.client.Call("workspace/send", pkgariapi.WorkspaceSendParams{
		Workspace: "send-ws",
		From:      "sender",
		To:        agentName,
		Message:   []pkgariapi.ContentBlock{pkgariapi.TextBlock("hello")},
	}, &sendResult))
	assert.True(t, sendResult.Delivered)

	require.Eventually(t, func() bool {
		return len(runSrv.receivedPrompts()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "mock agent-run did not receive prompt")

	// Delivered prompt must include the sender envelope and the original message.
	prompt := runSrv.receivedPrompts()[0]
	assert.Contains(t, prompt, `<workspace-message from="sender" />`)
	assert.Contains(t, prompt, "hello")
}

func TestWorkspaceSendNeedsReplyAddsReplyHeader(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "reply-ws")

	agentName := "reply-agent"
	seedAgent(t, env.store, "reply-ws", agentName, apiruntime.StatusIdle)
	runSrv := injectMockRun(t, env, "reply-ws", agentName)

	var sendResult pkgariapi.WorkspaceSendResult
	require.NoError(t, env.client.Call("workspace/send", pkgariapi.WorkspaceSendParams{
		Workspace:  "reply-ws",
		From:       "codex",
		To:         agentName,
		Message:    []pkgariapi.ContentBlock{pkgariapi.TextBlock("please review")},
		NeedsReply: true,
	}, &sendResult))
	assert.True(t, sendResult.Delivered)

	require.Eventually(t, func() bool {
		return len(runSrv.receivedPrompts()) >= 1
	}, 2*time.Second, 20*time.Millisecond, "mock agent-run did not receive prompt")

	// When needsReply=true the envelope must include reply-to and reply-requested=true.
	prompt := runSrv.receivedPrompts()[0]
	assert.Contains(t, prompt, `<workspace-message from="codex" reply-to="codex" reply-requested="true" />`)
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
		Message:   []pkgariapi.ContentBlock{pkgariapi.TextBlock("hi")},
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

// ────────────────────────────────────────────────────────────────────────────
// Agent (agent/) service handler tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgentCreate(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "test-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "echo"},
	}
	var result pkgariapi.Agent
	require.NoError(t, env.client.Call("agent/create", ag, &result))
	assert.Equal(t, "test-agent", result.Metadata.Name)
	assert.Equal(t, "echo", result.Spec.Command)
}

func TestAgentCreate_DuplicateError(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "dup-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "echo"},
	}
	var result pkgariapi.Agent
	require.NoError(t, env.client.Call("agent/create", ag, &result))

	// Second create with same name should fail.
	_, err := callRaw(t, env.client, "agent/create", ag)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAgentCreate_MissingName(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{Spec: pkgariapi.AgentSpec{Command: "echo"}}
	_, err := callRaw(t, env.client, "agent/create", ag)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAgentCreate_MissingCommand(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{Metadata: pkgariapi.ObjectMeta{Name: "no-cmd"}}
	_, err := callRaw(t, env.client, "agent/create", ag)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestAgentGet(t *testing.T) {
	env := newTestServer(t)

	// Seed an agent via store.
	require.NoError(t, env.store.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "get-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "cat"},
	}))

	var result pkgariapi.Agent
	require.NoError(t, env.client.Call("agent/get", pkgariapi.ObjectKey{Name: "get-agent"}, &result))
	assert.Equal(t, "get-agent", result.Metadata.Name)
	assert.Equal(t, "cat", result.Spec.Command)
}

func TestAgentGet_NotFound(t *testing.T) {
	env := newTestServer(t)

	_, err := callRaw(t, env.client, "agent/get", pkgariapi.ObjectKey{Name: "no-such"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAgentGet_MissingName(t *testing.T) {
	env := newTestServer(t)

	_, err := callRaw(t, env.client, "agent/get", pkgariapi.ObjectKey{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAgentUpdate(t *testing.T) {
	env := newTestServer(t)

	// Create first.
	require.NoError(t, env.store.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "upd-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "old-cmd"},
	}))

	// Update command.
	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "upd-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "new-cmd"},
	}
	var result pkgariapi.Agent
	require.NoError(t, env.client.Call("agent/update", ag, &result))
	assert.Equal(t, "new-cmd", result.Spec.Command)
}

func TestAgentUpdate_NotFound(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "ghost"},
		Spec:     pkgariapi.AgentSpec{Command: "echo"},
	}
	_, err := callRaw(t, env.client, "agent/update", ag)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAgentUpdate_MissingName(t *testing.T) {
	env := newTestServer(t)

	ag := pkgariapi.Agent{Spec: pkgariapi.AgentSpec{Command: "echo"}}
	_, err := callRaw(t, env.client, "agent/update", ag)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestAgentList(t *testing.T) {
	env := newTestServer(t)
	ctx := context.Background()

	require.NoError(t, env.store.SetAgent(ctx, &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "ag1"},
		Spec:     pkgariapi.AgentSpec{Command: "a"},
	}))
	require.NoError(t, env.store.SetAgent(ctx, &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "ag2"},
		Spec:     pkgariapi.AgentSpec{Command: "b"},
	}))

	var result pkgariapi.AgentList
	require.NoError(t, env.client.Call("agent/list", pkgariapi.ListOptions{}, &result))
	assert.GreaterOrEqual(t, len(result.Items), 2)

	names := make(map[string]bool)
	for _, ag := range result.Items {
		names[ag.Metadata.Name] = true
	}
	assert.True(t, names["ag1"])
	assert.True(t, names["ag2"])
}

func TestAgentList_Empty(t *testing.T) {
	env := newTestServer(t)

	var result pkgariapi.AgentList
	require.NoError(t, env.client.Call("agent/list", pkgariapi.ListOptions{}, &result))
	assert.Empty(t, result.Items)
}

func TestAgentDelete(t *testing.T) {
	env := newTestServer(t)

	require.NoError(t, env.store.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "del-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "rm"},
	}))

	require.NoError(t, env.client.Call("agent/delete", pkgariapi.ObjectKey{Name: "del-agent"}, nil))

	// Verify gone.
	ag, err := env.store.GetAgent(context.Background(), "del-agent")
	require.NoError(t, err)
	assert.Nil(t, ag)
}

func TestAgentDelete_NotFound(t *testing.T) {
	env := newTestServer(t)

	// Delete is a no-op for non-existent agent (per implementation).
	require.NoError(t, env.client.Call("agent/delete", pkgariapi.ObjectKey{Name: "ghost"}, nil))
}

func TestAgentDelete_MissingName(t *testing.T) {
	env := newTestServer(t)

	_, err := callRaw(t, env.client, "agent/delete", pkgariapi.ObjectKey{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// ────────────────────────────────────────────────────────────────────────────
// agentrun/create rejects disabled agent
// ────────────────────────────────────────────────────────────────────────────

func TestAgentRunCreateRejectsDisabledAgent(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "dis-ws")

	// Seed a disabled agent definition.
	disabled := true
	require.NoError(t, env.store.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "disabled-agent"},
		Spec:     pkgariapi.AgentSpec{Disabled: &disabled, Command: "echo"},
	}))

	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "dis-ws", Name: "run-1"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "disabled-agent"},
	}
	_, err := callRaw(t, env.client, "agentrun/create", ar)
	require.Error(t, err, "agentrun/create must reject disabled agent")
	assert.Contains(t, err.Error(), "-32602", "error code must be InvalidParams")
	assert.Contains(t, err.Error(), "disabled")
}

func TestAgentRunCreateAcceptsEnabledAgent(t *testing.T) {
	env := newTestServer(t)
	createAndWaitWorkspace(t, env.client, "en-ws")

	// Seed an enabled agent definition (Disabled=nil).
	require.NoError(t, env.store.SetAgent(context.Background(), &pkgariapi.Agent{
		Metadata: pkgariapi.ObjectMeta{Name: "enabled-agent"},
		Spec:     pkgariapi.AgentSpec{Command: "echo"},
	}))

	ar := pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "en-ws", Name: "run-1"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "enabled-agent"},
	}
	raw, err := callRaw(t, env.client, "agentrun/create", ar)
	// Should not fail with "disabled" error. May fail later (no real process) but
	// must not be -32602 about disabled.
	if err != nil {
		assert.NotContains(t, err.Error(), "disabled",
			"enabled agent must not be rejected as disabled")
	} else {
		var result pkgariapi.AgentRun
		require.NoError(t, json.Unmarshal(raw, &result))
		assert.Equal(t, apiruntime.StatusCreating, result.Status.State)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// agentrun/stop and agentrun/cancel handler tests (error paths only —
// happy-path requires full process lifecycle which is covered in
// pkg/agentd/process_test.go integration test)
// ────────────────────────────────────────────────────────────────────────────

func TestAgentRunStop_NotFound(t *testing.T) {
	env := newTestServer(t)

	_, err := callRaw(t, env.client, "agentrun/stop", pkgariapi.ObjectKey{
		Workspace: "no-ws",
		Name:      "no-agent",
	})
	require.Error(t, err)
}

func TestAgentRunCancel_NotFound(t *testing.T) {
	env := newTestServer(t)

	_, err := callRaw(t, env.client, "agentrun/cancel", pkgariapi.ObjectKey{
		Workspace: "no-ws",
		Name:      "no-agent",
	})
	require.Error(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// MapRPCError tests
// ────────────────────────────────────────────────────────────────────────────

func TestMapRPCError_Nil(t *testing.T) {
	assert.NoError(t, ariserver.MapRPCError(nil))
}

func TestMapRPCError_PlainError(t *testing.T) {
	err := ariserver.MapRPCError(fmt.Errorf("boom"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestServerServeShutdown(t *testing.T) {
	env := newTestServer(t)
	// env.client is already connected; just verify it can call a no-op method.
	err := env.client.Call("workspace/list", nil, nil)
	require.NoError(t, err)
}
