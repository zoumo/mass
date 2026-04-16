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

	apishim "github.com/zoumo/mass/pkg/shim/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock shim server — speaks the clean-break session/* + runtime/* surface
// ────────────────────────────────────────────────────────────────────────────

// mockShimServer is a minimal JSON-RPC server that mimics agent-shim behavior
// on the clean-break protocol surface.
type mockShimServer struct {
	listener net.Listener
	conn     *jsonrpc2.Conn
	done     chan struct{}
	once     sync.Once

	mu                sync.Mutex
	statusResult      apishim.RuntimeStatusResult
	promptResult      apishim.SessionPromptResult
	subscribed        bool
	liveNotifications []shimNotif // queued to emit after subscribe

	// session/load tracking
	loadCalled     bool
	loadCalledWith string
	loadSessionErr error // nil = success; non-nil = return RPC error
}

type shimNotif struct {
	method string
	params any
}

// newMockShimServer creates and starts a mock server on a temp Unix socket.
func newMockShimServer(t *testing.T) (*mockShimServer, string) {
	t.Helper()
	// Short path to avoid macOS's 104-char Unix socket path limit.
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("shim-mock-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	s := &mockShimServer{
		listener: ln,
		done:     make(chan struct{}),
		statusResult: apishim.RuntimeStatusResult{
			State: apiruntime.State{
				MassVersion: "0.1.0",
				ID:         "test-session",
				Status:     apiruntime.StatusIdle,
				Bundle:     "/tmp/test-bundle",
			},
			Recovery: apishim.RuntimeStatusRecovery{LastSeq: -1},
		},
		promptResult: apishim.SessionPromptResult{StopReason: "end_turn"},
	}

	go s.serve()

	t.Cleanup(func() {
		s.close()
		_ = os.Remove(socketPath)
	})

	return s, socketPath
}

func (s *mockShimServer) serve() {
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
		h := jsonrpc2.AsyncHandler(&mockShimHandler{srv: s})
		s.conn = jsonrpc2.NewConn(context.Background(), stream, h)
		<-s.conn.DisconnectNotify()
	}
}

func (s *mockShimServer) close() {
	s.once.Do(func() { close(s.done) })
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// queueNotification queues a notification to emit on the next Subscribe call.
func (s *mockShimServer) queueNotification(method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveNotifications = append(s.liveNotifications, shimNotif{method: method, params: params})
}

// ── mock handler ─────────────────────────────────────────────────────────

type mockShimHandler struct {
	srv *mockShimServer
}

func (h *mockShimHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
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

func (h *mockShimHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	result := h.srv.promptResult
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, result)
}

func (h *mockShimHandler) handleCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
}

func (h *mockShimHandler) handleLoad(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var params apishim.SessionLoadParams
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

func (h *mockShimHandler) handleWatchEvent(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Fixed watchID for the mock — client-side Watcher filters by this value.
	const watchID = "mock-watch-1"

	h.srv.mu.Lock()
	h.srv.subscribed = true
	notifs := make([]shimNotif, len(h.srv.liveNotifications))
	copy(notifs, h.srv.liveNotifications)
	h.srv.mu.Unlock()

	_ = conn.Reply(ctx, req.ID, apishim.SessionWatchEventResult{WatchID: watchID, NextSeq: 0})

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

func (h *mockShimHandler) handleStatus(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	result := h.srv.statusResult
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, result)
}

func (h *mockShimHandler) handleStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.srv.close()
	}()
}
