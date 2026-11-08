package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type defaultsDriver struct {
	runtime.Driver
	pipe *events.EventPipe

	mu      sync.Mutex
	starts  int
	opens   []runtime.SessionSpec
	resumes []runtime.SessionSpec
	next    int
}

func (d *defaultsDriver) Events() (events.Stream, error) { return d.pipe, nil }

func (d *defaultsDriver) Start(context.Context) (runtime.Capabilities, error) {
	d.mu.Lock()
	d.starts++
	d.mu.Unlock()
	return runtime.Capabilities{"textInput": true}, nil
}

func (d *defaultsDriver) OpenSession(_ context.Context, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.next++
	id := fmt.Sprintf("native-%d", d.next)
	d.opens = append(d.opens, spec)
	d.mu.Unlock()
	return runtime.DriverSession{
		SessionID:         id,
		Capabilities:      runtime.Capabilities{"textInput": true},
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
		Source:            events.NewSource(),
	}, nil
}

func (d *defaultsDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.resumes = append(d.resumes, spec)
	d.mu.Unlock()
	return runtime.DriverSession{
		SessionID:         id,
		Capabilities:      runtime.Capabilities{"textInput": true},
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
		Source:            events.NewSource(),
	}, nil
}

func (*defaultsDriver) CloseSession(context.Context, string) error { return nil }
func (*defaultsDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (d *defaultsDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type defaultsFactory struct{ driver *defaultsDriver }

func (defaultsFactory) Validate(runtime.Config) error { return nil }
func (f defaultsFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func newDefaultsFixture(t *testing.T) (*service.Service, *defaultsDriver, *file.Store) {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	driver := &defaultsDriver{pipe: events.NewEventPipe(32)}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": defaultsFactory{driver: driver}}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	features := &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)}
	servers := []runtime.MCPServer{{
		Name:    "runtime",
		Type:    "http",
		URL:     "https://example.invalid/runtime",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	}}
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec: resource.RuntimeSpec{
			Type: "fixture", Config: runtime.Config{}, Features: features, MCPServers: &servers,
		},
	})
	require.NoError(t, err)
	return svc, driver, st
}

func TestRuntimeDefaultsAreAppliedOnlyWhenSessionOpens(t *testing.T) {
	svc, driver, _ := newDefaultsFixture(t)
	_, err := svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	driver.mu.Lock()
	require.Equal(t, 1, driver.starts)
	require.Empty(t, driver.opens)
	driver.mu.Unlock()

	updatedServers := []runtime.MCPServer{{
		Name: "updated",
		Type: "http",
		URL:  "https://example.invalid/updated",
	}}
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{
		Type: "fixture", Config: runtime.Config{},
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)},
		MCPServers: &updatedServers,
	})
	require.NoError(t, err)

	inherited, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "inherited"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
	})
	require.NoError(t, err)
	require.Nil(t, inherited.Spec.Features)
	require.Nil(t, inherited.Spec.MCPServers)

	finalServers := []runtime.MCPServer{
		{Name: "updated", Type: "http", URL: "https://example.invalid/final"},
		{Name: "default", Type: "http", URL: "https://example.invalid/default"},
	}
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{
		Type: "fixture", Config: runtime.Config{},
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		MCPServers: &finalServers,
	})
	require.NoError(t, err)

	sessionServers := []runtime.MCPServer{
		{Name: "updated", Type: "stdio", Command: "/session-updated"},
		{Name: "session", Type: "http", URL: "https://example.invalid/session"},
	}
	overridden, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "overridden"},
		Spec: resource.SessionSpec{
			RuntimeRef: "agent",
			SessionSpec: runtime.SessionSpec{
				Cwd:        "/work",
				Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)},
				MCPServers: &sessionServers,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, overridden.Spec.Features.AgentQuestionsEnabled())
	require.Equal(t, sessionServers, *overridden.Spec.MCPServers)

	driver.mu.Lock()
	require.Len(t, driver.opens, 2)
	first := driver.opens[0]
	second := driver.opens[1]
	starts := driver.starts
	driver.mu.Unlock()
	require.True(t, first.Features.AgentQuestionsEnabled())
	require.Equal(t, updatedServers, *first.MCPServers)
	require.True(t, second.Features.AgentQuestionsEnabled())
	require.Equal(t, []runtime.MCPServer{
		{Name: "updated", Type: "stdio", Command: "/session-updated"},
		{Name: "default", Type: "http", URL: "https://example.invalid/default"},
		{Name: "session", Type: "http", URL: "https://example.invalid/session"},
	}, *second.MCPServers)
	require.Equal(t, 1, starts)
}

func TestSessionDefaultsAreReappliedOnResume(t *testing.T) {
	svc, driver, st := newDefaultsFixture(t)

	_, err := svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	servers := []runtime.MCPServer{{Name: "runtime", Type: "stdio", Command: "/runtime-mcp"}}
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{
		Type: "fixture", Config: runtime.Config{},
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		MCPServers: &servers,
	})
	require.NoError(t, err)
	original, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "resume"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
		Status:   resource.SessionStatus{SessionID: "native-resume", State: "offline"},
	})
	require.NoError(t, err)

	resumed, err := svc.ResumeSession(context.Background(), resource.SessionRef{SessionID: original.Status.SessionID})
	require.NoError(t, err)
	require.Nil(t, resumed.Spec.Features)
	require.Nil(t, resumed.Spec.MCPServers)

	driver.mu.Lock()
	require.Len(t, driver.resumes, 1)
	spec := driver.resumes[0]
	driver.mu.Unlock()
	require.False(t, spec.Features.AgentQuestionsEnabled())
	require.Equal(t, "runtime", (*spec.MCPServers)[0].Name)
}

func TestInvalidSessionMCPRejectedBeforeRuntimeStarts(t *testing.T) {
	svc, driver, _ := newDefaultsFixture(t)
	_, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "invalid"},
		Spec: resource.SessionSpec{
			RuntimeRef:  "agent",
			SessionSpec: runtime.SessionSpec{Cwd: "/work", MCPServers: &[]runtime.MCPServer{{Name: "bad", Type: "stdio"}}},
		},
	})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	driver.mu.Lock()
	require.Zero(t, driver.starts)
	driver.mu.Unlock()
}

func TestRuntimeEmptyMCPListExcludesAmbientWhileSessionEmptyIsNoOverride(t *testing.T) {
	svc, driver, _ := newDefaultsFixture(t)
	_, err := svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)

	empty := []runtime.MCPServer{}
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{
		Type: "fixture", Config: runtime.Config{}, MCPServers: &empty,
	})
	require.NoError(t, err)
	_, err = svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "runtime-empty"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
	})
	require.NoError(t, err)

	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}})
	require.NoError(t, err)
	_, err = svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "session-empty"},
		Spec: resource.SessionSpec{
			RuntimeRef: "agent",
			SessionSpec: runtime.SessionSpec{
				Cwd:        "/work",
				MCPServers: &empty,
			},
		},
	})
	require.NoError(t, err)
	_, err = svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "undeclared"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/work"}},
	})
	require.NoError(t, err)

	driver.mu.Lock()
	defer driver.mu.Unlock()
	require.Len(t, driver.opens, 3)
	require.NotNil(t, driver.opens[0].MCPServers)
	require.Empty(t, *driver.opens[0].MCPServers)
	require.Nil(t, driver.opens[1].MCPServers)
	require.Nil(t, driver.opens[2].MCPServers)
}

type defaultsLoadDriver struct {
	runtime.Driver
	pipe  *events.EventPipe
	specs chan runtime.SessionSpec
}

func (d *defaultsLoadDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*defaultsLoadDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (*defaultsLoadDriver) CloseSession(context.Context, string) error { return nil }
func (*defaultsLoadDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}
func (*defaultsLoadDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (d *defaultsLoadDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

func (d *defaultsLoadDriver) LoadSession(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec, loadID string) error {
	d.specs <- spec
	source := events.NewSource()
	for _, payload := range []events.Payload{
		events.HistoryReplay{
			LoadID:        loadID,
			Phase:         "started",
			RetainedScope: "all_retained_main_history",
			ResumedSession: &events.ResumedSession{
				SessionID:         id,
				Capabilities:      runtime.Capabilities{},
				EffectiveFeatures: events.ResumedSessionFeatures{AgentQuestions: spec.Features.AgentQuestionsEnabled()},
			},
		},
		events.HistoryReplay{
			LoadID:        loadID,
			Phase:         "native_completed",
			RetainedScope: "all_retained_main_history",
			ResumedSession: &events.ResumedSession{
				SessionID:         id,
				Capabilities:      runtime.Capabilities{},
				EffectiveFeatures: events.ResumedSessionFeatures{AgentQuestions: spec.Features.AgentQuestionsEnabled()},
			},
		},
	} {
		event, err := events.NewEvent(events.EventBase{
			EventID:     uuid.NewString(),
			Namespace:   address.Namespace,
			SessionName: address.SessionName,
			SessionID:   id,
			Time:        time.Now(),
			ItemID:      "replay",
			Replay:      &events.ReplaySource{LoadID: loadID, NativeRecordID: "replay", NativeParentID: "turn"},
		}, payload, source)
		if err != nil {
			return err
		}
		if err := d.pipe.Send(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type defaultsLoadFactory struct{ driver *defaultsLoadDriver }

func (defaultsLoadFactory) Validate(runtime.Config) error { return nil }
func (f defaultsLoadFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func TestRuntimeDefaultsAreAppliedToHistoryLoad(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	driver := &defaultsLoadDriver{pipe: events.NewEventPipe(16), specs: make(chan runtime.SessionSpec, 1)}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": defaultsLoadFactory{driver: driver}}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec: resource.RuntimeSpec{
			Type:     "fixture",
			Config:   runtime.Config{},
			Features: &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)},
			MCPServers: &[]runtime.MCPServer{{
				Name: "stale", Type: "http", URL: "https://example.invalid/stale",
			}},
		},
	})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	_, err = svc.UpdateRuntime("agent", resource.RuntimeSpec{
		Type:     "fixture",
		Config:   runtime.Config{},
		Features: &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		MCPServers: &[]runtime.MCPServer{{
			Name: "runtime", Type: "http", URL: "https://example.invalid/runtime",
		}},
	})
	require.NoError(t, err)
	overrides := []runtime.MCPServer{
		{Name: "session", Type: "http", URL: "https://example.invalid/session"},
	}
	accepted, err := svc.LoadSession(context.Background(), resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "one", Name: "loaded"},
		RuntimeRef: "agent",
		Cwd:        "/work",
		SessionID:  "native-load",
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)},
		MCPServers: &overrides,
	})
	require.NoError(t, err)
	select {
	case spec := <-driver.specs:
		require.True(t, spec.Features.AgentQuestionsEnabled())
		require.Equal(t, []runtime.MCPServer{
			{Name: "runtime", Type: "http", URL: "https://example.invalid/runtime"},
			{Name: "session", Type: "http", URL: "https://example.invalid/session"},
		}, *spec.MCPServers)
	case <-time.After(time.Second):
		t.Fatal("history loader did not receive Session defaults")
	}
	require.Eventually(t, func() bool {
		session, getErr := svc.GetSession(resource.SessionRef{SessionID: accepted.Session.Status.SessionID})
		return getErr == nil && session.Status.HistoryLoad != nil && session.Status.HistoryLoad.Phase == "completed"
	}, time.Second, time.Millisecond)
}
