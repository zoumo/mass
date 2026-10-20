package events_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

type broadcastHandler struct {
	required bool
	handle   func(events.SessionEvent) error
	close    func() error
}

func (h *broadcastHandler) Required() bool                     { return h.required }
func (h *broadcastHandler) Handle(e events.SessionEvent) error { return h.handle(e) }
func (h *broadcastHandler) Close() error {
	if h.close != nil {
		return h.close()
	}
	return nil
}

func broadcastEvent(t *testing.T) events.SessionEvent {
	t.Helper()
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "ns", SessionName: "name", SessionID: "id", Time: time.Now(), Meta: map[string]any{"value": "original"}}, events.SessionError{Message: "example"}, events.Source{})
	require.NoError(t, err)
	return e
}

func TestBroadcastInsertionOrderAndPartialDelivery(t *testing.T) {
	b, err := events.NewBroadcast("ns", "name", "id", 2)
	require.NoError(t, err)
	defer b.Close()
	sub, err := b.Subscribe()
	require.NoError(t, err)
	failure := errors.New("required failed")
	require.NoError(t, b.AddHandler(&broadcastHandler{required: true, handle: func(events.SessionEvent) error { return failure }}))
	later := 0
	require.NoError(t, b.AddHandler(&broadcastHandler{handle: func(events.SessionEvent) error { later++; return nil }}))
	e := broadcastEvent(t)
	require.ErrorIs(t, b.Publish(e), failure)
	require.Equal(t, e.EventID, (<-sub.Events()).EventID)
	require.Zero(t, later)
	require.ErrorIs(t, b.Publish(broadcastEvent(t)), failure)
	require.Len(t, sub.Events(), 1) // only the second publication, no automatic retry
}

func TestSubscriptionTerminationPreservesBufferAndReason(t *testing.T) {
	b, err := events.NewBroadcast("ns", "name", "id", 1)
	require.NoError(t, err)
	defer b.Close()
	s, err := b.Subscribe()
	require.NoError(t, err)
	channel := s.Events()
	require.Equal(t, channel, s.Events())
	e := broadcastEvent(t)
	require.NoError(t, b.Publish(e))
	require.NoError(t, b.Publish(broadcastEvent(t)))
	require.ErrorIs(t, s.Err(), events.ErrSlowSubscriber)
	require.NoError(t, s.Close())
	require.Equal(t, e.EventID, (<-s.Events()).EventID)
	_, ok := <-s.Events()
	require.False(t, ok)
	require.ErrorIs(t, s.Err(), events.ErrSlowSubscriber)
}

func TestBroadcastOrdinaryFailureMutationAndOwnership(t *testing.T) {
	b, err := events.NewBroadcast("ns", "name", "id", 4)
	require.NoError(t, err)
	other, err := events.NewBroadcast("ns", "name", "id", 1)
	require.NoError(t, err)
	defer other.Close()
	closed := 0
	h := &broadcastHandler{handle: func(e events.SessionEvent) error { e.Meta["value"] = "changed"; return errors.New("ordinary failure") }, close: func() error { closed++; return nil }}
	require.NoError(t, b.AddHandler(h))
	require.Error(t, b.AddHandler(h))
	require.Error(t, other.AddHandler(h))
	sub, err := b.Subscribe()
	require.NoError(t, err)
	e := broadcastEvent(t)
	require.NoError(t, b.Publish(e))
	got := <-sub.Events()
	require.Equal(t, "original", got.Meta["value"])
	require.Equal(t, "original", e.Meta["value"])
	wrong := e
	wrong.SessionName = "other"
	require.Error(t, b.Publish(wrong))
	require.NoError(t, b.Close())
	require.NoError(t, b.Close())
	require.Equal(t, 1, closed)
	require.Error(t, b.Publish(e))
	_, err = b.Subscribe()
	require.Error(t, err)
	_, ok := <-sub.Events()
	require.False(t, ok)
	require.NoError(t, sub.Err())
}

func TestBroadcastCancelFromHandlerAndWaitingReceiver(t *testing.T) {
	b, err := events.NewBroadcast("ns", "name", "id", 2)
	require.NoError(t, err)
	defer b.Close()
	sub, err := b.Subscribe()
	require.NoError(t, err)
	require.NoError(t, b.AddHandler(&broadcastHandler{handle: func(events.SessionEvent) error { return sub.Close() }}))
	result := make(chan error, 1)
	e := broadcastEvent(t)
	go func() { result <- b.Publish(e) }()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("self cancellation deadlocked")
	}
	require.Len(t, sub.Events(), 1)
	<-sub.Events()
	_, ok := <-sub.Events()
	require.False(t, ok)
	waiting, err := b.Subscribe()
	require.NoError(t, err)
	awake := make(chan bool, 1)
	go func() { _, ok := <-waiting.Events(); awake <- ok }()
	require.NoError(t, waiting.Close())
	select {
	case ok := <-awake:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("receiver did not wake")
	}
	healthy, err := b.Subscribe()
	require.NoError(t, err)
	require.NoError(t, b.Publish(broadcastEvent(t)))
	require.Len(t, healthy.Events(), 1)
}

func TestBroadcastConcurrentPublishSubscribeCancelClose(t *testing.T) {
	for range 20 {
		b, err := events.NewBroadcast("ns", "name", "id", 32)
		require.NoError(t, err)
		var active, maxActive atomic.Int32
		require.NoError(t, b.AddHandler(&broadcastHandler{required: true, handle: func(events.SessionEvent) error {
			n := active.Add(1)
			if n > maxActive.Load() {
				maxActive.Store(n)
			}
			active.Add(-1)
			return nil
		}}))
		e := broadcastEvent(t)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 10 {
					_ = b.Publish(e)
				}
			})
		}
		for range 8 {
			wg.Go(func() {
				for range 10 {
					s, err := b.Subscribe()
					if err == nil {
						_ = s.Close()
					}
				}
			})
		}
		wg.Go(func() { _ = b.Close() })
		wg.Wait()
		require.LessOrEqual(t, maxActive.Load(), int32(1))
		require.NoError(t, b.Close())
	}
}

func TestBroadcastRequiredPanicAndCloseFailureRetainCleanup(t *testing.T) {
	b, err := events.NewBroadcast("ns", "name", "id", 1)
	require.NoError(t, err)
	failed := true
	h := &broadcastHandler{required: true, handle: func(events.SessionEvent) error { panic("failure") }, close: func() error {
		if failed {
			return errors.New("cleanup failed")
		}
		return nil
	}}
	require.NoError(t, b.AddHandler(h))
	sub, err := b.Subscribe()
	require.NoError(t, err)
	require.ErrorContains(t, b.Publish(broadcastEvent(t)), "handler panic")
	require.Empty(t, sub.Events())
	require.ErrorContains(t, b.Close(), "cleanup failed")
	_, ok := <-sub.Events()
	require.False(t, ok)
	failed = false
	require.NoError(t, b.Close())
}

func TestSubscriptionOverflowRacingCancellation(t *testing.T) {
	for range 100 {
		b, err := events.NewBroadcast("ns", "name", "id", 1)
		require.NoError(t, err)
		sub, err := b.Subscribe()
		require.NoError(t, err)
		first := broadcastEvent(t)
		require.NoError(t, b.Publish(first))
		second := broadcastEvent(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; _ = b.Publish(second) })
		wg.Go(func() { <-start; _ = sub.Close() })
		close(start)
		wg.Wait()
		cause := sub.Err()
		require.True(t, cause == nil || errors.Is(cause, events.ErrSlowSubscriber))
		require.Equal(t, first.EventID, (<-sub.Events()).EventID)
		_, ok := <-sub.Events()
		require.False(t, ok)
		require.NoError(t, sub.Close())
		require.Equal(t, cause, sub.Err())
		require.NoError(t, b.Close())
	}
	// When cancellation is ordered after the queue Handler, it must retain overflow.
	b, err := events.NewBroadcast("ns", "name", "id", 1)
	require.NoError(t, err)
	defer b.Close()
	sub, err := b.Subscribe()
	require.NoError(t, err)
	require.NoError(t, b.Publish(broadcastEvent(t)))
	require.NoError(t, b.AddHandler(&broadcastHandler{handle: func(events.SessionEvent) error { return sub.Close() }}))
	require.NoError(t, b.Publish(broadcastEvent(t)))
	require.ErrorIs(t, sub.Err(), events.ErrSlowSubscriber)
}
