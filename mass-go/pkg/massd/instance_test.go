package massd

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type closeCounter struct {
	closed int
	err    error
}

type delayedShutdown struct{}

func (*delayedShutdown) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *closeCounter) Close() error {
	c.closed++
	return c.err
}

func TestStoreOwnershipRetainedForUnsettledService(t *testing.T) {
	first, second := &closeCounter{}, &closeCounter{}
	settled, err := settleService(t.Context(), &delayedShutdown{}, 20*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, settled)
	require.NoError(t, closeStores(settled, first, second))
	require.Zero(t, first.closed, "Store ownership must remain held while settlement is active")
	require.Zero(t, second.closed)

	require.NoError(t, closeStores(true, first, second))
	require.Equal(t, 1, first.closed)
	require.Equal(t, 1, second.closed)

	first.err = errors.New("first close failed")
	second.err = errors.New("second close failed")
	err = closeStores(true, first, second)
	require.ErrorContains(t, err, "first close failed")
	require.ErrorContains(t, err, "second close failed")
}

func TestPrepareFailureReleasesDataDirectory(t *testing.T) {
	options := Options{DataDir: t.TempDir(), Transport: transport.Options{WriteTimeout: time.Second}}
	first, err := Start(t.Context(), options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Shutdown(context.Background())) })
	require.NoError(t, first.Shutdown(t.Context()))

	meta, err := backend.Open(options.DataDir, options.MetaStore, false)
	require.NoError(t, err)
	metaOpen := true
	t.Cleanup(func() {
		if metaOpen {
			require.NoError(t, meta.Close())
		}
	})
	r, err := meta.GetRuntime("codex")
	require.NoError(t, err)
	r.Status.State = "running"
	_, err = meta.UpdateRuntime(r)
	require.NoError(t, err)
	require.NoError(t, meta.Close())
	metaOpen = false

	db, err := sql.Open("sqlite", filepath.Join(options.DataDir, "metadata-authority.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `CREATE TRIGGER fail_prepare BEFORE UPDATE ON runtime
		WHEN NEW.state = 'unknown' BEGIN SELECT RAISE(ABORT, 'prepare write rejected'); END`)
	require.NoError(t, err)
	failed, err := Start(t.Context(), options)
	if failed != nil {
		t.Cleanup(func() { require.NoError(t, failed.Shutdown(context.Background())) })
	}
	require.ErrorContains(t, err, "prepare write rejected")
	require.Nil(t, failed)

	_, err = db.ExecContext(t.Context(), `DROP TRIGGER fail_prepare`)
	require.NoError(t, err)
	instance, err := Start(t.Context(), options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })
	r, err = instance.service.GetRuntime("codex")
	require.NoError(t, err)
	require.Equal(t, "unknown", r.Status.State)
}
