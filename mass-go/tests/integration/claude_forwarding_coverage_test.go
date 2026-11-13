package integration_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type forwardingNotificationFactory struct {
	runtime.Factory
	release <-chan struct{}
}

func (f forwardingNotificationFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	driver, err := f.Factory.New(name, config)
	if err != nil {
		return nil, err
	}
	return forwardingNotificationDriver{Driver: driver, release: f.release}, nil
}

type forwardingNotificationDriver struct {
	runtime.Driver
	release <-chan struct{}
}

func (d forwardingNotificationDriver) Events() (events.Stream, error) {
	stream, err := d.Driver.Events()
	return forwardingNotificationStream{Stream: stream, release: d.release}, err
}

type forwardingNotificationStream struct {
	events.Stream
	release <-chan struct{}
}

func (s forwardingNotificationStream) Next(ctx context.Context) (events.SessionEvent, error) {
	event, err := s.Stream.Next(ctx)
	if err != nil {
		return event, err
	}
	select {
	case <-s.release:
		return event, nil
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}

func TestClaudeAssignedRootForwardingGapSurvivesRPCNotifications(t *testing.T) {
	authority, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, authority.Close()) })
	releaseEvents := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseEvents) })
	defer release()
	factory := forwardingNotificationFactory{Factory: claude.Factory{RecoveryDir: t.TempDir()}, release: releaseEvents}
	svc := service.NewWithPolicy(authority, authority, runtime.NewRegistry(map[string]runtime.Factory{"claude": factory}), prototype.AutoApprovePermissions)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, svc.Shutdown(cleanup))
	})
	handler, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	t.Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := newHTTPClient(t, server.URL, "")
	opened := openNative(t, svc, "claude", "basic", "forwarding")
	_, err = uuid.Parse(opened.Status.SessionID)
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: opened.Status.SessionID}
	observer, err := client.Subscribe(ctx(t), ref)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, observer.Close()) })
	release()
	event, err := observer.Next(ctx(t))
	require.NoError(t, err)
	require.Equal(t, "session.error", event.Type())
	require.Equal(t, ref.SessionID, event.SessionID)
	require.Equal(t, opened.Metadata.Namespace, event.Namespace)
	require.Equal(t, opened.Metadata.Name, event.SessionName)
	require.NotEmpty(t, event.EventID)
	require.Equal(t, events.Source{}, event.Source())
	requireClaudeForwardingGap(t, event)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Events)
	var receipt runtime.InputReceipt
	require.NoError(t, client.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}, &receipt))
	require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
	answered := false
	for {
		event, err = observer.Next(ctx(t))
		require.NoError(t, err)
		if event.Type() == "agent.message" {
			answered = true
		}
		if state, ok := event.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle && answered {
			break
		}
	}
	require.Eventually(t, func() bool {
		page, err = svc.ListHistoryEvents(ref, store.HistoryQuery{Types: []string{"agent.message"}})
		return err == nil && len(page.Events) > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, client.CloseSession(ctx(t), ref, nil))
}

func requireClaudeForwardingGap(t *testing.T, event events.SessionEvent) {
	t.Helper()
	require.False(t, event.Persistent())
	diagnostic, ok := event.Payload().(events.SessionError)
	require.True(t, ok)
	require.Equal(t, "native_relationship_gap", diagnostic.Reason)
	meta, ok := event.Meta["claude"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "forwarding-not-confirmed", meta["observationReason"])
	require.Equal(t, "initialize-forwardSubagentText-is-a-request", meta["evidence"])
}
