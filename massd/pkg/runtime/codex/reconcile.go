package codex

import (
	"context"

	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

func (d *Driver) ReconcileTurn(ctx context.Context, id string, source events.Source) (runtime.TurnReconciliation, error) {
	s, turnID := d.observationTurn(id, source)
	if turnID == "" {
		return runtime.TurnReconciliation{}, nil
	}
	reader, err := d.reader()
	if err != nil {
		return runtime.TurnReconciliation{}, err
	}
	// Recent turns cover the ordinary case and bound work on shared Threads
	// with conflicting activity. Absence from this page proves nothing.
	turns, err := reader.ListThreadTurns(ctx, ct.ThreadTurnsListParams{
		ThreadID: id, SortDirection: ct.Some(ct.SortDirectionDesc),
		ItemsView: ct.Some(ct.TurnItemsViewSummary), Limit: ct.Some(uint32(10)),
	})
	if err != nil {
		return runtime.TurnReconciliation{}, err
	}
	if current, work := d.observationTurn(id, source); current != s || work != turnID {
		return runtime.TurnReconciliation{}, nil
	}
	for index, turn := range turns.Data {
		if turn.ID != turnID {
			continue
		}
		switch turn.Status {
		case ct.TurnStatusCompleted, ct.TurnStatusFailed, ct.TurnStatusInterrupted:
			return runtime.TurnReconciliation{TurnID: turnID, Status: string(turn.Status), Latest: index == 0}, nil
		}
		break
	}
	return runtime.TurnReconciliation{}, nil
}

func (d *Driver) observationTurn(id string, source events.Source) (*session, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil || !s.source.SameRun(source) || s.closing.Load() ||
		d.stopped || d.stopping || s.relay == nil || s.relay.Err() == nil {
		return nil, ""
	}
	select {
	case <-s.relay.Done():
	default:
		return nil, ""
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s, s.observedTurn
}

var _ runtime.TurnReconciler = (*Driver)(nil)
