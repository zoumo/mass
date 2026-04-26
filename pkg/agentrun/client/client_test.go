package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	runserver "github.com/zoumo/mass/pkg/agentrun/server"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// stubRunService is a minimal Handler for testing client round-trips.
type stubRunService struct {
	promptResult runapi.SessionPromptResult
}

func (s *stubRunService) Prompt(_ context.Context, req *runapi.SessionPromptParams) (*runapi.SessionPromptResult, error) {
	return &s.promptResult, nil
}
func (s *stubRunService) Cancel(_ context.Context) error { return nil }
func (s *stubRunService) Load(_ context.Context, _ *runapi.SessionLoadParams) error {
	return nil
}

func (s *stubRunService) WatchEvent(_ context.Context, _ *runapi.SessionWatchEventParams, watchID string) (*runapi.SessionWatchEventResult, error) {
	return &runapi.SessionWatchEventResult{WatchID: watchID, NextSeq: 0}, nil
}

func (s *stubRunService) Status(_ context.Context) (*runapi.RuntimePhaseResult, error) {
	return &runapi.RuntimePhaseResult{}, nil
}

func (s *stubRunService) SetModel(_ context.Context, _ *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error) {
	return &runapi.SessionSetModelResult{}, nil
}
func (s *stubRunService) Stop(_ context.Context) error { return nil }

// startTestServer starts a jsonrpc.Server with Register on a temp socket.
func startTestServer(t *testing.T, svc runserver.Handler) string {
	t.Helper()

	// Short path to avoid macOS's 104-char Unix socket path limit.
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("run-ct-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(sockPath)

	srv := jsonrpc.NewServer(slog.Default())
	runserver.Register(srv, svc)

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	go func() { _ = srv.Serve(ln) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
		_ = os.Remove(sockPath)
	})

	return sockPath
}

func TestDial(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	// Connection should be alive.
	select {
	case <-sc.DisconnectNotify():
		t.Fatal("should not be disconnected")
	default:
	}
}

func TestClient_Prompt(t *testing.T) {
	svc := &stubRunService{
		promptResult: runapi.SessionPromptResult{StopReason: "end_turn"},
	}
	sockPath := startTestServer(t, svc)

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.Prompt(context.Background(), &runapi.SessionPromptParams{Prompt: []runapi.ContentBlock{runapi.TextBlock("hello")}})
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)
}

func TestClient_Cancel(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	err = sc.Cancel(context.Background())
	assert.NoError(t, err)
}

func TestClient_Status(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.Status(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestClient_WatchEvent(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	watcher, err := sc.WatchEvent(context.Background(), &runapi.SessionWatchEventParams{})
	require.NoError(t, err)
	watcher.Stop()
}

func TestClient_Stop(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	err = sc.Stop(context.Background())
	assert.NoError(t, err)
}

func TestParseAgentRunEvent(t *testing.T) {
	ev := runapi.AgentRunEvent{
		RunID:     "run-1",
		SessionID: "sess-1",
		Seq:       0,
		Type:      runapi.EventTypeAgentMessage,
	}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)

	parsed, err := runclient.ParseAgentRunEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, "run-1", parsed.RunID)
	assert.Equal(t, "sess-1", parsed.SessionID)
	assert.Equal(t, 0, parsed.Seq)
	assert.Equal(t, runapi.EventTypeAgentMessage, parsed.Type)
}

func TestClient_WatchEvent_WatcherStopIdempotent(t *testing.T) {
	sockPath := startTestServer(t, &stubRunService{})

	sc, err := runclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	watcher, err := sc.WatchEvent(context.Background(), &runapi.SessionWatchEventParams{})
	require.NoError(t, err)

	// Stop should be idempotent — calling multiple times should not panic.
	watcher.Stop()
	watcher.Stop()
	watcher.Stop()
}
