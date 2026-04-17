package jsonrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// rawServer is a minimal jsonrpc2 handler for client-side tests.
// It dispatches method calls via a map of handler functions set with on().
type rawServer struct {
	mu       sync.RWMutex
	handlers map[string]func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request)
}

func newRawServer() *rawServer {
	return &rawServer{handlers: make(map[string]func(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request))}
}

func (s *rawServer) on(method string, fn func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request)) {
	s.mu.Lock()
	s.handlers[method] = fn
	s.mu.Unlock()
}

func (s *rawServer) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}
	s.mu.RLock()
	fn := s.handlers[req.Method]
	s.mu.RUnlock()
	if fn == nil {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "not found"})
		return
	}
	fn(ctx, conn, req)
}

// wireRawServer starts a raw jsonrpc2 server on one end of a net.Pipe and
// returns a pkg/jsonrpc Client wrapping the other end. Both connections are
// registered for t.Cleanup.
func wireRawServer(t *testing.T, srv *rawServer, opts ...jsonrpc.ClientOption) *jsonrpc.Client {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() {
		_ = srvConn.Close()
		_ = cliConn.Close()
	})
	go func() {
		stream := jsonrpc2.NewPlainObjectStream(srvConn)
		conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(srv))
		<-conn.DisconnectNotify()
	}()
	client := jsonrpc.NewClient(cliConn, opts...)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// wireRawServerCaptureConn is like wireRawServer but also delivers the server's
// jsonrpc2.Conn on a channel — useful when tests need to send notifications.
func wireRawServerCaptureConn(t *testing.T, srv *rawServer, opts ...jsonrpc.ClientOption) (*jsonrpc.Client, <-chan *jsonrpc2.Conn) {
	t.Helper()
	srvNC, cliNC := net.Pipe()
	t.Cleanup(func() {
		_ = srvNC.Close()
		_ = cliNC.Close()
	})
	connCh := make(chan *jsonrpc2.Conn, 1)
	go func() {
		stream := jsonrpc2.NewPlainObjectStream(srvNC)
		conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(srv))
		connCh <- conn
		<-conn.DisconnectNotify()
	}()
	client := jsonrpc.NewClient(cliNC, opts...)
	t.Cleanup(func() { _ = client.Close() })
	return client, connCh
}

// collectN reads exactly n items from ch within timeout, failing if it times out.
func collectN[T any](t *testing.T, ch <-chan T, n int, timeout time.Duration) []T {
	t.Helper()
	out := make([]T, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case v := <-ch:
			out = append(out, v)
		case <-deadline:
			t.Fatalf("collectN timed out after %d/%d items", len(out), n)
		}
	}
	return out
}

// ---- tests ----

func TestClient_Call(t *testing.T) {
	srv := newRawServer()
	srv.on("echo/hello", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		var p struct{ Name string }
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		_ = conn.Reply(ctx, req.ID, map[string]string{"greeting": "hello " + p.Name})
	})

	client := wireRawServer(t, srv)

	var result map[string]string
	err := client.Call(context.Background(), "echo/hello", map[string]string{"Name": "world"}, &result)
	require.NoError(t, err)
	assert.Equal(t, "hello world", result["greeting"])
}

func TestClient_ConcurrentCall(t *testing.T) {
	srv := newRawServer()
	srv.on("math/square", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		var p struct{ N int }
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		_ = conn.Reply(ctx, req.ID, map[string]int{"result": p.N * p.N})
	})

	client := wireRawServer(t, srv)

	const concurrency = 20
	var wg sync.WaitGroup
	errors := make([]error, concurrency)
	results := make([]int, concurrency)

	wg.Add(concurrency)
	for i := range concurrency {
		go func() {
			defer wg.Done()
			var r map[string]int
			err := client.Call(context.Background(), "math/square", map[string]int{"N": i}, &r)
			errors[i] = err
			if err == nil {
				results[i] = r["result"]
			}
		}()
	}
	wg.Wait()

	for i := range concurrency {
		require.NoError(t, errors[i], "goroutine %d", i)
		assert.Equal(t, i*i, results[i], "goroutine %d", i)
	}
}

func TestClient_CallWithNotification(t *testing.T) {
	// Server sends a notification before replying; client receives both correctly.
	srv := newRawServer()
	srv.on("svc/work", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		_ = conn.Notify(ctx, "svc/progress", map[string]string{"status": "working"})
		_ = conn.Reply(ctx, req.ID, "done")
	})

	notifReceived := make(chan string, 1)
	client := wireRawServer(t, srv, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			notifReceived <- method
		},
	))

	var result string
	err := client.Call(context.Background(), "svc/work", nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "done", result)

	select {
	case method := <-notifReceived:
		assert.Equal(t, "svc/progress", method)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestClient_NotificationHandler(t *testing.T) {
	srv := newRawServer()
	received := make(chan string, 4)
	client, connCh := wireRawServerCaptureConn(t, srv, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			received <- method
		},
	))
	_ = client

	serverConn := <-connCh

	_ = serverConn.Notify(context.Background(), "events/foo", nil)
	_ = serverConn.Notify(context.Background(), "events/bar", nil)

	got := collectN(t, received, 2, 5*time.Second)
	assert.Equal(t, []string{"events/foo", "events/bar"}, got)
}

func TestClient_NotificationOrder(t *testing.T) {
	srv := newRawServer()
	const n = 50
	received := make(chan int, n+4)
	_, connCh := wireRawServerCaptureConn(t, srv, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			var p struct{ Seq int }
			if err := json.Unmarshal(params, &p); err == nil {
				received <- p.Seq
			}
		},
	))

	serverConn := <-connCh
	for i := range n {
		_ = serverConn.Notify(context.Background(), "seq/tick", map[string]int{"Seq": i})
	}

	got := collectN(t, received, n, 5*time.Second)
	for i, v := range got {
		assert.Equal(t, i, v, "notification %d out of order", i)
	}
}

func TestClient_SlowNotificationHandler(t *testing.T) {
	// A slow notification handler must not delay the response from a Call.
	srv := newRawServer()
	srv.on("svc/ping", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		_ = conn.Notify(ctx, "svc/slow", nil)
		_ = conn.Reply(ctx, req.ID, "pong")
	})

	notifDone := make(chan struct{})
	client := wireRawServer(t, srv, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			time.Sleep(300 * time.Millisecond)
			close(notifDone)
		},
	))

	start := time.Now()
	var result string
	err := client.Call(context.Background(), "svc/ping", nil, &result)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "pong", result)
	// Response should arrive well before the slow handler finishes.
	assert.Less(t, elapsed, 200*time.Millisecond, "response blocked by slow notification handler")

	select {
	case <-notifDone:
	case <-time.After(3 * time.Second):
		t.Fatal("notification handler did not complete")
	}
}

func TestClient_NotificationBackpressure(t *testing.T) {
	// When the 256-element notification buffer is full, the read loop blocks
	// (backpressure). Once the worker catches up, everything drains correctly.
	const total = 300 // more than the 256-element buffer

	srvNC, cliNC := net.Pipe()
	defer srvNC.Close()
	defer cliNC.Close()

	srvConnCh := make(chan *jsonrpc2.Conn, 1)
	go func() {
		stream := jsonrpc2.NewPlainObjectStream(srvNC)
		conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(newRawServer()))
		srvConnCh <- conn
		<-conn.DisconnectNotify()
	}()
	serverConn := <-srvConnCh

	var received atomic.Int64
	release := make(chan struct{})
	first := make(chan struct{})
	var firstOnce sync.Once

	cli := jsonrpc.NewClient(cliNC, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			// Signal on the very first notification, then block until released.
			firstOnce.Do(func() { close(first) })
			<-release
			received.Add(1)
		},
	))
	defer func() { _ = cli.Close() }()

	// Send all notifications from server in a goroutine.
	go func() {
		for i := range total {
			_ = serverConn.Notify(context.Background(), "flood/tick", map[string]int{"i": i})
		}
	}()

	// Wait for the first notification to arrive, then unblock all.
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("no notifications received")
	}
	close(release)

	require.Eventually(t, func() bool {
		return received.Load() == int64(total)
	}, 10*time.Second, 10*time.Millisecond, "expected all %d notifications, got %d", total, received.Load())
}

func TestClient_ContextCancel(t *testing.T) {
	// Canceled context causes Call to return ctx.Err().
	// The connection must remain usable for subsequent calls.
	//
	// The svc/slow handler uses conn.DisconnectNotify() instead of an explicit
	// unblock channel so it exits when the connection closes — this avoids a
	// race where the server sends a late reply while the client is being
	// closed (which would close call.done, causing a send-on-closed-channel panic
	// in jsonrpc2's readMessages goroutine).
	srv := newRawServer()
	srv.on("svc/slow", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		// Block until the connection closes; never send a reply.
		<-conn.DisconnectNotify()
	})
	srv.on("svc/fast", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		_ = conn.Reply(ctx, req.ID, "fast-result")
	})

	client := wireRawServer(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		var result any
		callDone <- client.Call(ctx, "svc/slow", nil, &result)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-callDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("canceled call did not return")
	}

	// Connection should still be usable for subsequent calls.
	var fastResult string
	err := client.Call(context.Background(), "svc/fast", nil, &fastResult)
	require.NoError(t, err)
	assert.Equal(t, "fast-result", fastResult)
}

func TestClient_Close(t *testing.T) {
	// Close wakes all pending Calls and the notification worker exits cleanly.
	srvNC, cliNC := net.Pipe()
	defer srvNC.Close()

	srv := newRawServer()
	srv.on("svc/block", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		// Never replies — connection will be closed.
		<-conn.DisconnectNotify()
	})
	go func() {
		stream := jsonrpc2.NewPlainObjectStream(srvNC)
		conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(srv))
		<-conn.DisconnectNotify()
	}()

	client := jsonrpc.NewClient(cliNC)

	callDone := make(chan error, 1)
	go func() {
		var result any
		callDone <- client.Call(context.Background(), "svc/block", nil, &result)
	}()

	time.Sleep(20 * time.Millisecond)
	require.NoError(t, client.Close())

	select {
	case err := <-callDone:
		require.Error(t, err) // ErrClosed or similar
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not wake pending call")
	}
	// If we reach here, the notification worker exited (Close returned).
}

func TestClient_ResponseOutOfOrder(t *testing.T) {
	// Out-of-order server responses are correctly matched to pending calls.
	capturedReqs := make(chan reqCapture, 10)
	capHandler := &captureRequestHandler{ch: capturedReqs}

	srvNC, cliNC := net.Pipe()
	defer srvNC.Close()
	defer cliNC.Close()

	go func() {
		stream := jsonrpc2.NewPlainObjectStream(srvNC)
		conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(capHandler))
		<-conn.DisconnectNotify()
	}()

	client := jsonrpc.NewClient(cliNC)
	defer func() { _ = client.Close() }()

	// Send 3 concurrent calls.
	type callResult struct {
		n   int
		val string
		err error
	}
	resultsCh := make(chan callResult, 3)
	for i := range 3 {
		go func() {
			var r string
			err := client.Call(context.Background(), "svc/get", map[string]int{"n": i}, &r)
			resultsCh <- callResult{n: i, val: r, err: err}
		}()
	}

	// Collect all 3 requests on the server side.
	reqs := collectN(t, capturedReqs, 3, 5*time.Second)

	// Reply in reverse order.
	ctx := context.Background()
	for i := len(reqs) - 1; i >= 0; i-- {
		var p struct{ N int }
		if reqs[i].req.Params != nil {
			_ = json.Unmarshal(*reqs[i].req.Params, &p)
		}
		_ = reqs[i].conn.Reply(ctx, reqs[i].req.ID, fmt.Sprintf("result-%d", p.N))
	}

	// Verify each goroutine got its correct result.
	results := collectN(t, resultsCh, 3, 5*time.Second)
	got := make(map[int]string, 3)
	for _, r := range results {
		require.NoError(t, r.err)
		got[r.n] = r.val
	}
	for i := range 3 {
		assert.Equal(t, fmt.Sprintf("result-%d", i), got[i], "result for call %d", i)
	}
}

// reqCapture holds a captured server-side request.
type reqCapture struct {
	conn *jsonrpc2.Conn
	req  *jsonrpc2.Request
}

// captureRequestHandler captures requests without replying (used in ResponseOutOfOrder test).
type captureRequestHandler struct {
	ch chan reqCapture
}

func (h *captureRequestHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}
	h.ch <- reqCapture{conn: conn, req: req}
}

func TestClient_WithNotificationChannel(t *testing.T) {
	srv := newRawServer()
	ch := make(chan jsonrpc.NotificationMsg, 4)
	_, connCh := wireRawServerCaptureConn(t, srv, jsonrpc.WithNotificationChannel(ch))
	serverConn := <-connCh

	_ = serverConn.Notify(context.Background(), "events/a", map[string]int{"x": 1})
	_ = serverConn.Notify(context.Background(), "events/b", map[string]int{"x": 2})

	got := collectN(t, ch, 2, 5*time.Second)
	assert.Equal(t, "events/a", got[0].Method)
	assert.Equal(t, "events/b", got[1].Method)

	var p struct{ X int }
	require.NoError(t, json.Unmarshal(got[0].Params, &p))
	assert.Equal(t, 1, p.X)
}

func TestClient_WithNotificationChannelOrder(t *testing.T) {
	srv := newRawServer()
	const n = 50
	ch := make(chan jsonrpc.NotificationMsg, n+4)
	_, connCh := wireRawServerCaptureConn(t, srv, jsonrpc.WithNotificationChannel(ch))
	serverConn := <-connCh

	for i := range n {
		_ = serverConn.Notify(context.Background(), "seq/tick", map[string]int{"Seq": i})
	}

	got := collectN(t, ch, n, 5*time.Second)
	for i, msg := range got {
		var p struct{ Seq int }
		require.NoError(t, json.Unmarshal(msg.Params, &p))
		assert.Equal(t, i, p.Seq, "notification %d out of order", i)
	}
}

func TestClient_WithNotificationChannelAndHandlerPanics(t *testing.T) {
	assert.Panics(t, func() {
		srvConn, cliConn := net.Pipe()
		defer srvConn.Close()
		defer cliConn.Close()
		jsonrpc.NewClient(cliConn,
			jsonrpc.WithNotificationHandler(func(context.Context, string, json.RawMessage) {}),
			jsonrpc.WithNotificationChannel(make(chan jsonrpc.NotificationMsg, 1)),
		)
	})
}

func TestClient_CallAsync(t *testing.T) {
	srv := newRawServer()
	srv.on("svc/long", func(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		// Simulate a long-running method.
		time.Sleep(100 * time.Millisecond)
		_ = conn.Reply(ctx, req.ID, "done")
	})

	client := wireRawServer(t, srv)

	// CallAsync should return immediately.
	start := time.Now()
	err := client.CallAsync(context.Background(), "svc/long", nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 50*time.Millisecond, "CallAsync should return immediately")

	// Wait for the background call to complete.
	time.Sleep(200 * time.Millisecond)
}
