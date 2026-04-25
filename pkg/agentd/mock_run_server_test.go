package agentd

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
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock agent-run server — speaks the clean-break session/* + runtime/* surface
// ────────────────────────────────────────────────────────────────────────────

// mockRunServer is a minimal JSON-RPC server that mimics agent-run behavior
// on the clean-break protocol surface.
type mockRunServer struct {
	listener net.Listener
	done     chan struct{}
	once     sync.Once

	mu                sync.Mutex
	conns             []*jsonrpc2.Conn // all active connections
	statusResult      runapi.RuntimeStatusResult
	promptResult      runapi.SessionPromptResult
	subscribed        bool
	liveNotifications []runNotif // queued to emit after subscribe

	// session/load tracking
	loadCalled     bool
	loadCalledWith string
	loadSessionErr error // nil = success; non-nil = return RPC error
}

type runNotif struct {
	method string
	params any
}

// newMockRunServer creates and starts a mock server on a temp Unix socket.
func newMockRunServer(t *testing.T) (*mockRunServer, string) {
	t.Helper()
	// Short path to avoid macOS's 104-char Unix socket path limit.
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("run-mock-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	s := &mockRunServer{
		listener: ln,
		done:     make(chan struct{}),
		statusResult: runapi.RuntimeStatusResult{
			State: apiruntime.State{
				MassVersion: "0.1.0",
				ID:          "test-session",
				Status:      apiruntime.StatusIdle,
				Bundle:      "/tmp/test-bundle",
			},
			Recovery: runapi.RuntimeStatusRecovery{LastSeq: -1},
		},
		promptResult: runapi.SessionPromptResult{StopReason: "end_turn"},
	}

	go s.serve()

	t.Cleanup(func() {
		s.close()
		_ = os.Remove(socketPath)
	})

	return s, socketPath
}

func (s *mockRunServer) serve() {
	for {
		nc, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
			default:
			}
			return
		}
		stream := jsonrpc2.NewPlainObjectStream(nc)
		h := jsonrpc2.AsyncHandler(&mockRunHandler{srv: s})
		conn := jsonrpc2.NewConn(context.Background(), stream, h)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		// Handle each connection in its own goroutine so multiple concurrent
		// connections (e.g. recoverAgent's status client + WatchClient's dial)
		// can be served simultaneously.
		go func(c *jsonrpc2.Conn) {
			<-c.DisconnectNotify()
			// Remove from conns list on disconnect.
			s.mu.Lock()
			for i, cc := range s.conns {
				if cc == c {
					s.conns = append(s.conns[:i], s.conns[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
		}(conn)
	}
}

func (s *mockRunServer) close() {
	s.once.Do(func() { close(s.done) })
	if s.listener != nil {
		_ = s.listener.Close()
	}
	// Close all active connections.
	s.mu.Lock()
	conns := make([]*jsonrpc2.Conn, len(s.conns))
	copy(conns, s.conns)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// queueNotification queues a notification to emit on the next Subscribe call.
func (s *mockRunServer) queueNotification(method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveNotifications = append(s.liveNotifications, runNotif{method: method, params: params})
}

// ── mock handler ─────────────────────────────────────────────────────────

type mockRunHandler struct {
	srv *mockRunServer
}

func (h *mockRunHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	switch req.Method {
	case "session/prompt":
		h.handlePrompt(ctx, conn, req)
	case "session/cancel":
		h.handleCancel(ctx, conn, req)
	case "session/load":
		h.handleLoad(ctx, conn, req)
	case "runtime/watch_event":
		h.handleWatchEvent(ctx, conn, req)
	case "runtime/status":
		h.handleStatus(ctx, conn, req)
	case "runtime/stop":
		h.handleStop(ctx, conn, req)
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		})
	}
}

func (h *mockRunHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	result := h.srv.promptResult
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, result)
}

func (h *mockRunHandler) handleCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
}

func (h *mockRunHandler) handleLoad(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params runapi.SessionLoadParams
	if req.Params != nil {
		_ = json.Unmarshal(*req.Params, &params)
	}

	h.srv.mu.Lock()
	h.srv.loadCalled = true
	h.srv.loadCalledWith = params.SessionID
	sessionErr := h.srv.loadSessionErr
	h.srv.mu.Unlock()

	if sessionErr != nil {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeInternalError,
			Message: sessionErr.Error(),
		})
		return
	}
	_ = conn.Reply(ctx, req.ID, nil)
}

func (h *mockRunHandler) handleWatchEvent(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Fixed watchID for the mock — client-side Watcher filters by this value.
	const watchID = "mock-watch-1"

	h.srv.mu.Lock()
	h.srv.subscribed = true
	notifs := make([]runNotif, len(h.srv.liveNotifications))
	copy(notifs, h.srv.liveNotifications)
	h.srv.mu.Unlock()

	_ = conn.Reply(ctx, req.ID, runapi.SessionWatchEventResult{WatchID: watchID, NextSeq: 0})

	// Emit queued notifications asynchronously, stamping each with the watchID
	// so the client-side Watcher filter can match them.
	go func() {
		for _, n := range notifs {
			if m, ok := n.params.(map[string]any); ok {
				m["watchId"] = watchID
			}
			_ = conn.Notify(ctx, n.method, n.params)
		}
	}()
}

func (h *mockRunHandler) handleStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	result := h.srv.statusResult
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, result)
}

func (h *mockRunHandler) handleStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.srv.close()
	}()
}
