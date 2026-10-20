package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
)

func TestSameNameNewIdentityKeepsBroadcastsIsolated(t *testing.T) {
	for _, authority := range []string{"sqlite", "file"} {
		t.Run(authority, func(t *testing.T) {
			meta, err := backend.Open(t.TempDir(), metastore.Config{Backend: authority}, false)
			require.NoError(t, err)
			defer meta.Close()
			history, err := eventfile.Open(t.TempDir(), false)
			require.NoError(t, err)
			defer history.Close()
			svc := service.New(meta, history, runtime.NewRegistry(nil))
			defer func() { require.NoError(t, svc.Shutdown(context.Background())) }()
			named := resource.SessionRef{Namespace: "one", Name: "demo"}
			create := func(id string) resource.Session {
				r, err := meta.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: named.Namespace, Name: named.Name}, Spec: resource.SessionSpec{RuntimeRef: "unstarted"}, Status: resource.SessionStatus{SessionID: id, State: "archived"}})
				require.NoError(t, err)
				return r
			}
			old := create("old-identity")
			oldBroadcast, err := svc.EventBroadcast(named)
			require.NoError(t, err)
			oldSub, err := svc.SubscribeEvents(resource.SessionRef{SessionID: old.Status.SessionID})
			require.NoError(t, err)
			defer oldSub.Close()
			// Fixture replacement through MetaStore, not a product deletion API or
			// a decision about event-retention/cross-store cleanup semantics.
			require.NoError(t, meta.DeleteSession(named, old.Metadata.ResourceVersion))
			newer := create("new-identity")
			newBroadcast, err := svc.EventBroadcast(named)
			require.NoError(t, err)
			byID, err := svc.EventBroadcast(resource.SessionRef{SessionID: newer.Status.SessionID})
			require.NoError(t, err)
			require.Same(t, newBroadcast, byID)
			require.NotSame(t, oldBroadcast, newBroadcast)
			newSub, err := svc.SubscribeEvents(named)
			require.NoError(t, err)
			defer newSub.Close()
			for _, target := range []struct {
				id        string
				broadcast *events.EventBroadcast
				sub       *events.Subscription
			}{{old.Status.SessionID, oldBroadcast, oldSub}, {newer.Status.SessionID, newBroadcast, newSub}} {
				fact, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: named.Namespace, SessionName: named.Name, SessionID: target.id, Time: time.Now().UTC()}, events.SessionError{Message: "offline realtime probe"}, events.Source{})
				require.NoError(t, err)
				require.NoError(t, target.broadcast.Publish(fact))
				select {
				case got := <-target.sub.Events():
					require.Equal(t, fact.EventID, got.EventID)
				default:
					t.Fatal("synchronous publish did not admit its own event")
				}
				require.Empty(t, oldSub.Events())
				require.Empty(t, newSub.Events(), "same name must not mix persistent identities")
			}
		})
	}
}
