package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func TestTicket05ArchivedIsNotAPublicSessionFilter(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	svc := service.New(st, st, nil)
	_, err = svc.ListSessions(resource.SessionQuery{State: events.SessionState("archived")})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
}

func TestTicket05PrepareMigratesLegacyArchive(t *testing.T) {
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
			legacy, err := meta.CreateSession(resource.Session{
				Metadata: resource.Metadata{Namespace: "one", Name: "legacy"},
				Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic", SystemPrompt: "preserved"}},
				Status: resource.SessionStatus{
					HistoryLoad: completedLoad,
					SessionID:   "native/legacy",
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
				Metadata: resource.Metadata{Namespace: "one", Name: "legacy-pending"},
				Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
				Status: resource.SessionStatus{
					HistoryLoad: pendingLoad,
					SessionID:   "native/pending",
					State:       "offline",
				},
			})
			require.NoError(t, err)
			require.NoError(t, meta.Close())

			legacy.Status.State = "archived"
			legacy.Status.Reason = "legacy archive marker"
			legacy.Status.AppliedGeneration = 4
			legacy.Status.RuntimeGeneration = 3
			legacy.Status.Capabilities = runtime.Capabilities{"input": true}
			legacy.Status.EffectiveFeatures = &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)}
			writeLegacySessionState(t, dir, backendName, legacy)

			pending.Status.State = "archived"
			pending.Status.Reason = "legacy archive marker"
			writeLegacySessionState(t, dir, backendName, pending)

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
			require.Zero(t, factory.created.Load(), "migration must not construct a Driver")

			migrated, err := svc.GetSession(resource.SessionRef{SessionID: legacy.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, events.SessionState("offline"), migrated.Status.State)
			require.Empty(t, migrated.Status.Reason)
			require.Zero(t, migrated.Status.AppliedGeneration)
			require.Zero(t, migrated.Status.RuntimeGeneration)
			require.Nil(t, migrated.Status.Capabilities)
			require.Nil(t, migrated.Status.EffectiveFeatures)
			require.Equal(t, legacy.Status.HistoryLoad, migrated.Status.HistoryLoad)
			require.Equal(t, legacy.Metadata.UID, migrated.Metadata.UID)
			require.Equal(t, legacy.Metadata.CreatedAt, migrated.Metadata.CreatedAt)
			require.Equal(t, legacy.Metadata.Generation, migrated.Metadata.Generation)
			require.Equal(t, legacy.Spec, migrated.Spec)
			require.Equal(t, legacy.Status.SessionID, migrated.Status.SessionID)
			require.NotEqual(t, legacy.Metadata.ResourceVersion, migrated.Metadata.ResourceVersion)

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
			again, err := svc.GetSession(resource.SessionRef{SessionID: legacy.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, migrated.Metadata.ResourceVersion, again.Metadata.ResourceVersion)
			require.Zero(t, factory.created.Load())
		})
	}
}

func writeLegacySessionState(t *testing.T, dir, backendName string, legacy resource.Session) {
	t.Helper()
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	switch backendName {
	case "sqlite":
		db, err := sql.Open("sqlite", filepath.Join(dir, "metadata-authority.sqlite"))
		require.NoError(t, err)
		defer func() { require.NoError(t, db.Close()) }()
		result, err := db.ExecContext(t.Context(), "UPDATE session SET state='archived', payload=? WHERE session_id=?", payload, legacy.Status.SessionID)
		require.NoError(t, err)
		count, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
	case "file":
		path := filepath.Join(dir, "sessions", legacy.Metadata.Namespace, legacy.Metadata.Name, "state.json")
		require.NoError(t, os.WriteFile(path, payload, 0o600))
	default:
		t.Fatalf("unknown backend %q", backendName)
	}
}
