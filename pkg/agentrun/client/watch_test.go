package client_test

import (
	"context"
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
	"github.com/zoumo/mass/pkg/watch"
)

// replayService implements server.Handler with configurable replay behavior.
// On each WatchEvent call, it sends events [fromSeq, totalEvents) as
// notifications stamped with the client-provided watchID.
type replayService struct {
	totalEvents int
}

func (s *replayService) Prompt(context.Context, *runapi.SessionPromptParams) (*runapi.SessionPromptResult, error) {
	return &runapi.SessionPromptResult{}, nil
}
func (s *replayService) Cancel(context.Context) error                          { return nil }
func (s *replayService) Load(context.Context, *runapi.SessionLoadParams) error { return nil }
func (s *replayService) SetModel(context.Context, *runapi.SessionSetModelParams) (*runapi.SessionSetModelResult, error) {
	return &runapi.SessionSetModelResult{}, nil
}

func (s *replayService) Status(context.Context) (*runapi.RuntimePhaseResult, error) {
	return &runapi.RuntimePhaseResult{}, nil
}
func (s *replayService) Stop(context.Context) error { return nil }

func (s *replayService) WatchEvent(ctx context.Context, req *runapi.SessionWatchEventParams, watchID string) (*runapi.SessionWatchEventResult, error) {
	peer := jsonrpc.PeerFromContext(ctx)

	fromSeq := 0
	if req.FromSeq != nil {
		fromSeq = *req.FromSeq
	}

	go func() {
		for i := fromSeq; i < s.totalEvents; i++ {
			ev := runapi.AgentRunEvent{
				WatchID: watchID,
				RunID:   "run-replay",
				Seq:     i,
				Type:    runapi.EventTypeTurnStart,
				Payload: runapi.TurnStartEvent{},
			}
			if err := peer.Notify(ctx, runapi.MethodRuntimeEventUpdate, ev); err != nil {
				return
			}
		}
	}()

	return &runapi.SessionWatchEventResult{WatchID: watchID, NextSeq: s.totalEvents}, nil
}

func startReplayServer(t *testing.T, svc runserver.Handler) string {
	t.Helper()
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("replay-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(sockPath)

	srv := jsonrpc.NewServer(slog.Default())
	runserver.Register(srv, svc)

	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()

	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond)

	t.Cleanup(func() {
		ln.Close()
		os.Remove(sockPath)
	})
	return sockPath
}

// TestRetryWatcher_LargeReplay verifies that RetryWatcher recovers all events
// even when the server sends more replay notifications than the WatchStream
// buffer (256). The sequence:
//
//  1. Server sends 10000 events starting at seq 0.
//  2. WatchStream buffer fills → routeWatchEvent evicts the stream.
//  3. RetryWatcher detects eviction (ResultChan closed), reconnects from
//     cursor+1 (last successfully delivered seq + 1).
//  4. Server replays from the new fromSeq.
//  5. Repeat until all events are delivered.
//
// This replaces the old TestWatchEventConn_LargeReplayBeforeWatcherDrain.
func TestRetryWatcher_LargeReplay(t *testing.T) {
	const totalEvents = 10000

	sockPath := startReplayServer(t, &replayService{totalEvents: totalEvents})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rw := watch.NewRetryWatcher(
		ctx,
		runclient.NewWatchFunc(sockPath),
		-1,
		func(ev runapi.AgentRunEvent) int { return ev.Seq },
		64, // small queue to exercise eviction + reconnect
	)
	defer rw.Stop()

	seen := make(map[int]bool, totalEvents)
	for {
		select {
		case ev, ok := <-rw.ResultChan():
			if !ok {
				t.Fatalf("ResultChan closed after %d events (expected %d)", len(seen), totalEvents)
			}
			seen[ev.Seq] = true
			if len(seen) == totalEvents {
				for i := 0; i < totalEvents; i++ {
					assert.True(t, seen[i], "missing event seq %d", i)
				}
				t.Logf("received all %d events via eviction+reconnect", totalEvents)
				return
			}
		case <-ctx.Done():
			t.Fatalf("timeout: received %d/%d events", len(seen), totalEvents)
		}
	}
}

// TestRetryWatcher_CursorAdvancesAfterEnqueue verifies that the cursor is only
// updated after an event is successfully written to the result channel.
func TestRetryWatcher_CursorAdvancesAfterEnqueue(t *testing.T) {
	const totalEvents = 10

	sockPath := startReplayServer(t, &replayService{totalEvents: totalEvents})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rw := watch.NewRetryWatcher(
		ctx,
		runclient.NewWatchFunc(sockPath),
		-1,
		func(ev runapi.AgentRunEvent) int { return ev.Seq },
		256,
	)
	defer rw.Stop()

	for i := 0; i < totalEvents; i++ {
		select {
		case <-rw.ResultChan():
		case <-ctx.Done():
			t.Fatalf("timeout at event %d", i)
		}
	}

	assert.Equal(t, totalEvents-1, rw.Cursor(), "cursor should be last delivered seq")
}
