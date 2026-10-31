package service_test

import (
	"context"
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

type loadDriver struct {
	runtime.Driver
	pipe    *events.EventPipe
	entered chan context.Context
	release chan struct{}
}

func (d *loadDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*loadDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (*loadDriver) CloseSession(context.Context, string) error              { return nil }
func (*loadDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (d *loadDriver) Stop(context.Context) error                            { d.pipe.End(nil); return nil }
func (d *loadDriver) LoadSession(ctx context.Context, id string, a runtime.Address, cwd, loadID string) error {
	d.entered <- ctx
	<-d.release
	source := events.NewSource()
	for _, p := range []events.Payload{
		events.HistoryReplay{LoadID: loadID, Phase: "started", RetainedScope: "all_retained_main_history", ResumedSession: &events.ResumedSession{SessionID: id, Capabilities: map[string]bool{}}},
		events.UserMessage{MessageID: "first", Content: []events.ContentBlock{events.Text("saved input")}},
		events.HistoryReplay{LoadID: loadID, Phase: "native_completed", RetainedScope: "all_retained_main_history", ResumedSession: &events.ResumedSession{SessionID: id, Capabilities: map[string]bool{}}},
	} {
		e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: a.Namespace, SessionName: a.SessionName, SessionID: id, Time: time.Now(), ItemID: "first", Replay: &events.ReplaySource{LoadID: loadID, NativeRecordID: "first", NativeParentID: "turn"}}, p, source)
		if err != nil {
			return err
		}
		if err := d.pipe.Send(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

type loadFactory struct{ d *loadDriver }

func (loadFactory) Validate(runtime.Config) error                        { return nil }
func (f loadFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.d, nil }

func TestLoadAcceptedBeforeNativeReadAndOutlivesRequest(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	d := &loadDriver{pipe: events.NewEventPipe(16), entered: make(chan context.Context, 1), release: make(chan struct{})}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": loadFactory{d}}))
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receipt, err := svc.LoadSession(ctx, resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: "native"})
	require.NoError(t, err)
	require.NotEmpty(t, receipt.LoadID)
	require.Equal(t, "accepted", receipt.Session.Status.HistoryLoad.Phase)
	select {
	case owner := <-d.entered:
		cancel()
		require.NoError(t, owner.Err())
	case <-time.After(time.Second):
		t.Fatal("no native load")
	}
	_, err = svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: "native"}, runtime.Input{Content: []events.ContentBlock{events.Text("too soon")}})
	require.Error(t, err)
	close(d.release)
	require.Eventually(t, func() bool {
		r, e := svc.GetSession(resource.SessionRef{SessionID: "native"})
		if e == nil && r.Status.HistoryLoad.Phase == "failed" {
			t.Logf("load failure: %+v", r.Status.HistoryLoad)
		}
		return e == nil && r.Status.HistoryLoad.Phase == "completed"
	}, time.Second, time.Millisecond)
	page, err := svc.ListHistoryEvents(resource.SessionRef{SessionID: "native"}, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, "saved input", page.Events[0].Payload().(events.UserMessage).Content[0].Text)
}
