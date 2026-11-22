package codex

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/agentrelay"
)

type reconciliationReader struct {
	codexReadClient
	list func(ct.ThreadTurnsListParams) (ct.ThreadTurnsListResponse, error)
}

func (r reconciliationReader) ListThreadTurns(_ context.Context, p ct.ThreadTurnsListParams) (ct.ThreadTurnsListResponse, error) {
	return r.list(p)
}

type closedEventStream struct{ agent.Session }

func (closedEventStream) Events() (agent.EventStream, error) { return closedEventStream{}, nil }
func (closedEventStream) Next(context.Context) (agent.Event, error) {
	return agent.Event{}, io.EOF
}

func TestReconcileTurnQueriesOnlyOriginalThreadAndTurn(t *testing.T) {
	const threadID, originalTurn = "original-thread", "original-turn"
	source := events.NewSource()
	relay, err := agentrelay.Start(t.Context(), agentrelay.Config{
		Session: closedEventStream{}, Address: runtime.Address{Namespace: "ns", SessionName: "name"},
		ExpectedEOF: func() bool { return false },
		Sink:        func(context.Context, events.SessionEvent) error { return nil },
	})
	require.NoError(t, err)
	<-relay.Done()
	require.Error(t, relay.Err())
	original := &session{source: source, relay: relay, observedTurn: originalTurn}
	driver := &Driver{providerReady: true, sessions: map[string]*session{threadID: original}}
	completed := ct.Turn{ID: originalTurn, Status: ct.TurnStatusCompleted}
	unrelated := ct.Turn{ID: "unrelated", Status: ct.TurnStatusCompleted}
	for _, test := range []struct {
		name  string
		turns []ct.Turn
		err   error
		want  runtime.TurnReconciliation
	}{
		{"latest completed", []ct.Turn{completed, unrelated}, nil, runtime.TurnReconciliation{TurnID: originalTurn, Status: "completed", Latest: true}},
		{"older completed", []ct.Turn{{ID: "newer-active", Status: ct.TurnStatusInProgress}, completed}, nil, runtime.TurnReconciliation{TurnID: originalTurn, Status: "completed"}},
		{"failed", []ct.Turn{{ID: originalTurn, Status: ct.TurnStatusFailed}}, nil, runtime.TurnReconciliation{TurnID: originalTurn, Status: "failed", Latest: true}},
		{"interrupted", []ct.Turn{{ID: originalTurn, Status: ct.TurnStatusInterrupted}}, nil, runtime.TurnReconciliation{TurnID: originalTurn, Status: "interrupted", Latest: true}},
		{"active", []ct.Turn{unrelated, {ID: originalTurn, Status: ct.TurnStatusInProgress}}, nil, runtime.TurnReconciliation{}},
		{"missing", []ct.Turn{unrelated}, nil, runtime.TurnReconciliation{}},
		{"query failure", nil, errors.New("connection failed"), runtime.TurnReconciliation{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			driver.nativeClient = func() (codexReadClient, error) {
				return reconciliationReader{list: func(p ct.ThreadTurnsListParams) (ct.ThreadTurnsListResponse, error) {
					calls++
					require.Equal(t, threadID, p.ThreadID)
					require.Equal(t, ct.SortDirectionDesc, p.SortDirection.ValueOr(""))
					require.Equal(t, ct.TurnItemsViewSummary, p.ItemsView.ValueOr(""))
					require.NotZero(t, p.Limit.ValueOr(0))
					return ct.ThreadTurnsListResponse{Data: test.turns}, test.err
				}}, nil
			}
			got, err := driver.ReconcileTurn(t.Context(), threadID, events.NewSource())
			require.NoError(t, err)
			require.Empty(t, got)
			require.Zero(t, calls)
			got, err = driver.ReconcileTurn(t.Context(), threadID, source)
			require.ErrorIs(t, err, test.err)
			require.Equal(t, test.want, got)
			require.Equal(t, 1, calls)
		})
	}
	driver.nativeClient = func() (codexReadClient, error) {
		return reconciliationReader{list: func(ct.ThreadTurnsListParams) (ct.ThreadTurnsListResponse, error) {
			driver.sessions[threadID] = &session{source: events.NewSource()}
			return ct.ThreadTurnsListResponse{Data: []ct.Turn{completed}}, nil
		}}, nil
	}
	got, err := driver.ReconcileTurn(t.Context(), threadID, source)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestReconciliationTracksRootTurnThroughStaleAndChildEvents(t *testing.T) {
	s := &session{}
	state := events.Running
	withState := func(method, work string, child bool) {
		meta := map[string]any{"method": method}
		if child {
			meta["sourceThreadId"] = "child-thread"
		}
		source := events.NewSource().WithWork(work)
		event, err := events.NewEvent(events.EventBase{
			EventID: uuid.NewString(), Namespace: "ns", SessionName: "name", SessionID: "native-thread",
			Time: time.Now(), Meta: map[string]any{"codex": meta},
		}, events.StateUpdate{State: &state}, source)
		require.NoError(t, err)
		s.observeWork(event, work)
	}
	withState("turn/started", "older", false)
	withState("turn/started", "original", false)
	state = events.Idle
	withState("turn/completed", "older", false)
	withState("turn/started", "child", true)
	withState("turn/completed", "child", true)
	require.Equal(t, "original", s.observedTurn)
}
