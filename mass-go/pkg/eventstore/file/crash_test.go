package file_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// Exit inside real SQLite transactions: no fake commit, event or acknowledgement.
func TestEventProjectionProcessExit(t *testing.T) {
	if dir := os.Getenv("MASS_EVENTSTORE_CRASH_DIR"); dir != "" {
		require.NoError(t, sqlite.RegisterScalarFunction("exit_process", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { os.Exit(77); return nil, nil }))
		st, err := file.Open(dir, false)
		require.NoError(t, err)
		stage := os.Getenv("MASS_EVENTSTORE_CRASH_STAGE")
		if stage != "response-lost" {
			statements := map[string]string{
				"before-event": "BEFORE INSERT ON event", "after-event": "AFTER INSERT ON event",
				"before-progress": "BEFORE UPDATE ON sync_progress", "after-progress": "AFTER UPDATE ON sync_progress",
			}
			db := diskSQL(t, dir)
			_, err = db.ExecContext(t.Context(), "CREATE TRIGGER interrupt "+statements[stage]+" BEGIN SELECT exit_process(); END")
			require.NoError(t, err)
		}
		var facts []events.SessionEvent
		require.NoError(t, json.Unmarshal(readBytes(t, filepath.Join(dir, "peer-events.json")), &facts))
		_, err = st.Append("native", facts)
		require.NoError(t, err)
		require.Equal(t, "response-lost", stage)
		os.Exit(77)
	}
	for _, stage := range []string{"before-event", "after-event", "before-progress", "after-progress", "response-lost"} {
		t.Run(stage, func(t *testing.T) {
			st, dir := open(t)
			old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("old")}})
			middle := event(t, "native", "middle", events.AgentMessage{MessageID: "middle", Content: []events.ContentBlock{events.Text("middle")}})
			_, err := st.Append("native", []events.SessionEvent{old, middle})
			require.NoError(t, err)
			page, err := st.ListHistory("native", store.HistoryQuery{Limit: 1})
			require.NoError(t, err)
			prefix := readBytes(t, logPath(dir))
			facts := []events.SessionEvent{event(t, "native", "new1", events.AgentMessage{MessageID: "new1", Content: []events.ContentBlock{events.Text("first new")}}), event(t, "native", "new2", events.AgentMessage{MessageID: "new2", Content: []events.ContentBlock{events.Text("last new")}})}
			b, err := json.Marshal(facts)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "peer-events.json"), b, 0o600))
			require.NoError(t, st.Close())
			binary, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestEventProjectionProcessExit$")
			cmd.Env = append(os.Environ(), "MASS_EVENTSTORE_CRASH_DIR="+dir, "MASS_EVENTSTORE_CRASH_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, string(output))
			require.Equal(t, 77, exit.ExitCode(), string(output))
			if stage != "response-lost" {
				rejectOpen(t, dir, true)
			}
			restored, err := file.Open(dir, false)
			require.NoError(t, err)
			defer restored.Close()
			next, err := restored.ListHistory("native", store.HistoryQuery{After: page.Next})
			require.NoError(t, err)
			require.Len(t, next.Events, 3)
			require.Equal(t, middle.EventID, next.Events[0].EventID)
			require.Equal(t, facts[1].EventID, *next.SyncedEventID)
			committed := readBytes(t, logPath(dir))
			require.True(t, bytes.HasPrefix(committed, prefix))
			_, err = restored.Append("native", append([]events.SessionEvent{old}, facts...))
			require.NoError(t, err)
			require.Equal(t, committed, readBytes(t, logPath(dir)))
			_, err = os.Stat(filepath.Join(dir, ".events.append.pending.json"))
			require.True(t, os.IsNotExist(err))
		})
	}
}
