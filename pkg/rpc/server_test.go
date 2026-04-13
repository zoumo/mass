package rpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/oar/api"
	apispec "github.com/zoumo/oar/api/spec"
	"github.com/zoumo/oar/pkg/events"
	"github.com/zoumo/oar/pkg/rpc"
	pkgruntime "github.com/zoumo/oar/pkg/runtime"
	"github.com/zoumo/oar/pkg/shimapi"
	"github.com/zoumo/oar/pkg/spec"
)

var mockAgentBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mockagent-rpc-*")
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
	_, filename, _, _ := goruntime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..")

	binPath := filepath.Join(tmpDir, "mockagent")
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/zoumo/oar/internal/testutil/mockagent")
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

func testConfig(name string) apispec.Config {
	return apispec.Config{
		OarVersion: "0.1.0",
		Metadata:   apispec.Metadata{Name: name},
		AgentRoot:  apispec.AgentRoot{Path: "workspace"},
		AcpAgent: apispec.AcpAgent{
			Process: apispec.AcpProcess{Command: mockAgentBin, Args: []string{}},
		},
		Permissions: apispec.ApproveAll,
	}
}

type notifHandler struct {
	mu   sync.Mutex
	evts []events.Envelope
	ch   chan events.Envelope
}

func newNotifHandler() *notifHandler {
	return &notifHandler{ch: make(chan events.Envelope, 128)}
}

func (h *notifHandler) Handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif {
		return
	}
	if req.Method != events.MethodSessionUpdate && req.Method != events.MethodRuntimeStateChange {
		return
	}
	if req.Params == nil {
		return
	}

	wire, err := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{Method: req.Method, Params: *req.Params})
	if err != nil {
		return
	}

	var env events.Envelope
	if err := json.Unmarshal(wire, &env); err != nil {
		return
	}

	h.mu.Lock()
	h.evts = append(h.evts, env)
	h.mu.Unlock()
	select {
	case h.ch <- env:
	default:
	}
}

func (h *notifHandler) collect(want int, timeout time.Duration) []events.Envelope {
	var out []events.Envelope
	deadline := time.After(timeout)
	for len(out) < want {
		select {
		case env := <-h.ch:
			out = append(out, env)
		case <-deadline:
			return out
		}
	}
	return out
}

type serverHarness struct {
	mgr     *pkgruntime.Manager
	trans   *events.Translator
	evLog   *events.EventLog
	srv     *rpc.Server
	socket  string
	logPath string

	serveErr  chan error
	stateDir  string
	bundleDir string
}

func newServerHarness(t *testing.T) *serverHarness {
	t.Helper()
	return newServerHarnessWithConfig(t, testConfig(t.Name()))
}

func newServerHarnessWithConfig(t *testing.T, cfg apispec.Config) *serverHarness {
	t.Helper()

	bundleDir, err := os.MkdirTemp("", "oad-bnd-")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(bundleDir, "workspace"), 0o755))

	stateDir, err := os.MkdirTemp("", "oad-st-")
	require.NoError(t, err)

	socketPath := spec.ShimSocketPath(stateDir)
	logPath := spec.EventLogPath(stateDir)

	mgr := pkgruntime.New(cfg, bundleDir, stateDir, slog.Default())
	agentCtx, agentCancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(agentCancel)
	require.NoError(t, mgr.Create(agentCtx))

	evLog, err := events.OpenEventLog(logPath)
	require.NoError(t, err)

	trans := events.NewTranslator(t.Name(), mgr.Events(), evLog)
	mgr.SetStateChangeHook(func(change pkgruntime.StateChange) {
		trans.NotifyStateChange(change.PreviousStatus.String(), change.Status.String(), change.PID, change.Reason)
	})
	trans.Start()

	srv := rpc.New(mgr, trans, socketPath, logPath, slog.Default())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "server socket did not appear")

	h := &serverHarness{
		mgr:       mgr,
		trans:     trans,
		evLog:     evLog,
		srv:       srv,
		socket:    socketPath,
		logPath:   logPath,
		serveErr:  serveErr,
		stateDir:  stateDir,
		bundleDir: bundleDir,
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = srv.Shutdown(cleanupCtx)
		trans.Stop()
		_ = evLog.Close()

		select {
		case <-serveErr:
		case <-cleanupCtx.Done():
		}
		require.Eventually(t, func() bool {
			st, err := mgr.GetState()
			return err == nil && st.Status == api.StatusStopped
		}, 5*time.Second, 20*time.Millisecond, "expected agent status=stopped before cleanup")

		_ = os.RemoveAll(stateDir)
		_ = os.RemoveAll(bundleDir)
	})

	return h
}

func (h *serverHarness) dial(t *testing.T, handler jsonrpc2.Handler) *jsonrpc2.Conn {
	t.Helper()
	nc, err := net.Dial("unix", h.socket)
	require.NoError(t, err)
	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(handler))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (h *serverHarness) dialSerialized(t *testing.T, handler jsonrpc2.Handler) *jsonrpc2.Conn {
	t.Helper()
	nc, err := net.Dial("unix", h.socket)
	require.NoError(t, err)
	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(context.Background(), stream, handler)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRPCServer_CleanBreakSurface(t *testing.T) {
	h := newServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	notifH := newNotifHandler()
	client := h.dial(t, notifH)

	t.Run("subscribe and status", func(t *testing.T) {
		var subResult shimapi.SessionSubscribeResult
		err := client.Call(ctx, "session/subscribe", nil, &subResult)
		require.NoError(t, err)
		require.Equal(t, 0, subResult.NextSeq)

		var status shimapi.RuntimeStatusResult
		err = client.Call(ctx, "runtime/status", nil, &status)
		require.NoError(t, err)
		require.Equal(t, api.StatusIdle, status.State.Status)
		require.Equal(t, -1, status.Recovery.LastSeq)
	})

	t.Run("prompt history and recovery metadata", func(t *testing.T) {
		var promptResult shimapi.SessionPromptResult
		err := client.Call(ctx, "session/prompt", shimapi.SessionPromptParams{Prompt: "hello"}, &promptResult)
		require.NoError(t, err)
		require.Equal(t, "end_turn", promptResult.StopReason)

		live := notifH.collect(6, 10*time.Second)
		require.Len(t, live, 6)
		sortEnvelopesBySeq(live)

		seq0, err := live[0].Seq()
		require.NoError(t, err)
		require.Equal(t, 0, seq0)
		// live[0]=turn_start, live[1]=user_message, live[2]=stateChange(idle→running),
		// live[3]=text(mock response), live[4]=stateChange(running→idle),
		// live[5]=turn_end
		require.Equal(t, events.MethodSessionUpdate, live[0].Method)
		require.Equal(t, events.MethodSessionUpdate, live[1].Method)       // user_message
		require.Equal(t, events.MethodRuntimeStateChange, live[2].Method)
		require.Equal(t, events.MethodRuntimeStateChange, live[4].Method)

		// Assert turn_start (live[0]) has TurnId
		ts := live[0].Params.(events.SessionUpdateParams)
		require.NotEmpty(t, ts.TurnID)
		// Assert all session/update events in turn share the same TurnId
		for _, idx := range []int{0, 1, 3, 5} {
			p := live[idx].Params.(events.SessionUpdateParams)
			require.Equal(t, ts.TurnID, p.TurnID, "live[%d] TurnId mismatch", idx)
		}

		var history shimapi.RuntimeHistoryResult
		err = client.Call(ctx, "runtime/history", shimapi.RuntimeHistoryParams{FromSeq: intPtr(0)}, &history)
		require.NoError(t, err)
		require.Equal(t, live, history.Entries)

		var status shimapi.RuntimeStatusResult
		err = client.Call(ctx, "runtime/status", nil, &status)
		require.NoError(t, err)
		require.Equal(t, api.StatusIdle, status.State.Status)
		require.Equal(t, 5, status.Recovery.LastSeq) // 6 events, seq 0-5
	})

	t.Run("subscribe with fromSeq returns backfill", func(t *testing.T) {
		// Open a second client. Call session/subscribe with fromSeq=0.
		// We already generated 6 events from the first prompt above.
		backfillNotifs := newNotifHandler()
		backfillClient := h.dial(t, backfillNotifs)

		var subResult shimapi.SessionSubscribeResult
		fromSeq := 0
		err := backfillClient.Call(ctx, "session/subscribe", shimapi.SessionSubscribeParams{FromSeq: &fromSeq}, &subResult)
		require.NoError(t, err)
		require.Len(t, subResult.Entries, 6, "expected 6 backfill entries from first prompt")
		sortEnvelopesBySeq(subResult.Entries)
		for i, env := range subResult.Entries {
			seq, seqErr := env.Seq()
			require.NoError(t, seqErr)
			require.Equal(t, i, seq, "backfill entry %d has wrong seq", i)
		}

		// Trigger a second prompt and assert live events arrive.
		var promptResult shimapi.SessionPromptResult
		err = client.Call(ctx, "session/prompt", shimapi.SessionPromptParams{Prompt: "second"}, &promptResult)
		require.NoError(t, err)
		require.Equal(t, "end_turn", promptResult.StopReason)

		live := backfillNotifs.collect(6, 10*time.Second)
		require.Len(t, live, 6, "expected 6 live events from second prompt")
		for _, env := range live {
			seq, seqErr := env.Seq()
			require.NoError(t, seqErr)
			require.GreaterOrEqual(t, seq, 6, "live events should have seq >= 6")
		}
	})

	t.Run("subscribe afterSeq filters prior history", func(t *testing.T) {
		// Get the current nextSeq from status so the afterSeq floor is correct
		// even if earlier subtests generated additional events.
		var curStatus shimapi.RuntimeStatusResult
		err := client.Call(ctx, "runtime/status", nil, &curStatus)
		require.NoError(t, err)
		afterSeq := curStatus.Recovery.LastSeq

		secondaryNotifs := newNotifHandler()
		secondaryClient := h.dial(t, secondaryNotifs)

		var subResult shimapi.SessionSubscribeResult
		err = secondaryClient.Call(ctx, "session/subscribe", shimapi.SessionSubscribeParams{AfterSeq: &afterSeq}, &subResult)
		require.NoError(t, err)
		require.Equal(t, afterSeq+1, subResult.NextSeq)

		select {
		case env := <-secondaryNotifs.ch:
			t.Fatalf("unexpected replayed live notification: %#v", env)
		case <-time.After(200 * time.Millisecond):
		}

		var promptResult shimapi.SessionPromptResult
		err = client.Call(ctx, "session/prompt", shimapi.SessionPromptParams{Prompt: "again"}, &promptResult)
		require.NoError(t, err)
		require.Equal(t, "end_turn", promptResult.StopReason)

		live := secondaryNotifs.collect(5, 10*time.Second)
		require.Len(t, live, 5)
		for _, env := range live {
			seq, seqErr := env.Seq()
			require.NoError(t, seqErr)
			require.Greater(t, seq, afterSeq)
		}
	})
}

func TestRPCServer_DirectShimLiveOrdering(t *testing.T) {
	cfg := testConfig(t.Name())
	cfg.AcpAgent.Process.Env = []string{"OAR_MOCKAGENT_CHUNKS=20"}
	h := newServerHarnessWithConfig(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	notifH := newNotifHandler()
	client := h.dialSerialized(t, notifH)

	var subResult shimapi.SessionSubscribeResult
	require.NoError(t, client.Call(ctx, "session/subscribe", nil, &subResult))

	var promptResult shimapi.SessionPromptResult
	require.NoError(t, client.Call(ctx, "session/prompt", shimapi.SessionPromptParams{Prompt: "ordering"}, &promptResult))
	require.Equal(t, "end_turn", promptResult.StopReason)

	live := notifH.collect(24, 10*time.Second)
	require.Len(t, live, 24)

	lastSeq := -1
	var textChunks []string
	for _, env := range live {
		seq, err := env.Seq()
		require.NoError(t, err)
		require.Equal(t, lastSeq+1, seq, "live notification seq must match receive order")
		lastSeq = seq

		p, ok := env.Params.(events.SessionUpdateParams)
		if !ok {
			continue
		}
		if p.Event.Type != "text" {
			continue
		}
		payload, ok := p.Event.Payload.(events.TextEvent)
		require.True(t, ok)
		textChunks = append(textChunks, payload.Text)
	}

	require.Len(t, textChunks, 20)
	for i := 0; i < 20; i++ {
		require.Equal(t, fmt.Sprintf("text-chunk-%02d", i), textChunks[i])
	}
}

func TestRPCServer_RejectsLegacyAndInvalidParams(t *testing.T) {
	h := newServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, newNotifHandler())

	for _, method := range []string{"Prompt", "Cancel", "Subscribe", "GetState", "GetHistory", "Shutdown", "$/event"} {
		t.Run("legacy-"+method, func(t *testing.T) {
			var result any
			err := client.Call(ctx, method, nil, &result)
			require.Error(t, err)
			var rpcErr *jsonrpc2.Error
			require.ErrorAs(t, err, &rpcErr)
			require.Equal(t, int64(jsonrpc2.CodeMethodNotFound), rpcErr.Code)
		})
	}

	t.Run("session prompt missing params", func(t *testing.T) {
		var result shimapi.SessionPromptResult
		err := client.Call(ctx, "session/prompt", nil, &result)
		require.Error(t, err)
		assertRPCCode(t, err, jsonrpc2.CodeInvalidParams)
	})

	t.Run("session prompt empty prompt", func(t *testing.T) {
		var result shimapi.SessionPromptResult
		err := client.Call(ctx, "session/prompt", shimapi.SessionPromptParams{}, &result)
		require.Error(t, err)
		assertRPCCode(t, err, jsonrpc2.CodeInvalidParams)
	})

	t.Run("subscribe negative afterSeq", func(t *testing.T) {
		var result shimapi.SessionSubscribeResult
		neg := -1
		err := client.Call(ctx, "session/subscribe", shimapi.SessionSubscribeParams{AfterSeq: &neg}, &result)
		require.Error(t, err)
		assertRPCCode(t, err, jsonrpc2.CodeInvalidParams)
	})

	t.Run("subscribe negative fromSeq", func(t *testing.T) {
		var result shimapi.SessionSubscribeResult
		neg := -1
		err := client.Call(ctx, "session/subscribe", shimapi.SessionSubscribeParams{FromSeq: &neg}, &result)
		require.Error(t, err)
		assertRPCCode(t, err, jsonrpc2.CodeInvalidParams)
	})

	t.Run("history negative fromSeq", func(t *testing.T) {
		var result shimapi.RuntimeHistoryResult
		neg := -1
		err := client.Call(ctx, "runtime/history", shimapi.RuntimeHistoryParams{FromSeq: &neg}, &result)
		require.Error(t, err)
		assertRPCCode(t, err, jsonrpc2.CodeInvalidParams)
	})
}

func TestRPCServer_StopRepliesBeforeDisconnect(t *testing.T) {
	h := newServerHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := h.dial(t, newNotifHandler())

	var stopResult any
	err := client.Call(ctx, "runtime/stop", nil, &stopResult)
	require.NoError(t, err)

	select {
	case serveErr := <-h.serveErr:
		require.NoError(t, serveErr)
	case <-time.After(10 * time.Second):
		t.Fatal("Server.Serve did not exit within 10s after runtime/stop")
	}

	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", h.socket)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}, 3*time.Second, 50*time.Millisecond, "expected socket to be unavailable after runtime/stop")
}

func assertRPCCode(t *testing.T, err error, code int64) {
	t.Helper()
	var rpcErr *jsonrpc2.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, code, rpcErr.Code)
}

func sortEnvelopesBySeq(entries []events.Envelope) {
	sort.Slice(entries, func(i, j int) bool {
		left, leftErr := entries[i].Seq()
		right, rightErr := entries[j].Seq()
		if leftErr != nil || rightErr != nil {
			return i < j
		}
		return left < right
	})
}

func intPtr(v int) *int { return &v }
