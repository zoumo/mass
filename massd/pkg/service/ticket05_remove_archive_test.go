package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/internal/massdtest"
	"github.com/zoumo/mass/massd/pkg/events"
	eventfile "github.com/zoumo/mass/massd/pkg/eventstore/file"
	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/metastore/backend"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/service"
)

func TestTicket05ArchivedIsNotAPublicSessionFilter(t *testing.T) {
	st, err := massdtest.OpenFileStores(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	svc := service.New(st.Meta, st.Events, nil)
	_, err = svc.ListSessions(resource.SessionQuery{State: events.SessionState("archived")})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
}

func TestPreparePreservesHistoryLoadAcrossRestart(t *testing.T) {
	for _, backendName := range []string{"sqlite", "file"} {
		t.Run(backendName, func(t *testing.T) {
			dir := t.TempDir()
			meta, err := backend.Open(dir, metastore.Config{Backend: backendName}, false)
			require.NoError(t, err)
			_, err = meta.CreateRuntime(resource.Runtime{
				Metadata: resource.Metadata{Name: "agent"},
				Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
				Status:   resource.RuntimeStatus{State: "stopped"},
			})
			require.NoError(t, err)

			completedLoad := &resource.HistoryLoadStatus{
				LoadID:               "completed-load",
				Phase:                "completed",
				NativeVerified:       true,
				NativeResumed:        true,
				NativeCompleted:      true,
				DeliveryCompleted:    true,
				CleanupCompleted:     true,
				LastCommittedEventID: "event-17",
			}
			completed, err := meta.CreateSession(resource.Session{
				Metadata: resource.Metadata{Namespace: "one", Name: "completed"},
				Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic", SystemPrompt: "preserved"}},
				Status: resource.SessionStatus{
					HistoryLoad: completedLoad,
					SessionID:   "native/completed",
					State:       "offline",
				},
			})
			require.NoError(t, err)
			pendingLoad := &resource.HistoryLoadStatus{
				LoadID:               "pending-load",
				Phase:                "replaying",
				NativeVerified:       true,
				NativeResumed:        true,
				LastCommittedEventID: "event-9",
			}
			pending, err := meta.CreateSession(resource.Session{
				Metadata: resource.Metadata{Namespace: "one", Name: "pending"},
				Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
				Status: resource.SessionStatus{
					HistoryLoad: pendingLoad,
					SessionID:   "native/pending",
					State:       "offline",
				},
			})
			require.NoError(t, err)
			require.NoError(t, meta.Close())

			meta, err = backend.Open(dir, metastore.Config{Backend: backendName}, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, meta.Close()) })
			history, err := eventfile.Open(dir, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, history.Close()) })

			factory := &ticket03Factory{}
			svc := service.New(meta, history, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, svc.Shutdown(ctx))
			})
			require.NoError(t, svc.Prepare(context.Background()))
			require.Zero(t, factory.created.Load(), "preparation must not construct a Driver")

			retained, err := svc.GetSession(resource.SessionRef{SessionID: completed.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, completed, retained)

			preserved, err := svc.GetSession(resource.SessionRef{SessionID: pending.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, events.Unknown, preserved.Status.State)
			require.NotEmpty(t, preserved.Status.Reason)
			require.Equal(t, pending.Status.HistoryLoad, preserved.Status.HistoryLoad)
			require.Equal(t, pending.Metadata.UID, preserved.Metadata.UID)
			require.Equal(t, pending.Spec, preserved.Spec)
			require.Equal(t, pending.Status.SessionID, preserved.Status.SessionID)

			_, err = svc.ListSessions(resource.SessionQuery{State: "archived"})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)

			require.NoError(t, svc.Prepare(context.Background()))
			again, err := svc.GetSession(resource.SessionRef{SessionID: completed.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, retained.Metadata.ResourceVersion, again.Metadata.ResourceVersion)
			require.Zero(t, factory.created.Load())
		})
	}
}
