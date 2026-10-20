package file_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// Adapted from the preserved combined-store startup experiment. Only event
// projection startup is interrupted; no metadata lifecycle is involved.
func TestInterruptedEventProjectionStartup(t *testing.T) {
	if dir := os.Getenv("MASS_TEST_EVENTSTORE_STARTUP_DIR"); dir != "" {
		st, err := file.Open(dir, false)
		require.NoError(t, err)
		defer st.Close()
		require.NoError(t, os.WriteFile(os.Getenv("MASS_TEST_EVENTSTORE_READY"), []byte("ready"), 0o600))
		return
	}
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	owned := st
	t.Cleanup(func() { require.NoError(t, owned.Close()) })
	const count = 2048
	var first, last events.SessionEvent
	for offset := 0; offset < count; offset += 128 {
		batch := make([]events.SessionEvent, 0, 128)
		for i := range 128 {
			e, err := events.NewEvent(events.EventBase{EventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", offset+i), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: "startup-item", Time: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}, events.AgentMessage{MessageID: strconv.Itoa(offset + i), Content: []events.ContentBlock{events.Text(strings.Repeat("x", 8192))}}, events.NewSource())
			require.NoError(t, err)
			batch = append(batch, e)
		}
		if offset == 0 {
			first = batch[0]
		}
		last = batch[len(batch)-1]
		_, err = st.Append("native", batch)
		require.NoError(t, err)
	}
	page, err := st.ListHistory("native", store.HistoryQuery{Limit: 1})
	require.NoError(t, err)
	cursor := page.Next
	path := logPath(dir)
	digest := func() [32]byte {
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()
		h := sha256.New()
		_, err = io.Copy(h, f)
		require.NoError(t, err)
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		return sum
	}
	before := digest()
	require.NoError(t, st.Close())
	require.NoError(t, os.Remove(filepath.Join(dir, "history.sqlite")))
	ready := filepath.Join(t.TempDir(), "ready")
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestInterruptedEventProjectionStartup$")
	cmd.Env = append(os.Environ(), "MASS_TEST_EVENTSTORE_STARTUP_DIR="+dir, "MASS_TEST_EVENTSTORE_READY="+ready)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Stat(filepath.Join(dir, "history.sqlite"))
		if err == nil && info.Size() > 1024*1024 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("startup recovery did not reach disk batches")
		case <-ticker.C:
		}
	}
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	_, err = os.Stat(ready)
	require.True(t, os.IsNotExist(err), "partial recovery cannot return a ready Store: %s", output.String())
	require.Equal(t, before, digest())
	st, err = file.Open(dir, false)
	require.NoError(t, err)
	reopened := st
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	page, err = st.ListHistory("native", store.HistoryQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, first.EventID, page.Events[0].EventID)
	page, err = st.ListHistory("native", store.HistoryQuery{After: cursor, Limit: count})
	require.NoError(t, err)
	require.Len(t, page.Events, count-1)
	require.Equal(t, last.EventID, page.Events[len(page.Events)-1].EventID)
	require.Equal(t, last.EventID, *page.SyncedEventID)
	progress, err := st.Append("native", []events.SessionEvent{last})
	require.NoError(t, err)
	require.Equal(t, last.EventID, *progress)
	require.Equal(t, before, digest())
}
