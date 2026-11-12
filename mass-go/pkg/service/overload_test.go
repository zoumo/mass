package service_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type overloadDriver struct {
	runtime.Driver
	pipe               *events.EventPipe
	source, peerSource events.Source
	sent               []events.SessionEvent
	count              int
	closed             atomic.Int32
	unknown            atomic.Bool
}

func TestOverloadTotalRetentionExhaustionRetainsOriginalDelivery(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	defer st.Close()
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	d := &overloadDriver{pipe: events.NewEventPipe(128), source: events.NewSource(), peerSource: events.NewSource(), count: 320}
	d.unknown.Store(true)
	svc := service.New(st, &overloadStore{EventStore: st, gate: gate}, runtime.NewRegistry(map[string]runtime.Factory{"fixture": overloadFactory{d}}))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	defer func() { release.Do(func() { close(gate) }); d.unknown.Store(false); _ = svc.Shutdown(ctx) }()
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	for _, id := range []string{"slow", "fast"} {
		_, err = svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: id}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
		require.NoError(t, err)
	}
	slow := resource.SessionRef{SessionID: "slow"}
	fast := resource.SessionRef{SessionID: "fast"}
	input := runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}
	_, err = svc.SendPrompt(ctx, slow, input)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		defer stop()
		return errors.Is(svc.ReconcileDelivery(short, "agent"), context.DeadlineExceeded)
	}, time.Second, 10*time.Millisecond, "finite closing reserve must retain an overflow rather than silently drop or grow")
	_, err = svc.SendPrompt(ctx, fast, input)
	require.Error(t, err, "total exhaustion cannot promise healthy-peer progress")
	release.Do(func() { close(gate) })
	require.Eventually(t, func() bool {
		short, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop()
		_ = svc.ReconcileDelivery(short, "agent")
		page, e := svc.ListHistoryEvents(slow, store.HistoryQuery{Limit: 400})
		return e == nil && len(page.Events) == 320
	}, 25*time.Second, 50*time.Millisecond)
	page, err := svc.ListHistoryEvents(slow, store.HistoryQuery{Limit: 400})
	require.NoError(t, err)
	for i, e := range page.Events {
		require.Equal(t, d.sent[i].EventID, e.EventID)
		require.Equal(t, fmt.Sprintf("fact-%03d", i), e.ItemID)
	}
	d.unknown.Store(false)
	_, err = svc.CloseSession(ctx, slow)
	require.NoError(t, err)
	require.NoError(t, svc.Shutdown(ctx))
}
func (d *overloadDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *overloadDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (d *overloadDriver) OpenSession(ctx context.Context, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	source := d.source
	if a.SessionName == "fast" {
		source = d.peerSource
	}
	idle := events.Idle
	e, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   a.Namespace,
		SessionName: a.SessionName,
		SessionID:   a.SessionName,
		Time:        time.Now().UTC(),
	}, events.StateUpdate{State: &idle}, source)
	if err != nil {
		return runtime.DriverSession{}, err
	}
	if err := d.pipe.Send(ctx, e); err != nil {
		return runtime.DriverSession{}, err
	}
	return runtime.DriverSession{SessionID: a.SessionName, Source: source}, nil
}

func (d *overloadDriver) CloseSession(ctx context.Context, id string) error {
	if id == "slow" {
		d.closed.Add(1)
		if d.unknown.Load() {
			return runtime.Unavailable("release unknown")
		}
	}
	return d.pipe.Flush(ctx)
}

func (*overloadDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}
func (d *overloadDriver) Stop(context.Context) error { d.pipe.End(nil); return nil }
func (d *overloadDriver) SendPrompt(ctx context.Context, id string, _ runtime.Input) (runtime.InputReceipt, error) {
	n := 1
	if id == "slow" {
		n = 160
		if d.count > 0 {
			n = d.count
		}
	}
	source := d.source
	if id == "fast" {
		source = d.peerSource
	}
	for i := range n {
		text := fmt.Sprintf("fact-%03d", i)
		e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: id, SessionID: id, ItemID: text, Time: time.Now()}, events.AgentMessage{MessageID: text, Content: []events.ContentBlock{events.Text(text)}}, source)
		if err != nil {
			return runtime.InputReceipt{}, err
		}
		if id == "slow" {
			d.sent = append(d.sent, e)
		}
		if err := d.pipe.Send(ctx, e); err != nil {
			return runtime.InputReceipt{}, err
		}
	}
	return runtime.InputReceipt{Submission: "accepted", Result: agent.InputAppliedNewWork, Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, nil
}

type overloadFactory struct{ d *overloadDriver }

func (f overloadFactory) Validate(runtime.Config) error                      { return nil }
func (f overloadFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.d, nil }

type overloadStore struct {
	eventstore.EventStore
	gate        chan struct{}
	once        sync.Once
	unavailable atomic.Bool
	failed      chan struct{}
	failOnce    sync.Once
}

func (g *overloadStore) Append(id string, e []events.SessionEvent) (*string, error) {
	if id == "slow" {
		g.once.Do(func() { <-g.gate })
		if g.unavailable.Load() {
			g.failOnce.Do(func() { close(g.failed) })
			return nil, errors.New("persistent storage unavailable")
		}
	}
	return g.EventStore.Append(id, e)
}

func TestOverloadIsolatesPeerBeforeStorageRelease(t *testing.T) {
	for _, mode := range []string{"storage", "required", "unavailable"} {
		required := mode == "required"
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, "/releaseUnknown=", unknown), func(t *testing.T) {
				st, err := file.Open(t.TempDir(), false)
				require.NoError(t, err)
				defer st.Close()
				gate := make(chan struct{})
				var release sync.Once
				defer release.Do(func() { close(gate) })
				d := &overloadDriver{pipe: events.NewEventPipe(128), source: events.NewSource(), peerSource: events.NewSource()}
				d.unknown.Store(unknown)
				var history eventstore.EventStore = st
				faultStore := &overloadStore{EventStore: st, gate: gate, failed: make(chan struct{})}
				faultStore.unavailable.Store(mode == "unavailable")
				if !required {
					history = faultStore
				}
				svc := service.New(st, history, runtime.NewRegistry(map[string]runtime.Factory{"fixture": overloadFactory{d}}))
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				defer func() {
					release.Do(func() { close(gate) })
					d.unknown.Store(false)
					settle, stop := context.WithTimeout(context.Background(), time.Second)
					defer stop()
					_ = svc.ReconcileDelivery(settle, "agent")
					_ = svc.Shutdown(settle)
				}()
				_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
				require.NoError(t, err)
				_, err = svc.StartRuntime(ctx, "agent")
				require.NoError(t, err)
				for _, id := range []string{"slow", "fast"} {
					_, err = svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: id}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
					require.NoError(t, err)
				}
				slow := resource.SessionRef{SessionID: "slow"}
				fast := resource.SessionRef{SessionID: "fast"}
				b, e := svc.EventBroadcast(slow)
				require.NoError(t, e)
				var delivered atomic.Int32
				var diagnosed atomic.Bool
				require.NoError(t, b.AddHandler(&observingHandler{handle: func(e events.SessionEvent) error {
					if e.Persistent() {
						delivered.Add(1)
					}
					if failure, ok := e.Payload().(events.SessionError); ok && failure.Reason == "session_delivery_overloaded" {
						diagnosed.Store(true)
					}
					return nil
				}}))
				if required {
					var once sync.Once
					require.NoError(t, b.AddHandler(&observingHandler{required: true, handle: func(e events.SessionEvent) error {
						if e.Persistent() {
							once.Do(func() { <-gate })
							return errors.New("required failure after partial publish")
						}
						return nil
					}}))
				}
				h, e := transport.New(svc, transport.Options{Token: "fixture"})
				require.NoError(t, e)
				defer h.Shutdown()
				server := httptest.NewServer(h)
				defer server.Close()
				c := client.New("ws"+strings.TrimPrefix(server.URL, "http")+"/rpc", "fixture")
				defer c.Close()
				input := runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}
				err = c.SendPrompt(ctx, slow, input, nil)
				require.NoError(t, err)
				require.Eventually(t, func() bool { r, e := svc.GetSession(slow); return e == nil && r.Status.State == "closing" }, time.Second, time.Millisecond, "saturation must fail only the slow Session")
				err = c.SendPrompt(ctx, slow, input, nil)
				var public *runtime.OperationError
				require.ErrorAs(t, err, &public)
				switch public.Code {
				case "resource_exhausted":
					require.Contains(t, err.Error(), "session_delivery_overloaded")
				case "conflict":
					require.Equal(t, "session_resume_required", public.Reason)
				default:
					require.FailNow(t, "unexpected overloaded Session admission error", "%v", err)
				}
				require.Eventually(t, func() bool { return d.closed.Load() > 0 }, time.Second, time.Millisecond, "owned native close starts without waiting for blocked storage")
				err = c.SendPrompt(ctx, fast, input, nil)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					p, e := svc.ListHistoryEvents(fast, store.HistoryQuery{})
					return e == nil && len(p.Events) == 1
				}, time.Second, time.Millisecond, "healthy peer completes AFTER saturation and BEFORE gate release")
				release.Do(func() { close(gate) })
				if mode == "unavailable" {
					select {
					case <-faultStore.failed:
					case <-ctx.Done():
						t.Fatal("missing storage failure")
					}
					page, e := svc.ListHistoryEvents(slow, store.HistoryQuery{})
					require.NoError(t, e)
					require.Empty(t, page.Events, "persistent fault is not successful drain")
					short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
					_, e = svc.CloseSession(short, slow)
					stop()
					require.Error(t, e, "unknown release or persistent commit must stay unsettled")
					r, e := svc.GetSession(slow)
					require.NoError(t, e)
					require.EqualValues(t, "closing", r.Status.State)
					faultStore.unavailable.Store(false)
					require.Eventually(t, func() bool { return svc.ReconcileHistory(ctx, slow) == nil }, time.Second, 10*time.Millisecond)
				}
				require.Eventually(t, func() bool {
					p, e := svc.ListHistoryEvents(slow, store.HistoryQuery{Limit: 200})
					return e == nil && len(p.Events) == 160
				}, 20*time.Second, 50*time.Millisecond)
				page, err := svc.ListHistoryEvents(slow, store.HistoryQuery{Limit: 200})
				require.NoError(t, err)
				for i, e := range page.Events {
					require.Equal(t, fmt.Sprintf("fact-%03d", i), e.ItemID)
					require.Equal(t, d.sent[i].EventID, e.EventID)
					require.Equal(t, d.sent[i].Payload(), e.Payload())
				}
				if unknown {
					r, e := svc.GetSession(slow)
					require.NoError(t, e)
					require.EqualValues(t, "closing", r.Status.State)
					d.unknown.Store(false)
				}
				_, err = svc.CloseSession(ctx, slow)
				if required {
					require.ErrorContains(t, err, "session_close_failed")
				} else {
					require.NoError(t, err)
				}
				require.EqualValues(t, 160, delivered.Load(), "partial required publication never replays already committed facts")
				require.True(t, diagnosed.Load(), "the local overload failure is published after storage settles")
				if !unknown {
					require.EqualValues(t, 1, d.closed.Load(), "confirmed native release is not repeated during settlement")
				}
				require.NoError(t, svc.Shutdown(ctx))
			})
		}
	}
}
