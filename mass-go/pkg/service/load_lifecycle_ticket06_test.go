package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

// This peer controls the public Driver stream, not Service delivery internals.
// A page is produced only when the test explicitly admits it; native cleanup
// uncertainty is independent of the history-read terminal marker.
type ticket06FaultBinding struct {
	id, loadID string
	address    runtime.Address
	source     events.Source
	terminal   bool
	readDone   bool
	closed     bool
}

type ticket06FaultDriver struct {
	runtime.Driver
	mu             sync.Mutex
	pipe           *events.EventPipe
	bindings       map[string]*ticket06FaultBinding
	loading        chan *ticket06FaultBinding
	cleanupUnknown bool
}

func (d *ticket06FaultDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*ticket06FaultDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (d *ticket06FaultDriver) LoadSession(_ context.Context, id string, a runtime.Address, _, loadID string) error {
	b := &ticket06FaultBinding{id: id, loadID: loadID, address: a, source: events.NewSource()}
	d.mu.Lock()
	d.bindings[id] = b
	d.mu.Unlock()
	d.loading <- b
	return nil
}
func (d *ticket06FaultDriver) OpenSession(_ context.Context, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	b := &ticket06FaultBinding{id: a.SessionName, address: a, source: events.NewSource()}
	d.mu.Lock()
	d.bindings[b.id] = b
	d.mu.Unlock()
	return runtime.DriverSession{SessionID: b.id, Source: b.source}, nil
}
func (d *ticket06FaultDriver) ResumeSession(_ context.Context, id string, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	b := &ticket06FaultBinding{id: id, address: a, source: events.NewSource()}
	d.mu.Lock()
	d.bindings[id] = b
	d.mu.Unlock()
	return runtime.DriverSession{SessionID: id, Source: b.source}, nil
}
func (d *ticket06FaultDriver) emit(ctx context.Context, b *ticket06FaultBinding, item string, p events.Payload, replay bool) error {
	base := events.EventBase{EventID: uuid.NewString(), Namespace: b.address.Namespace, SessionName: b.address.SessionName, SessionID: b.id, ItemID: item, Time: time.Now().UTC()}
	if replay {
		base.Replay = &events.ReplaySource{LoadID: b.loadID, NativeRecordID: item, NativeParentID: "saved-turn"}
	}
	e, err := events.NewEvent(base, p, b.source)
	if err != nil {
		return err
	}
	return d.pipe.Send(ctx, e)
}
func (d *ticket06FaultDriver) marker(ctx context.Context, b *ticket06FaultBinding, phase string) error {
	p := events.HistoryReplay{LoadID: b.loadID, Phase: phase, RetainedScope: "all_retained_main_history"}
	if phase == "started" || phase == "native_completed" {
		p.ResumedSession = &events.ResumedSession{SessionID: b.id, Capabilities: map[string]bool{}}
	}
	if phase == "native_completed" {
		d.mu.Lock()
		b.terminal = true
		b.readDone = true
		d.mu.Unlock()
	}
	return d.emit(ctx, b, "", p, true)
}
func (d *ticket06FaultDriver) CancelHistoryLoad(ctx context.Context, id, loadID string) error {
	d.mu.Lock()
	b := d.bindings[id]
	if b == nil || b.loadID != loadID {
		d.mu.Unlock()
		return runtime.FailedPrecondition("wrong original load")
	}
	terminal := b.terminal
	b.terminal = true
	unknown := d.cleanupUnknown
	if !unknown {
		b.closed = true
	}
	d.mu.Unlock()
	if !terminal {
		if err := d.marker(ctx, b, "canceled"); err != nil {
			return err
		}
	}
	if unknown {
		return runtime.Unavailable("owned recovery release unknown")
	}
	return d.pipe.Flush(ctx)
}
func (d *ticket06FaultDriver) CloseSession(ctx context.Context, id string) error {
	d.mu.Lock()
	b := d.bindings[id]
	if b != nil && b.loadID != "" && !b.closed && !b.readDone {
		d.mu.Unlock()
		return runtime.ErrWorkUnsettled
	}
	if b != nil {
		b.closed = true
	}
	d.mu.Unlock()
	return d.pipe.Flush(ctx)
}
func (*ticket06FaultDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{}, runtime.FailedPrecondition("ordinary cancel cannot settle a history read")
}
func (d *ticket06FaultDriver) SendPrompt(ctx context.Context, id string, _ runtime.Input) (runtime.InputReceipt, error) {
	d.mu.Lock()
	b := d.bindings[id]
	closed := b == nil || b.closed
	d.mu.Unlock()
	if closed {
		return runtime.InputReceipt{}, runtime.FailedPrecondition("native connection closed")
	}
	err := d.emit(ctx, b, "live-fact", events.AgentMessage{MessageID: "live-fact", Content: []events.ContentBlock{events.Text("healthy live work")}}, false)
	return runtime.InputReceipt{Submission: "live-turn", Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, err
}
func (d *ticket06FaultDriver) Stop(context.Context) error { d.pipe.End(nil); return nil }

type ticket06FaultFactory struct{ d *ticket06FaultDriver }

func (ticket06FaultFactory) Validate(runtime.Config) error { return nil }
func (f ticket06FaultFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.d, nil
}

func ticket06FaultContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func ticket06FaultService(t *testing.T, st *file.Store, history eventstore.EventStore) (*service.Service, *ticket06FaultDriver) {
	t.Helper()
	svc, d := ticket06FaultOwner(t, st, history)
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault-peer", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ticket06FaultContext(t), "agent")
	require.NoError(t, err)
	return svc, d
}
func ticket06FaultOwner(t *testing.T, st *file.Store, history eventstore.EventStore) (*service.Service, *ticket06FaultDriver) {
	t.Helper()
	d := &ticket06FaultDriver{pipe: events.NewEventPipe(16), bindings: map[string]*ticket06FaultBinding{}, loading: make(chan *ticket06FaultBinding, 1)}
	svc := service.New(st, history, runtime.NewRegistry(map[string]runtime.Factory{"fault-peer": ticket06FaultFactory{d}}))
	t.Cleanup(func() {
		// No OS resource is created by this peer. Even a red ownership assertion
		// must end its public stream before Store teardown.
		d.mu.Lock()
		for _, b := range d.bindings {
			b.closed = true
		}
		d.cleanupUnknown = false
		d.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
		d.pipe.End(nil)
	})
	return svc, d
}

func TestTicket06CompletedLoadCancelCannotTouchLiveWork(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	receipt, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-first", events.UserMessage{MessageID: "saved-first", Content: []events.ContentBlock{events.Text("retained first page")}}, true))
	require.NoError(t, d.marker(ticket06FaultContext(t), b, "native_completed"))
	require.Eventually(t, func() bool {
		r, e := svc.GetSession(ref)
		return e == nil && r.Status.HistoryLoad.Phase == "completed" && r.Status.HistoryLoad.DeliveryCompleted
	}, 3*time.Second, 10*time.Millisecond)
	_, err = svc.SendPrompt(ticket06FaultContext(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("first live input")}})
	require.NoError(t, err)
	ticket06FaultHistory(t, svc, ref, 2)
	for _, loadID := range []string{"stale-other-load", receipt.LoadID} {
		_, err = svc.CancelHistoryLoad(ticket06FaultContext(t), ref, loadID)
		require.Error(t, err, "a stale or completed load cannot cancel the live connection")
	}
	_, err = svc.SendPrompt(ticket06FaultContext(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("second live input")}})
	require.NoError(t, err, "rejected load cancellation must leave subsequent live work usable")
	records := ticket06FaultHistory(t, svc, ref, 3)
	require.Equal(t, "healthy live work", records[2].Payload().(events.AgentMessage).Content[0].Text)
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, "completed", r.Status.HistoryLoad.Phase)
	require.True(t, r.Status.HistoryLoad.DeliveryCompleted)
}

func TestTicket06FreshServiceRetainsUnresolvedLoadFence(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	d.mu.Lock()
	d.cleanupUnknown = true
	d.mu.Unlock()
	receipt, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-first", events.UserMessage{MessageID: "saved-first", Content: []events.ContentBlock{events.Text("retained first page")}}, true))
	before := ticket06FaultHistory(t, svc, ref, 1)
	_, err = svc.CancelHistoryLoad(ticket06FaultContext(t), ref, receipt.LoadID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, e := svc.GetSession(ref)
		return e == nil && r.Status.HistoryLoad.Phase == "canceled" && !r.Status.HistoryLoad.CleanupCompleted
	}, 3*time.Second, 10*time.Millisecond)
	// Construct a new owner from durable resources, with no original in-memory
	// load lane. Unknown ownership must not be replaced by a new runtime.
	fresh, _ := ticket06FaultOwner(t, st, st)
	_, err = fresh.StartRuntime(ticket06FaultContext(t), "agent")
	require.Error(t, err, "fresh Service must retain the unresolved original runtime responsibility")
	r, err := fresh.GetSession(ref)
	require.NoError(t, err)
	require.NotNil(t, r.Status.HistoryLoad)
	require.Equal(t, receipt.LoadID, r.Status.HistoryLoad.LoadID)
	require.Equal(t, "canceled", r.Status.HistoryLoad.Phase)
	require.False(t, r.Status.HistoryLoad.CleanupCompleted)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
	after := ticket06FaultHistory(t, fresh, ref, 1)
	require.Equal(t, before[0].EventID, after[0].EventID)
	_, err = fresh.ResumeSession(ticket06FaultContext(t), ref)
	require.Error(t, err, "fresh Service must not resume an incomplete import with unresolved cleanup")
	_, err = fresh.SendPrompt(ticket06FaultContext(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must remain fenced after restart")}})
	require.Error(t, err)
	_, err = fresh.LoadSession(ticket06FaultContext(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "replacement"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: b.id})
	require.Error(t, err, "restart cannot replace the original global native ID reservation")
	r, err = fresh.GetSession(ref)
	require.NoError(t, err)
	require.NotNil(t, r.Status.HistoryLoad)
	require.Equal(t, receipt.LoadID, r.Status.HistoryLoad.LoadID)
	require.False(t, r.Status.HistoryLoad.CleanupCompleted)
}
func ticket06FaultLoad(t *testing.T, svc *service.Service, d *ticket06FaultDriver) (resource.SessionLoadAccepted, *ticket06FaultBinding) {
	t.Helper()
	receipt, err := svc.LoadSession(ticket06FaultContext(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: "native-import"})
	require.NoError(t, err)
	select {
	case b := <-d.loading:
		require.NoError(t, d.marker(ticket06FaultContext(t), b, "started"))
		return receipt, b
	case <-ticket06FaultContext(t).Done():
		t.Fatal("accepted load did not reach native paging seam")
		return receipt, nil
	}
}
func ticket06FaultHistory(t *testing.T, svc *service.Service, ref resource.SessionRef, n int) []events.SessionEvent {
	t.Helper()
	var records []events.SessionEvent
	require.Eventually(t, func() bool {
		page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
		records = page.Events
		return err == nil && len(records) == n
	}, 3*time.Second, 10*time.Millisecond)
	return records
}

func TestTicket06StopDuringPagingRetainsUnknownCleanup(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	d.mu.Lock()
	d.cleanupUnknown = true
	d.mu.Unlock()
	receipt, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-first", events.UserMessage{MessageID: "saved-first", Content: []events.ContentBlock{events.Text("retained first page")}}, true))
	ticket06FaultHistory(t, svc, ref, 1)
	_, err = svc.StopRuntime(ticket06FaultContext(t), "agent")
	require.Error(t, err, "unknown owned recovery release cannot report stopped")
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, receipt.LoadID, r.Status.HistoryLoad.LoadID)
	require.Contains(t, []string{"canceled", "failed"}, r.Status.HistoryLoad.Phase, "stop must cancel the original history read, not ordinary live work")
	require.False(t, r.Status.HistoryLoad.CleanupCompleted)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
	ticket06FaultHistory(t, svc, ref, 1)
}

func TestTicket06CancelPagingRetainsPartialHistoryAndHealthyPeer(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	receipt, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-first", events.UserMessage{MessageID: "saved-first", Content: []events.ContentBlock{events.Text("retained first page")}}, true))
	before := ticket06FaultHistory(t, svc, ref, 1)
	_, err = svc.CancelHistoryLoad(ticket06FaultContext(t), ref, receipt.LoadID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, e := svc.GetSession(ref)
		return e == nil && r.Status.HistoryLoad.Phase == "canceled" && r.Status.HistoryLoad.CleanupCompleted
	}, 3*time.Second, 10*time.Millisecond)
	after := ticket06FaultHistory(t, svc, ref, 1)
	require.Equal(t, before[0].EventID, after[0].EventID)
	require.Equal(t, "retained first page", after[0].Payload().(events.UserMessage).Content[0].Text)
	_, err = svc.SendPrompt(ticket06FaultContext(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("not a complete import")}})
	require.Error(t, err)
	_, err = svc.CreateSession(ticket06FaultContext(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "healthy"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
	require.NoError(t, err)
	peer := resource.SessionRef{SessionID: "healthy"}
	_, err = svc.SendPrompt(ticket06FaultContext(t), peer, runtime.Input{Content: []events.ContentBlock{events.Text("continue healthy peer")}})
	require.NoError(t, err)
	records := ticket06FaultHistory(t, svc, peer, 1)
	require.Equal(t, "healthy live work", records[0].Payload().(events.AgentMessage).Content[0].Text)
}

type ticket06DrainGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *ticket06DrainGate) wait() {
	g.once.Do(func() { close(g.entered) })
	<-g.release
}

type ticket06GatedHistory struct {
	eventstore.EventStore
	gate *ticket06DrainGate
}

func (h *ticket06GatedHistory) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == "native-import" && len(records) > 0 && records[0].ItemID == "saved-second" {
		h.gate.wait()
	}
	return h.EventStore.Append(id, records)
}

type ticket06GatedPublication struct{ gate *ticket06DrainGate }

func (*ticket06GatedPublication) Required() bool { return true }
func (*ticket06GatedPublication) Close() error   { return nil }
func (h *ticket06GatedPublication) Handle(e events.SessionEvent) error {
	if e.ItemID == "saved-second" {
		h.gate.wait()
		return errors.New("required publication unavailable after partial delivery")
	}
	return nil
}

func TestTicket06CancelDuringLocalDrainPreservesAdmittedHistory(t *testing.T) {
	for _, boundary := range []string{"append", "required-publish"} {
		t.Run(boundary, func(t *testing.T) {
			st, err := file.Open(t.TempDir(), false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			gate := &ticket06DrainGate{entered: make(chan struct{}), release: make(chan struct{})}
			var history eventstore.EventStore = st
			if boundary == "append" {
				history = &ticket06GatedHistory{EventStore: st, gate: gate}
			}
			svc, d := ticket06FaultService(t, st, history)
			// Release before Service cleanup, including on a failed assertion.
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			t.Cleanup(release)
			receipt, b := ticket06FaultLoad(t, svc, d)
			ref := resource.SessionRef{SessionID: b.id}
			if boundary == "required-publish" {
				broadcast, e := svc.EventBroadcast(ref)
				require.NoError(t, e)
				require.NoError(t, broadcast.AddHandler(&ticket06GatedPublication{gate}))
			}
			require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-first", events.UserMessage{MessageID: "saved-first", Content: []events.ContentBlock{events.Text("first saved input")}}, true))
			first := ticket06FaultHistory(t, svc, ref, 1)
			require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved-second", events.AgentMessage{MessageID: "saved-second", Content: []events.ContentBlock{events.Text("second admitted fact")}}, true))
			select {
			case <-gate.entered:
			case <-ticket06FaultContext(t).Done():
				t.Fatal("local settlement did not reach controlled fault boundary")
			}
			require.NoError(t, d.marker(ticket06FaultContext(t), b, "native_completed"))
			require.NoError(t, d.pipe.Flush(ticket06FaultContext(t)), "native read terminal has reached Service, not local completion")
			r, err := svc.GetSession(ref)
			require.NoError(t, err)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			_, err = svc.CancelHistoryLoad(ticket06FaultContext(t), ref, receipt.LoadID)
			require.NoError(t, err, "cancel remains valid after native completion but before local settlement")
			_, err = svc.SendPrompt(ticket06FaultContext(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must remain fenced")}})
			require.Error(t, err)
			release()
			require.Eventually(t, func() bool {
				r, e := svc.GetSession(ref)
				return e == nil && r.Status.HistoryLoad.CleanupCompleted
			}, 3*time.Second, 10*time.Millisecond)
			records := ticket06FaultHistory(t, svc, ref, 2)
			require.Equal(t, first[0].EventID, records[0].EventID)
			require.Equal(t, "second admitted fact", records[1].Payload().(events.AgentMessage).Content[0].Text)
			require.Equal(t, receipt.LoadID, records[1].Replay.LoadID)
			r, err = svc.GetSession(ref)
			require.NoError(t, err)
			require.Equal(t, "canceled", r.Status.HistoryLoad.Phase)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			require.NotNil(t, r.Status.HistoryLoad.Error)
			require.Equal(t, "history_load_canceled", r.Status.HistoryLoad.Error.Reason, "later required-publish error must not replace the original cancellation")
		})
	}
}
