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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/oar/api"
	apiruntime "github.com/zoumo/oar/api/runtime"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/api/shim"
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
	statusResult      shim.RuntimeStatusResult
	promptResult      shim.SessionPromptResult
	historyEntries    []events.ShimEvent
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
		statusResult: shim.RuntimeStatusResult{
			State: apiruntime.State{
				OarVersion: "0.1.0",
				ID:         "test-session",
				Status:     api.StatusIdle,
				Bundle:     "/tmp/test-bundle",
			},
			Recovery: shim.RuntimeStatusRecovery{LastSeq: -1},
		},
		promptResult: shim.SessionPromptResult{StopReason: "end_turn"},
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
	case "session/subscribe":
		h.handleSubscribe(ctx, conn, req)
	case "runtime/status":
		h.handleStatus(ctx, conn, req)
	case "runtime/history":
		h.handleHistory(ctx, conn, req)
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
	var params shim.SessionLoadParams
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

func (h *mockShimHandler) handleSubscribe(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	// Parse params to check for fromSeq.
	var params shim.SessionSubscribeParams
	if req.Params != nil {
		_ = json.Unmarshal(*req.Params, &params)
	}

	h.srv.mu.Lock()
	h.srv.subscribed = true
	notifs := make([]shimNotif, len(h.srv.liveNotifications))
	copy(notifs, h.srv.liveNotifications)

	// When fromSeq is present, return backfill entries from historyEntries.
	var result shim.SessionSubscribeResult
	if params.FromSeq != nil {
		// Return all history entries (mock always stores them in order).
		result.Entries = make([]events.ShimEvent, len(h.srv.historyEntries))
		copy(result.Entries, h.srv.historyEntries)
		result.NextSeq = len(result.Entries)
	}
	h.srv.mu.Unlock()

	_ = conn.Reply(ctx, req.ID, result)

	// Emit queued notifications.
	go func() {
		for _, n := range notifs {
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

func (h *mockShimHandler) handleHistory(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	entries := make([]events.ShimEvent, len(h.srv.historyEntries))
	copy(entries, h.srv.historyEntries)
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, shim.RuntimeHistoryResult{Entries: entries})
}

func (h *mockShimHandler) handleStop(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.srv.close()
	}()
}

// ────────────────────────────────────────────────────────────────────────────
// Dial and basic connection tests
// ────────────────────────────────────────────────────────────────────────────

func TestShimClientDial(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	assert.NotNil(t, c.conn)
	assert.Equal(t, socketPath, c.socketPath)
}

func TestShimClientDialFail(t *testing.T) {
	c, err := Dial(context.Background(), "/nonexistent/socket.sock")
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "dial")
}

func TestShimClientClose(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	require.NoError(t, c.Close())

	select {
	case <-c.DisconnectNotify():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected connection to be closed")
	}
}

func TestShimClientDisconnectNotify(t *testing.T) {
	srv, socketPath := newMockShimServer(t)

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	disconnectCh := c.DisconnectNotify()
	assert.NotNil(t, disconnectCh)

	select {
	case <-disconnectCh:
		t.Fatal("should not be disconnected initially")
	default:
	}

	srv.close()

	select {
	case <-disconnectCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("should be disconnected after server closes")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// session/* method tests
// ────────────────────────────────────────────────────────────────────────────

func TestShimClientPrompt(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	result, err := c.Prompt(context.Background(), "Hello, agent!")
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)
}

func TestShimClientCancel(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, c.Cancel(context.Background()))
}

func TestShimClientSubscribeNoAfterSeq(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	result, err := c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NextSeq)

	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed)
}

func TestShimClientSubscribeWithAfterSeq(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	afterSeq := 3
	result, err := c.Subscribe(context.Background(), &afterSeq, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NextSeq)
}

// TestShimClientSubscribeFromSeq verifies the atomic backfill path: when
// fromSeq is provided, the subscribe response includes backfill entries
// from the mock's historyEntries and the subscription is active.
func TestShimClientSubscribeFromSeq(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	// Pre-populate mock with 3 history entries.
	srv.mu.Lock()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	srv.historyEntries = []events.ShimEvent{
		{RunID: "test-run", Seq: 0, Time: at, Category: "session", Type: "text", Content: events.TextEvent{Text: "msg-0"}},
		{RunID: "test-run", Seq: 1, Time: at, Category: "session", Type: "text", Content: events.TextEvent{Text: "msg-1"}},
		{RunID: "test-run", Seq: 2, Time: at, Category: "session", Type: "text", Content: events.TextEvent{Text: "msg-2"}},
	}
	srv.mu.Unlock()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	fromSeq := 0
	result, err := c.Subscribe(context.Background(), nil, &fromSeq)
	require.NoError(t, err)

	// Verify backfill entries returned.
	require.Len(t, result.Entries, 3, "should return all 3 backfill entries")
	for i, entry := range result.Entries {
		assert.Equal(t, i, entry.Seq, "entry %d should have seq=%d", i, i)
	}
	assert.Equal(t, 3, result.NextSeq, "nextSeq should be count of entries")

	// Verify subscription was registered.
	srv.mu.Lock()
	subscribed := srv.subscribed
	srv.mu.Unlock()
	assert.True(t, subscribed, "shim should have been subscribed")
}

// TestShimClientSubscribeReceivesShimEvent verifies that shim/event
// notifications are delivered to the NotificationHandler registered via
// DialWithHandler.
func TestShimClientSubscribeReceivesShimEvent(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	// Queue a session text event.
	contentBytes, _ := json.Marshal(events.TextEvent{Text: "hello from shim"})
	srv.queueNotification(api.MethodShimEvent, map[string]any{
		"runId":    "test-run",
		"seq":      1,
		"time":     "2026-01-01T00:00:00Z",
		"category": "session",
		"type":     "text",
		"content":  json.RawMessage(contentBytes),
	})
	// Queue a runtime state_change event.
	scBytes, _ := json.Marshal(events.StateChangeEvent{PreviousStatus: "created", Status: "running", PID: 12345})
	srv.queueNotification(api.MethodShimEvent, map[string]any{
		"runId":    "test-run",
		"seq":      0,
		"time":     "2026-01-01T00:00:00Z",
		"category": "runtime",
		"type":     "state_change",
		"content":  json.RawMessage(scBytes),
	})

	// Track received notifications.
	var received []events.ShimEvent
	var mu sync.Mutex

	c, err := DialWithHandler(context.Background(), socketPath, func(ctx context.Context, method string, params json.RawMessage) {
		if method != api.MethodShimEvent {
			return
		}
		ev, err := ParseShimEvent(params)
		if err != nil {
			return
		}
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)

	// Wait for notifications to arrive.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 20*time.Millisecond, "expected at least 2 notifications")

	mu.Lock()
	types := make(map[string]bool)
	for _, ev := range received {
		types[ev.Type] = true
	}
	mu.Unlock()

	assert.True(t, types["text"], "should have received text event")
	assert.True(t, types["state_change"], "should have received state_change event")
}

// TestShimClientNotificationsAreSerialized verifies that a slow handler for an
// earlier notification cannot be overtaken by a later notification on the same
// shim connection.
func TestShimClientNotificationsAreSerialized(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	for i := 0; i < 3; i++ {
		contentBytes, _ := json.Marshal(events.TextEvent{Text: fmt.Sprintf("msg-%d", i)})
		srv.queueNotification(api.MethodShimEvent, map[string]any{
			"runId":    "test-run",
			"seq":      i,
			"time":     "2026-01-01T00:00:00Z",
			"category": "session",
			"type":     "text",
			"content":  json.RawMessage(contentBytes),
		})
	}

	var got []int
	var handlerErr error
	var mu sync.Mutex
	c, err := DialWithHandler(context.Background(), socketPath, func(_ context.Context, method string, params json.RawMessage) {
		if method != api.MethodShimEvent {
			return
		}
		p, parseErr := ParseShimEvent(params)
		if parseErr != nil {
			mu.Lock()
			handlerErr = parseErr
			mu.Unlock()
			return
		}
		if p.Seq == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		mu.Lock()
		got = append(got, p.Seq)
		mu.Unlock()
	})
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	}, 2*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.NoError(t, handlerErr)
	require.Equal(t, []int{0, 1, 2}, got)
}

// TestShimClientSubscribeDropsUnknownMethods verifies that notifications for
// unknown methods (e.g. $/event, session/update from the legacy surface) are
// silently dropped and not forwarded to the handler.
func TestShimClientSubscribeDropsUnknownMethods(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	// Queue a legacy-style notification that should be rejected.
	srv.queueNotification("$/event", map[string]any{"type": "text", "payload": map[string]any{"text": "oops"}})
	// Queue a legacy session/update that should also be rejected.
	srv.queueNotification("session/update", map[string]any{"sessionId": "s", "seq": 0})
	// Queue a valid shim/event notification.
	scBytes, _ := json.Marshal(events.StateChangeEvent{PreviousStatus: "created", Status: "running"})
	srv.queueNotification(api.MethodShimEvent, map[string]any{
		"runId":    "test-run",
		"seq":      0,
		"time":     "2026-01-01T00:00:00Z",
		"category": "runtime",
		"type":     "state_change",
		"content":  json.RawMessage(scBytes),
	})

	var received []string
	var mu sync.Mutex

	c, err := DialWithHandler(context.Background(), socketPath, func(_ context.Context, method string, _ json.RawMessage) {
		mu.Lock()
		received = append(received, method)
		mu.Unlock()
	})
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, 2*time.Second, 20*time.Millisecond)

	// Give a brief window for any unexpected extra deliveries.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, m := range received {
		assert.NotEqual(t, "$/event", m, "$/event must not be forwarded to handler")
		assert.NotEqual(t, "session/update", m, "legacy session/update must not be forwarded")
	}
	assert.Contains(t, received, api.MethodShimEvent)
}

// ────────────────────────────────────────────────────────────────────────────
// runtime/* method tests
// ────────────────────────────────────────────────────────────────────────────

func TestShimClientStatus(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	status, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", status.State.OarVersion)
	assert.Equal(t, "test-session", status.State.ID)
	assert.Equal(t, api.StatusIdle, status.State.Status)
	assert.Equal(t, "/tmp/test-bundle", status.State.Bundle)
	assert.Equal(t, -1, status.Recovery.LastSeq)
}

func TestShimClientStatusRecoveryMetadata(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	// Advance the recovery sequence to simulate post-prompt state.
	srv.mu.Lock()
	srv.statusResult.Recovery.LastSeq = 7
	srv.statusResult.State.Status = api.StatusRunning
	srv.mu.Unlock()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	status, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 7, status.Recovery.LastSeq)
	assert.Equal(t, api.StatusRunning, status.State.Status)
}

func TestShimClientHistoryEmpty(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	result, err := c.History(context.Background(), nil)
	require.NoError(t, err)
	// Empty history is valid; Entries should not be nil.
	assert.NotNil(t, result.Entries)
	assert.Empty(t, result.Entries)
}

func TestShimClientHistoryWithFromSeq(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	fromSeq := 0
	result, err := c.History(context.Background(), &fromSeq)
	require.NoError(t, err)
	assert.NotNil(t, result.Entries)
}

func TestShimClientStop(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	require.NoError(t, c.Stop(context.Background()))

	// Server should close the connection shortly after replying to runtime/stop.
	select {
	case <-c.DisconnectNotify():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected connection to be closed after runtime/stop")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Notification parsing helpers
// ────────────────────────────────────────────────────────────────────────────

func TestParseShimEvent_SessionText(t *testing.T) {
	contentBytes, _ := json.Marshal(events.TextEvent{Text: "hello"})
	raw, _ := json.Marshal(map[string]any{
		"runId":     "run-1",
		"sessionId": "s1",
		"seq":       5,
		"time":      "2026-01-01T00:00:00Z",
		"category":  "session",
		"type":      "text",
		"turnId":    "turn-001",
		"streamSeq": 3,
		"phase":     "acting",
		"content":   json.RawMessage(contentBytes),
	})

	p, err := ParseShimEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, "run-1", p.RunID)
	assert.Equal(t, "s1", p.SessionID)
	assert.Equal(t, 5, p.Seq)
	assert.Equal(t, "session", p.Category)
	assert.Equal(t, "text", p.Type)
	assert.Equal(t, "turn-001", p.TurnID)
	assert.Equal(t, 3, p.StreamSeq)
	assert.Equal(t, "acting", p.Phase)
	ev, ok := p.Content.(events.TextEvent)
	require.True(t, ok, "expected TextEvent content")
	assert.Equal(t, "hello", ev.Text)
}

func TestParseShimEvent_RuntimeStateChange(t *testing.T) {
	contentBytes, _ := json.Marshal(events.StateChangeEvent{
		PreviousStatus: "created",
		Status:         "running",
		PID:            42,
		Reason:         "prompt",
	})
	raw, _ := json.Marshal(map[string]any{
		"runId":    "run-1",
		"seq":      3,
		"time":     "2026-01-01T00:00:00Z",
		"category": "runtime",
		"type":     "state_change",
		"content":  json.RawMessage(contentBytes),
	})

	p, err := ParseShimEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, "runtime", p.Category)
	assert.Equal(t, "state_change", p.Type)
	assert.Equal(t, 3, p.Seq)
	sc, ok := p.Content.(events.StateChangeEvent)
	require.True(t, ok)
	assert.Equal(t, "created", sc.PreviousStatus)
	assert.Equal(t, "running", sc.Status)
	assert.Equal(t, 42, sc.PID)
	assert.Equal(t, "prompt", sc.Reason)
}

func TestParseShimEventMalformed(t *testing.T) {
	_, err := ParseShimEvent(json.RawMessage(`{not valid json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse shim/event")
}

// ────────────────────────────────────────────────────────────────────────────
// Multi-method and error-path tests
// ────────────────────────────────────────────────────────────────────────────

func TestShimClientMultipleMethods(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	// Status
	status, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-session", status.State.ID)

	// Cancel
	require.NoError(t, c.Cancel(context.Background()))

	// Prompt
	result, err := c.Prompt(context.Background(), "Test prompt")
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)

	// Subscribe
	subResult, err := c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, subResult.NextSeq)

	// Status again
	status, err = c.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-session", status.State.ID)
}

func TestShimClientUnknownMethod(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	// Calling a legacy method name must return an error.
	err = c.call(context.Background(), "GetState", nil, nil)
	require.Error(t, err)

	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, int64(jsonrpc2.CodeMethodNotFound), rpcErr.Code)
}

func TestShimClientConcurrentCalls(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = c.Status(context.Background())
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}
}

func TestShimClientRepeatedSubscribe(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	// Repeated Subscribe calls on the same connection are valid.
	_, err = c.Subscribe(context.Background(), nil, nil)
	require.NoError(t, err)

	after := 3
	_, err = c.Subscribe(context.Background(), &after, nil)
	require.NoError(t, err)
}

func TestShimClientDialAfterServerClose(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	srv.close()

	// Dial to a closed server must fail with a contextual error.
	_, err := Dial(context.Background(), socketPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shim_client:")
}

// ────────────────────────────────────────────────────────────────────────────
// session/load tests
// ────────────────────────────────────────────────────────────────────────────

// TestShimClient_Load_Success verifies that Load sends session/load with the
// correct sessionId and returns nil on success.
func TestShimClient_Load_Success(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	const wantSessionID = "reload-session-abc123"
	err = c.Load(context.Background(), wantSessionID)
	require.NoError(t, err)

	srv.mu.Lock()
	called := srv.loadCalled
	calledWith := srv.loadCalledWith
	srv.mu.Unlock()

	assert.True(t, called, "shim should have received session/load")
	assert.Equal(t, wantSessionID, calledWith, "session/load should carry the correct sessionId")
}

// TestShimClient_Load_RpcError verifies that Load propagates RPC errors from
// the shim so the caller can fall back (e.g. try_reload policy).
func TestShimClient_Load_RpcError(t *testing.T) {
	srv, socketPath := newMockShimServer(t)
	defer srv.close()

	srv.mu.Lock()
	srv.loadSessionErr = fmt.Errorf("runtime does not support session/load")
	srv.mu.Unlock()

	c, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer c.Close()

	err = c.Load(context.Background(), "any-session-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session/load")
}
