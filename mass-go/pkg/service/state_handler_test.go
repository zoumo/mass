package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
)

type gatedStateMeta struct {
	metastore.MetaStore
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
	reads   chan resource.Session
}

func (m *gatedStateMeta) GetSession(ref resource.SessionRef) (resource.Session, error) {
	r, err := m.MetaStore.GetSession(ref)
	if err == nil && m.reads != nil {
		m.reads <- r
	}
	return r, err
}

func (m *gatedStateMeta) UpdateSession(r resource.Session) (resource.Session, error) {
	if r.Status.State == events.Running && m.armed.CompareAndSwap(true, false) {
		close(m.entered)
		<-m.release
	}
	return m.MetaStore.UpdateSession(r)
}

func TestStateWritesDoNotBlockRealtimeOrOverwriteLifecycle(t *testing.T) {
	for _, kind := range []string{"sqlite", "file"} {
		t.Run(kind, func(t *testing.T) {
			meta, err := backend.Open(t.TempDir(), metastore.Config{Backend: kind}, false)
			require.NoError(t, err)
			defer meta.Close()
			history, err := eventfile.Open(t.TempDir(), false)
			require.NoError(t, err)
			defer history.Close()
			gate := &gatedStateMeta{MetaStore: meta, entered: make(chan struct{}), release: make(chan struct{})}
			var released atomic.Bool
			release := func() {
				if released.CompareAndSwap(false, true) {
					close(gate.release)
				}
			}
			defer release()
			driver := &workDriver{pipe: events.NewEventPipe(16), entered: make(chan context.Context, 1), release: make(chan struct{})}
			svc := service.New(gate, history, runtime.NewRegistry(map[string]runtime.Factory{"fixture": workFactory{driver}}))
			defer func() { release(); require.NoError(t, svc.Shutdown(context.Background())) }()
			_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
			require.NoError(t, err)
			_, err = svc.StartRuntime(context.Background(), "agent")
			require.NoError(t, err)
			r, err := svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
			require.NoError(t, err)
			ref := resource.SessionRef{SessionID: r.Status.SessionID}
			sub, err := svc.SubscribeEvents(ref)
			require.NoError(t, err)
			defer sub.Close()
			state := events.Running
			e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: r.Status.SessionID, Time: time.Now()}, events.StateUpdate{State: &state}, driver.source)
			require.NoError(t, err)
			gate.armed.Store(true)
			require.NoError(t, driver.pipe.Send(context.Background(), e))
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("state write not started")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := nextSubscription(sub, ctx)
			require.NoError(t, err, "slow metadata must not block notification")
			require.Equal(t, e.EventID, got.EventID)
			b, err := svc.EventBroadcast(ref)
			require.NoError(t, err)
			slow, err := b.Subscribe()
			require.NoError(t, err)
			// Saturating both independent bounded queues cannot block a healthy
			// observer. State overflow is diagnostic, not a delivery barrier.
			for range 140 {
				require.NoError(t, b.Publish(e))
				_, err := nextSubscription(sub, ctx)
				require.NoError(t, err)
			}
			require.ErrorIs(t, slow.Err(), events.ErrSlowSubscriber)
			// A real conditional write races the queued stale snapshot.
			current, err := meta.GetSession(ref)
			require.NoError(t, err)
			current.Spec.Cwd = "/concurrent"
			current.Status.State = "closing"
			saved, err := meta.UpdateSession(current)
			require.NoError(t, err)
			// A blocked write cannot be claimed released. Other owned handlers
			// still close, and the failed worker remains eligible for cleanup retry.
			require.Error(t, b.Close())
			_, open := <-sub.Events()
			require.False(t, open)
			release()
			require.NoError(t, b.Close())
			current, err = meta.GetSession(ref)
			require.NoError(t, err)
			require.Equal(t, saved, current, "shutdown must preserve closing/spec")
		})
	}
}

// The first real write either fails against a read-only authority or commits
// before returning an uncertain outcome. Both must leave subsequent work live.
type failingStateMeta struct {
	metastore.MetaStore
	readonly  metastore.MetaStore
	uncertain bool
	attempts  atomic.Int32
	completed chan events.SessionState
}

func (m *failingStateMeta) UpdateSession(r resource.Session) (resource.Session, error) {
	if r.Status.State == events.Running {
		m.attempts.Add(1)
		if !m.uncertain {
			return m.readonly.UpdateSession(r)
		}
		saved, err := m.MetaStore.UpdateSession(r)
		if err != nil {
			return saved, err
		}
		return saved, errors.New("outcome_unknown: injected after real commit")
	}
	saved, err := m.MetaStore.UpdateSession(r)
	if err == nil {
		m.completed <- r.Status.State
	}
	return saved, err
}

func TestFailedStateObservationContinuesWithoutReplayOrControl(t *testing.T) {
	for _, kind := range []string{"sqlite", "file"} {
		for _, uncertain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/uncertain=%v", kind, uncertain), func(t *testing.T) {
				dir := t.TempDir()
				meta, err := backend.Open(dir, metastore.Config{Backend: kind}, false)
				require.NoError(t, err)
				defer meta.Close()
				r, err := meta.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Status: resource.SessionStatus{SessionID: "original", State: events.Unknown, AppliedGeneration: 1}})
				require.NoError(t, err)
				ro, err := backend.Open(dir, metastore.Config{Backend: kind}, true)
				require.NoError(t, err)
				defer ro.Close()
				history, err := eventfile.Open(t.TempDir(), false)
				require.NoError(t, err)
				defer history.Close()
				observed := &failingStateMeta{MetaStore: meta, readonly: ro, uncertain: uncertain, completed: make(chan events.SessionState, 4)}
				svc := service.New(observed, history, runtime.NewRegistry(nil))
				defer func() { require.NoError(t, svc.Shutdown(context.Background())) }()
				ref := resource.SessionRef{SessionID: r.Status.SessionID}
				b, err := svc.EventBroadcast(ref)
				require.NoError(t, err)
				sub, err := b.Subscribe()
				require.NoError(t, err)
				for _, state := range []events.SessionState{events.Running, events.Idle} {
					e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", Time: time.Now()}, events.StateUpdate{State: &state}, events.Source{})
					require.NoError(t, err)
					require.NoError(t, b.Publish(e))
					require.Equal(t, state, *(<-sub.Events()).Payload().(events.StateUpdate).State)
				}
				select {
				case state := <-observed.completed:
					require.Equal(t, events.Idle, state)
				case <-time.After(time.Second):
					t.Fatal("failed observation stopped later state write")
				}
				require.EqualValues(t, 1, observed.attempts.Load(), "uncertain/failed writes must not be replayed")
				current, err := svc.GetSession(ref)
				require.NoError(t, err)
				require.Equal(t, events.Idle, current.Status.State)
				_, err = svc.SendPrompt(context.Background(), ref, runtime.Input{Content: []events.ContentBlock{events.Text("no runtime control")}})
				require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
			})
		}
	}
}

func TestActiveStateConflictRechecksLatestIdentityVersionAndTerminalState(t *testing.T) {
	for _, kind := range []string{"sqlite", "file"} {
		for _, change := range []string{"spec", "closing", "offline", "identity"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				meta, err := backend.Open(t.TempDir(), metastore.Config{Backend: kind}, false)
				require.NoError(t, err)
				defer meta.Close()
				r, err := meta.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Status: resource.SessionStatus{SessionID: "original", State: events.Unknown, AppliedGeneration: 1}})
				require.NoError(t, err)
				gate := &gatedStateMeta{MetaStore: meta, entered: make(chan struct{}), release: make(chan struct{}), reads: make(chan resource.Session, 16)}
				history, err := eventfile.Open(t.TempDir(), false)
				require.NoError(t, err)
				defer history.Close()
				svc := service.New(gate, history, runtime.NewRegistry(nil))
				var released atomic.Bool
				release := func() {
					if released.CompareAndSwap(false, true) {
						close(gate.release)
					}
				}
				defer func() { release(); require.NoError(t, svc.Shutdown(context.Background())) }()
				ref := resource.SessionRef{SessionID: r.Status.SessionID}
				b, err := svc.EventBroadcast(ref)
				require.NoError(t, err)
				<-gate.reads // Broadcast identity resolution.
				gate.armed.Store(true)
				state := events.Running
				e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", Time: time.Now()}, events.StateUpdate{State: &state}, events.Source{})
				require.NoError(t, err)
				require.NoError(t, b.Publish(e))
				select {
				case <-gate.entered:
				case <-time.After(time.Second):
					t.Fatal("state write not entered")
				}
				<-gate.reads // Original stale snapshot.
				if change == "identity" {
					require.NoError(t, meta.DeleteSession(ref, r.Metadata.ResourceVersion))
					r.Metadata = resource.Metadata{Namespace: "one", Name: "demo"}
					r, err = meta.CreateSession(r)
				} else {
					r.Spec.Cwd = "/new-spec"
					if change != "spec" {
						r.Status.State = events.SessionState(change)
					}
					r, err = meta.UpdateSession(r)
				}
				require.NoError(t, err)
				release()
				// Observe the actual re-read while the Handler remains open.
				select {
				case reread := <-gate.reads:
					require.Equal(t, r.Metadata.ResourceVersion, reread.Metadata.ResourceVersion)
				case <-time.After(time.Second):
					t.Fatal("conditional conflict did not re-read")
				}
				if change == "spec" {
					require.Eventually(t, func() bool {
						current, err := meta.GetSession(ref)
						return err == nil && current.Status.State == events.Running
					}, time.Second, time.Millisecond)
				}
				require.NoError(t, b.Close())
				current, err := meta.GetSession(ref)
				require.NoError(t, err)
				if change == "spec" {
					r.Status.State = events.Running
					r.Metadata.ResourceVersion = current.Metadata.ResourceVersion
				}
				require.Equal(t, r, current)
			})
		}
	}
}
