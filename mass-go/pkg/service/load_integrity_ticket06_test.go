package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type ticket06FailingPublication struct {
	mu   sync.Mutex
	seen []string
}

func (*ticket06FailingPublication) Required() bool { return true }
func (*ticket06FailingPublication) Close() error   { return nil }
func (h *ticket06FailingPublication) Handle(e events.SessionEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, e.ItemID)
	if e.ItemID == "failed" {
		return errors.New("required delivery failed")
	}
	return nil
}

func TestTicket06FailedPublicationCannotBeOvertaken(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	_, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.NativeResumed }, time.Second, time.Millisecond)
	broadcast, err := svc.EventBroadcast(ref)
	require.NoError(t, err)
	handler := &ticket06FailingPublication{}
	require.NoError(t, broadcast.AddHandler(handler))
	for _, id := range []string{"failed", "later"} {
		require.NoError(t, d.emit(ticket06FaultContext(t), b, id, events.AgentMessage{MessageID: id, Content: []events.ContentBlock{events.Text(id)}}, true))
	}
	ticket06FaultHistory(t, svc, ref, 2)
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.CleanupCompleted }, time.Second, time.Millisecond)
	handler.mu.Lock()
	defer handler.mu.Unlock()
	require.Equal(t, []string{"failed"}, handler.seen, "later facts stay durable but cannot overtake a failed required publish")
}

func TestTicket06UnexpectedDriverEOFIsNotHistoryCompletion(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, st)
	_, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved", events.AgentMessage{MessageID: "saved", Content: []events.ContentBlock{events.Text("partial")}}, true))
	ticket06FaultHistory(t, svc, ref, 1)
	d.pipe.End(nil)
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "failed" }, time.Second, time.Millisecond)
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.False(t, r.Status.HistoryLoad.NativeCompleted)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
}

// The write took effect, but its acknowledgement was lost. Readback, not a
// second create or native import, must resolve the durable acceptance boundary.
type ticket06UnknownCreate struct {
	metastore.MetaStore
	calls int
}

func (m *ticket06UnknownCreate) CreateSession(r resource.Session) (resource.Session, error) {
	m.calls++
	saved, err := m.MetaStore.CreateSession(r)
	if err != nil {
		return saved, err
	}
	return resource.Session{}, errors.New("metadata acknowledgement lost")
}

func TestTicket06AcceptanceResolvesUnknownMetadataByReadback(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	meta := &ticket06UnknownCreate{MetaStore: st}
	d := &ticket06FaultDriver{pipe: events.NewEventPipe(16), bindings: map[string]*ticket06FaultBinding{}, loading: make(chan *ticket06FaultBinding, 1)}
	svc := service.New(meta, st, runtime.NewRegistry(map[string]runtime.Factory{"fault-peer": ticket06FaultFactory{d}}))
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = svc.Shutdown(c)
		d.pipe.End(nil)
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fault-peer", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ticket06FaultContext(t), "agent")
	require.NoError(t, err)
	receipt, err := svc.LoadSession(ticket06FaultContext(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: "native-import"})
	require.NoError(t, err)
	require.Equal(t, 1, meta.calls)
	require.NotEmpty(t, receipt.LoadID)
	select {
	case b := <-d.loading:
		require.Equal(t, receipt.LoadID, b.loadID)
	case <-ticket06FaultContext(t).Done():
		t.Fatal("resolved acceptance never produced native load")
	}
}

type ticket06AppendFailure struct {
	eventstore.EventStore
	failed atomic.Bool
}

func (h *ticket06AppendFailure) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == "native-import" && !h.failed.Swap(true) {
		return nil, errors.New("original append unavailable")
	}
	return h.EventStore.Append(id, records)
}

func TestTicket06AppendFailureRequiresExplicitOriginalUUIDSettlement(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	svc, d := ticket06FaultService(t, st, &ticket06AppendFailure{EventStore: st})
	_, b := ticket06FaultLoad(t, svc, d)
	ref := resource.SessionRef{SessionID: b.id}
	require.NoError(t, d.emit(ticket06FaultContext(t), b, "saved", events.AgentMessage{MessageID: "saved", Content: []events.ContentBlock{events.Text("original fact")}}, true))
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "failed" }, time.Second, time.Millisecond)
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.False(t, r.Status.HistoryLoad.CleanupCompleted, "an unresolved append remains owned")
	require.NoError(t, svc.ReconcileHistory(ticket06FaultContext(t), ref))
	page := ticket06FaultHistory(t, svc, ref, 1)
	require.Equal(t, "original fact", page[0].Payload().(events.AgentMessage).Content[0].Text)
	require.Eventually(t, func() bool {
		current, e := svc.GetSession(ref)
		return e == nil && current.Status.HistoryLoad.CleanupCompleted && current.Status.HistoryLoad.LastCommittedEventID == page[0].EventID
	}, time.Second, time.Millisecond)
	r, err = svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, "failed", r.Status.HistoryLoad.Phase)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
}
