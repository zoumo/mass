package client_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	shimclient "github.com/zoumo/mass/pkg/shim/client"
	"github.com/zoumo/mass/pkg/jsonrpc"
	shimserver "github.com/zoumo/mass/pkg/shim/server"
)

// stubShimService is a minimal ShimService for testing client round-trips.
type stubShimService struct {
	promptResult apishim.SessionPromptResult
}

func (s *stubShimService) Prompt(_ context.Context, req *apishim.SessionPromptParams) (*apishim.SessionPromptResult, error) {
	return &s.promptResult, nil
}
func (s *stubShimService) Cancel(_ context.Context) error { return nil }
func (s *stubShimService) Load(_ context.Context, _ *apishim.SessionLoadParams) error {
	return nil
}
func (s *stubShimService) Subscribe(_ context.Context, _ *apishim.SessionSubscribeParams) (*apishim.SessionSubscribeResult, error) {
	return &apishim.SessionSubscribeResult{NextSeq: 0}, nil
}
func (s *stubShimService) Status(_ context.Context) (*apishim.RuntimeStatusResult, error) {
	return &apishim.RuntimeStatusResult{}, nil
}
func (s *stubShimService) History(_ context.Context, _ *apishim.RuntimeHistoryParams) (*apishim.RuntimeHistoryResult, error) {
	return &apishim.RuntimeHistoryResult{Entries: []apishim.ShimEvent{}}, nil
}
func (s *stubShimService) Stop(_ context.Context) error { return nil }

// startTestServer starts a jsonrpc.Server with RegisterShimService on a temp socket.
func startTestServer(t *testing.T, svc shimserver.ShimService) string {
	t.Helper()

	sockPath := filepath.Join(os.TempDir(), "shim-client-test-"+t.Name()+".sock")
	_ = os.Remove(sockPath)

	srv := jsonrpc.NewServer(nil)
	shimserver.RegisterShimService(srv, svc)

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
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	// Connection should be alive.
	select {
	case <-sc.DisconnectNotify():
		t.Fatal("should not be disconnected")
	default:
	}
}

func TestShimClient_Prompt(t *testing.T) {
	svc := &stubShimService{
		promptResult: apishim.SessionPromptResult{StopReason: "end_turn"},
	}
	sockPath := startTestServer(t, svc)

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.Prompt(context.Background(), &apishim.SessionPromptParams{Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "end_turn", result.StopReason)
}

func TestShimClient_Cancel(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	err = sc.Cancel(context.Background())
	assert.NoError(t, err)
}

func TestShimClient_Status(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.Status(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestShimClient_Subscribe(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.Subscribe(context.Background(), &apishim.SessionSubscribeParams{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.NextSeq)
}

func TestShimClient_History(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	result, err := sc.History(context.Background(), &apishim.RuntimeHistoryParams{})
	require.NoError(t, err)
	assert.Empty(t, result.Entries)
}

func TestShimClient_Stop(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	sc, err := shimclient.Dial(context.Background(), sockPath)
	require.NoError(t, err)
	defer sc.Close()

	err = sc.Stop(context.Background())
	assert.NoError(t, err)
}

func TestParseShimEvent(t *testing.T) {
	ev := apishim.ShimEvent{
		RunID:     "run-1",
		SessionID: "sess-1",
		Seq:       0,
		Type:      apishim.EventTypeAgentMessage,
	}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)

	parsed, err := shimclient.ParseShimEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, "run-1", parsed.RunID)
	assert.Equal(t, "sess-1", parsed.SessionID)
	assert.Equal(t, 0, parsed.Seq)
	assert.Equal(t, apishim.EventTypeAgentMessage, parsed.Type)
}

func TestDialWithHandler(t *testing.T) {
	sockPath := startTestServer(t, &stubShimService{})

	received := make(chan struct{}, 1)
	handler := func(_ context.Context, method string, _ json.RawMessage) {
		if method == apishim.MethodShimEvent {
			select {
			case received <- struct{}{}:
			default:
			}
		}
	}

	sc, err := shimclient.DialWithHandler(context.Background(), sockPath, handler)
	require.NoError(t, err)
	defer sc.Close()

	// Verify connection works.
	_, err = sc.Status(context.Background())
	assert.NoError(t, err)
}
