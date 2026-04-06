package agentd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ────────────────────────────────────────────────────────────────────────────
// Mock JSON-RPC Server
// ────────────────────────────────────────────────────────────────────────────

// mockServer is a minimal JSON-RPC server that mimics agent-shim behavior.
type mockServer struct {
	listener net.Listener
	conn     *jsonrpc2.Conn
	done     chan struct{}
	once     sync.Once

	mu           sync.Mutex
	state        GetStateResult
	promptResult PromptResult
	events       []EventNotification
	subscribed   bool
}

// newMockServer creates and starts a mock JSON-RPC server on a temp socket.
func newMockServer(t *testing.T) (*mockServer, string) {
	// Use a shorter path for the socket to avoid macOS Unix socket path length limits
	// (max ~107 chars). t.TempDir() paths can be too long.
	socketPath := filepath.Join("/tmp", fmt.Sprintf("agent-shim-test-%d.sock", time.Now().UnixNano()))

	// Ensure the socket file doesn't exist from a previous test
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	s := &mockServer{
		listener: ln,
		done:     make(chan struct{}),
		state: GetStateResult{
			OarVersion: "0.1.0",
			ID:         "test-session-id",
			Status:     "created",
			Bundle:     "/tmp/test-bundle",
		},
		promptResult: PromptResult{
			StopReason: "end_turn",
		},
	}

	go s.serve()

	// Cleanup when test finishes
	t.Cleanup(func() {
		s.close()
		_ = os.Remove(socketPath)
	})

	return s, socketPath
}

func (s *mockServer) serve() {
	for {
		nc, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				return
			}
		}

		stream := jsonrpc2.NewPlainObjectStream(nc)
		h := jsonrpc2.AsyncHandler(&mockHandler{srv: s})
		s.conn = jsonrpc2.NewConn(context.Background(), stream, h)
		<-s.conn.DisconnectNotify()
	}
}

func (s *mockServer) close() {
	s.once.Do(func() { close(s.done) })
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Mock Handler
// ────────────────────────────────────────────────────────────────────────────

type mockHandler struct {
	srv *mockServer
}

func (h *mockHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	switch req.Method {
	case "Prompt":
		h.handlePrompt(ctx, conn, req)
	case "Cancel":
		h.handleCancel(ctx, conn, req)
	case "Subscribe":
		h.handleSubscribe(ctx, conn, req)
	case "GetState":
		h.handleGetState(ctx, conn, req)
	case "Shutdown":
		h.handleShutdown(ctx, conn, req)
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: "unknown method",
		})
	}
}

func (h *mockHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	result := h.srv.promptResult
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, result)
}

func (h *mockHandler) handleCancel(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
}

func (h *mockHandler) handleSubscribe(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	h.srv.subscribed = true
	events := h.srv.events
	h.srv.mu.Unlock()

	_ = conn.Reply(ctx, req.ID, nil)

	// Stream events in a goroutine
	go func() {
		for _, ev := range events {
			_ = conn.Notify(ctx, "$/event", ev)
		}
	}()
}

func (h *mockHandler) handleGetState(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	h.srv.mu.Lock()
	state := h.srv.state
	h.srv.mu.Unlock()
	_ = conn.Reply(ctx, req.ID, state)
}

func (h *mockHandler) handleShutdown(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	_ = conn.Reply(ctx, req.ID, nil)
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.srv.close()
	}()
}

// ────────────────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────────────────

// TestShimClientDial tests basic connection to a mock shim server.
func TestShimClientDial(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	// Connection should be established
	assert.NotNil(t, client.conn)
	assert.Equal(t, socketPath, client.socketPath)
}

// TestShimClientDialFail tests connection failure when socket doesn't exist.
func TestShimClientDialFail(t *testing.T) {
	// Use a non-existent socket path
	socketPath := "/nonexistent/socket.sock"

	client, err := Dial(context.Background(), socketPath)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "dial")
}

// TestShimClientPrompt tests the Prompt RPC method.
func TestShimClientPrompt(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer client.Close()

	result, err := client.Prompt(context.Background(), "Hello, agent!")
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)
}

// TestShimClientCancel tests the Cancel RPC method.
func TestShimClientCancel(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.Cancel(context.Background())
	require.NoError(t, err)
}

// TestShimClientSubscribe tests the Subscribe RPC method with event handling.
func TestShimClientSubscribe(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	// Add test events to the mock server
	srv.mu.Lock()
	srv.events = []EventNotification{
		{Type: "text", Payload: map[string]any{"Text": "Hello"}},
		{Type: "turn_start", Payload: struct{}{}},
		{Type: "turn_end", Payload: map[string]any{"StopReason": "end_turn"}},
	}
	srv.mu.Unlock()

	// Collect received events
	var receivedEvents []events.Event
	var mu sync.Mutex

	handler := func(ctx context.Context, ev events.Event) {
		mu.Lock()
		receivedEvents = append(receivedEvents, ev)
		mu.Unlock()
	}

	client, err := DialWithHandler(context.Background(), socketPath, handler)
	require.NoError(t, err)
	defer client.Close()

	err = client.Subscribe(context.Background())
	require.NoError(t, err)

	// Wait for events to be received
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	evts := receivedEvents
	mu.Unlock()

	require.Len(t, evts, 3)

	// Check that all expected event types are present (order not guaranteed due to AsyncHandler)
	hasText := false
	hasTurnStart := false
	hasTurnEnd := false
	for _, ev := range evts {
		switch ev.(type) {
		case events.TextEvent:
			hasText = true
		case events.TurnStartEvent:
			hasTurnStart = true
		case events.TurnEndEvent:
			hasTurnEnd = true
		}
	}
	assert.True(t, hasText, "should have received text event")
	assert.True(t, hasTurnStart, "should have received turn_start event")
	assert.True(t, hasTurnEnd, "should have received turn_end event")
}

// TestShimClientGetState tests the GetState RPC method.
func TestShimClientGetState(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer client.Close()

	state, err := client.GetState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", state.OarVersion)
	assert.Equal(t, "test-session-id", state.ID)
	assert.Equal(t, "created", string(state.Status))
	assert.Equal(t, "/tmp/test-bundle", state.Bundle)
}

// TestShimClientShutdown tests the Shutdown RPC method.
func TestShimClientShutdown(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	err = client.Shutdown(context.Background())
	require.NoError(t, err)

	// Wait for the server to close the connection
	time.Sleep(50 * time.Millisecond)

	// Connection should be disconnected
	select {
	case <-client.DisconnectNotify():
		// Expected - connection closed
	default:
		t.Error("expected connection to be closed after shutdown")
	}
}

// TestShimClientClose tests explicit client close without Shutdown.
func TestShimClientClose(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	err = client.Close()
	require.NoError(t, err)

	// Connection should be disconnected
	select {
	case <-client.DisconnectNotify():
		// Expected - connection closed
	case <-time.After(100 * time.Millisecond):
		t.Error("expected connection to be closed")
	}
}

// TestShimClientDisconnectNotify tests the disconnect notification channel.
func TestShimClientDisconnectNotify(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)

	disconnectCh := client.DisconnectNotify()
	assert.NotNil(t, disconnectCh)

	// Initially not disconnected
	select {
	case <-disconnectCh:
		t.Error("should not be disconnected initially")
	default:
		// Expected
	}

	// Close the server
	srv.close()

	// Now should be disconnected
	select {
	case <-disconnectCh:
		// Expected
	case <-time.After(200 * time.Millisecond):
		t.Error("should be disconnected after server closes")
	}
}

// TestParseEvent tests the parseEvent helper function.
func TestParseEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   any
		check     func(events.Event) bool
	}{
		{
			name:      "text event",
			eventType: "text",
			payload:   map[string]any{"Text": "Hello world"},
			check: func(ev events.Event) bool {
				textEv, ok := ev.(events.TextEvent)
				return ok && textEv.Text == "Hello world"
			},
		},
		{
			name:      "thinking event",
			eventType: "thinking",
			payload:   map[string]any{"Text": "Let me think..."},
			check: func(ev events.Event) bool {
				thinkingEv, ok := ev.(events.ThinkingEvent)
				return ok && thinkingEv.Text == "Let me think..."
			},
		},
		{
			name:      "turn_start event",
			eventType: "turn_start",
			payload:   map[string]any{},
			check: func(ev events.Event) bool {
				_, ok := ev.(events.TurnStartEvent)
				return ok
			},
		},
		{
			name:      "turn_end event",
			eventType: "turn_end",
			payload:   map[string]any{"StopReason": "end_turn"},
			check: func(ev events.Event) bool {
				endEv, ok := ev.(events.TurnEndEvent)
				return ok && endEv.StopReason == "end_turn"
			},
		},
		{
			name:      "tool_call event",
			eventType: "tool_call",
			payload:   map[string]any{"ID": "call-123", "Kind": "function", "Title": "Read file"},
			check: func(ev events.Event) bool {
				toolEv, ok := ev.(events.ToolCallEvent)
				return ok && toolEv.ID == "call-123" && toolEv.Kind == "function"
			},
		},
		{
			name:      "error event",
			eventType: "error",
			payload:   map[string]any{"Msg": "something went wrong"},
			check: func(ev events.Event) bool {
				errEv, ok := ev.(events.ErrorEvent)
				return ok && errEv.Msg == "something went wrong"
			},
		},
		{
			name:      "unknown event type",
			eventType: "unknown_type",
			payload:   map[string]any{},
			check: func(ev events.Event) bool {
				errEv, ok := ev.(events.ErrorEvent)
				return ok && errEv.Msg == "unknown event type: unknown_type"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := parseEvent(tt.eventType, tt.payload)
			assert.True(t, tt.check(ev), "event check failed for type %s", tt.eventType)
		})
	}
}

// TestShimClientMultipleMethods tests calling multiple RPC methods in sequence.
func TestShimClientMultipleMethods(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer client.Close()

	// GetState
	state, err := client.GetState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-session-id", state.ID)

	// Cancel
	err = client.Cancel(context.Background())
	require.NoError(t, err)

	// Prompt
	result, err := client.Prompt(context.Background(), "Test prompt")
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)

	// GetState again
	state, err = client.GetState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-session-id", state.ID)
}

// TestShimClientConcurrentCalls tests concurrent RPC calls.
func TestShimClientConcurrentCalls(t *testing.T) {
	srv, socketPath := newMockServer(t)
	defer srv.close()

	client, err := Dial(context.Background(), socketPath)
	require.NoError(t, err)
	defer client.Close()

	// Make concurrent calls
	var wg sync.WaitGroup
	errors := make([]error, 10)
	
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := client.GetState(context.Background())
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	for _, err := range errors {
		assert.NoError(t, err)
	}
}