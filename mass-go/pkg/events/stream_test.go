package events_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

func pipeFact(t *testing.T, id, text string) events.SessionEvent {
	t.Helper()
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: id, SessionID: id, ItemID: text, Time: time.Now()}, events.AgentMessage{MessageID: text, Content: []events.ContentBlock{events.Text(text)}}, events.NewSource())
	require.NoError(t, err)
	return e
}

func TestPipeReservesAdmissionAndServesSessionsRoundRobin(t *testing.T) {
	p := events.NewEventPipe(4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a1, a2, b1 := pipeFact(t, "hot", "one"), pipeFact(t, "hot", "two"), pipeFact(t, "peer", "one")
	require.NoError(t, p.Send(ctx, a1))
	require.NoError(t, p.Send(ctx, a2))
	blocked, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, p.Send(blocked, pipeFact(t, "hot", "not-admitted")), context.Canceled)
	// An already full hot-session allocation cannot take the peer's capacity.
	short, expire := context.WithTimeout(ctx, 20*time.Millisecond)
	defer expire()
	require.ErrorIs(t, p.Send(short, pipeFact(t, "hot", "still-not-admitted")), context.DeadlineExceeded)
	require.NoError(t, p.Send(ctx, b1))
	for _, expected := range []events.SessionEvent{a1, b1, a2} {
		got, err := p.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, expected.EventID, got.EventID)
	}
	p.End(nil)
	_, err := p.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
}

func TestPipeTotalExhaustionAndEndRetainOriginalFacts(t *testing.T) {
	p := events.NewEventPipe(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, b := pipeFact(t, "a", "one"), pipeFact(t, "b", "one")
	require.NoError(t, p.Send(ctx, a))
	require.NoError(t, p.Send(ctx, b))
	short, expire := context.WithTimeout(ctx, 20*time.Millisecond)
	defer expire()
	require.ErrorIs(t, p.Send(short, pipeFact(t, "c", "not-admitted")), context.DeadlineExceeded)
	cause := errors.New("upstream exhausted")
	p.End(cause)
	require.Error(t, p.Send(ctx, pipeFact(t, "c", "ended")))
	for _, expected := range []events.SessionEvent{a, b} {
		got, err := p.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, expected.EventID, got.EventID)
	}
	_, err := p.Next(ctx)
	require.ErrorIs(t, err, cause)
}

func TestPipeFlushWaitsForConsumerToFinishRoutingLastFact(t *testing.T) {
	p := events.NewEventPipe(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, p.Send(ctx, pipeFact(t, "a", "last")))
	_, err := p.Next(ctx)
	require.NoError(t, err)
	short, expire := context.WithTimeout(ctx, 20*time.Millisecond)
	defer expire()
	require.ErrorIs(t, p.Flush(short), context.DeadlineExceeded, "returning a fact is not proof the consumer has routed it")
	p.End(nil)
	_, err = p.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, p.Flush(ctx))
}

func TestPipeEndReleasesWaitingProducerWithoutAdmittingItsFact(t *testing.T) {
	p := events.NewEventPipe(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first := pipeFact(t, "hot", "accepted")
	require.NoError(t, p.Send(ctx, first))
	waiting := make(chan error, 1)
	pending := pipeFact(t, "hot", "not-accepted")
	go func() { waiting <- p.Send(ctx, pending) }()
	p.End(nil)
	select {
	case err := <-waiting:
		require.Error(t, err)
	case <-ctx.Done():
		t.Fatal("End did not release producer")
	}
	got, err := p.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, first.EventID, got.EventID)
	_, err = p.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
}
