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
func (d *ticket06FaultDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("incomplete import must not be resumed")
}
func (d *ticket06FaultDriver) emit(ctx context.Context, b *ticket06FaultBinding, item string, p events.Payload, replay bool) error {
	base := events.EventBase{EventID: uuid.NewString(), Namespace: b.address.Namespace, SessionName: b.address.SessionName, SessionID: b.id, ItemID: item, Time: time.Now().UTC()}
	if replay {
		base.Replay = &events.ReplaySource{LoadID: b.loadID, NativeRecordID: item}
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
	if b != nil && b.loadID != "" && !b.closed {
		d.mu.Unlock()
		return runtime.ErrWorkUnsettled
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
func ticket06FaultService(t *testing.T, st *file.Store) (*service.Service, *ticket06FaultDriver) {
	t.Helper()
	d := &ticket06FaultDriver{pipe: events.NewEventPipe(16), bindings: map[string]*ticket06FaultBinding{}, loading: make(chan *ticket06FaultBinding, 1)}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fault-peer": ticket06FaultFactory{d}}))
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault-peer", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ticket06FaultContext(t), "agent")
	require.NoError(t, err)
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
	svc, d := ticket06FaultService(t, st)
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
