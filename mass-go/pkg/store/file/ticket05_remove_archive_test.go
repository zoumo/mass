package file_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestTicket05RejectArchivedSessionWritesAndFilters(t *testing.T) {
	st, _ := open(t)
	created, err := st.CreateSession(session("default", "legacy", "legacy-id"))
	require.NoError(t, err)

	forged := session("default", "forged", "forged-id")
	forged.Status.State = "archived"
	_, err = st.CreateSession(forged)
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)

	created.Status.State = "archived"
	_, err = st.UpdateSession(created)
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)

	_, err = st.ListSessions(resource.SessionQuery{State: "archived"})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)

	created.Status.State = "offline"
	updated, err := st.UpdateSession(created)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), updated.Status.State)
}
