package file_test

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func diskSQL(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(dir, "history.sqlite")}).String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestConsumptionCheckpointFailureSealsReadsUntilReconciled(t *testing.T) {
	st, dir := open(t)
	old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("old")}})
	_, err := st.Append("native", []events.SessionEvent{old})
	require.NoError(t, err)
	db := diskSQL(t, dir)
	// Abort the file-consumption checkpoint update after inserting the new rows.
	_, err = db.ExecContext(t.Context(), `CREATE TRIGGER fail_checkpoint BEFORE UPDATE OF file_end ON sync_progress BEGIN SELECT RAISE(ABORT,'checkpoint failure'); END`)
	require.NoError(t, err)
	fact := event(t, "native", "new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new")}})
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = st.ListHistory("native", store.HistoryQuery{})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = st.GetSyncProgress("native")
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.ErrorContains(t, err, "outcome_unknown")
	pending := filepath.Join(dir, ".events.append.pending.json")
	_, err = os.Stat(pending)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, st.Reconcile())
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	page, err := st.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, fact.EventID, *page.SyncedEventID)
}

func TestReadonlyProgressRejectsPendingWriterCommit(t *testing.T) {
	st, dir := open(t)
	old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("old")}})
	_, err := st.Append("native", []events.SessionEvent{old})
	require.NoError(t, err)
	reader, err := file.Open(dir, true)
	require.NoError(t, err)
	defer reader.Close()
	db := diskSQL(t, dir)
	_, err = db.ExecContext(t.Context(), `CREATE TRIGGER fail_progress BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT,'progress failure'); END`)
	require.NoError(t, err)
	fact := event(t, "native", "new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new")}})
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = reader.ListHistory("native", store.HistoryQuery{})
	require.Error(t, err)
	_, err = reader.GetSyncProgress("native")
	require.Error(t, err, "a detached reader must not report stale progress while a file commit is pending")
}

func logPath(dir string) string {
	return filepath.Join(dir, "sessions", "default", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("native")), "events.jsonl")
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func TestMatchingHalfWriteRecoveryAndEvidenceRejection(t *testing.T) {
	for _, damage := range []string{"half", "before-write", "divergent", "bad-json", "bad-format", "bad-identity", "duplicate", "bad-record", "committed-corruption", "other-corruption"} {
		t.Run(damage, func(t *testing.T) {
			st, dir := open(t)
			old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("committed original")}})
			_, err := st.Append("native", []events.SessionEvent{old})
			require.NoError(t, err)
			other := event(t, "other", "other", events.AgentMessage{MessageID: "other", Content: []events.ContentBlock{events.Text("other committed")}})
			_, err = st.Append("other", []events.SessionEvent{other})
			require.NoError(t, err)
			prefix := readBytes(t, logPath(dir))
			db := diskSQL(t, dir)
			_, err = db.ExecContext(t.Context(), `CREATE TRIGGER fail_progress BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT,'progress failure'); END`)
			require.NoError(t, err)
			fact := event(t, "native", "new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new original")}})
			_, err = st.Append("native", []events.SessionEvent{old, fact, fact})
			require.ErrorContains(t, err, "outcome_unknown")
			intentPath := filepath.Join(dir, ".events.append.pending.json")
			var intent map[string]any
			require.NoError(t, json.Unmarshal(readBytes(t, intentPath), &intent))
			data, err := base64.StdEncoding.DecodeString(intent["data"].(string))
			require.NoError(t, err)
			require.NotContains(t, string(data), old.EventID, "intent must contain only new events")
			require.Equal(t, 1, bytes.Count(data, []byte("\n")))
			// Model a real interrupted file write using only our own disk files. This
			// supplements the real process exits at SQLite boundaries in crash_test.go.
			tail := data[:len(data)/2]
			require.NoError(t, st.Close())
			require.NoError(t, db.Close())
			switch damage {
			case "before-write":
				tail = nil
			case "divergent":
				tail = []byte("divergent tail")
			case "bad-json":
				require.NoError(t, os.WriteFile(intentPath, []byte("{invalid"), 0o600))
			case "bad-format":
				intent["format"] = 999
			case "bad-identity":
				intent["uid"] = "foreign"
			case "duplicate":
				intent["data"] = append(append([]byte{}, data...), data...)
			case "bad-record":
				intent["data"] = []byte("invalid event\n")
			case "committed-corruption":
				prefix = bytes.Replace(prefix, []byte("committed original"), []byte("committed modified"), 1)
			case "other-corruption":
				otherPath := filepath.Join(dir, "sessions", "default", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("other")), "events.jsonl")
				require.NoError(t, os.WriteFile(otherPath, []byte("bad committed record\n"), 0o600))
			}
			if strings.HasPrefix(damage, "bad-") && damage != "bad-json" || damage == "duplicate" {
				encoded, err := json.Marshal(intent)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(intentPath, encoded, 0o600))
			}
			require.NoError(t, os.WriteFile(logPath(dir), append(append([]byte{}, prefix...), tail...), 0o600))
			before := readBytes(t, logPath(dir))
			evidence := readBytes(t, intentPath)
			rejectOpen(t, dir, true)
			require.Equal(t, before, readBytes(t, logPath(dir)))
			require.Equal(t, evidence, readBytes(t, intentPath))
			restored, err := file.Open(dir, false)
			ownOpened(t, restored)
			if damage != "half" && damage != "before-write" {
				require.Error(t, err)
				require.Equal(t, before, readBytes(t, logPath(dir)))
				require.Equal(t, evidence, readBytes(t, intentPath))
				return
			}
			require.NoError(t, err)
			defer restored.Close()
			require.Equal(t, append(append([]byte{}, prefix...), data...), readBytes(t, logPath(dir)))
			_, err = restored.Append("native", []events.SessionEvent{fact, old})
			require.NoError(t, err)
			page, err := restored.ListHistory("native", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			require.Equal(t, fact.EventID, *page.SyncedEventID)
		})
	}
}

func TestProjectionDamageRebuildPreservesAuthorityAndReadonlyCleanup(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "stale", "sql-only", "incompatible"} {
		t.Run(damage, func(t *testing.T) {
			st, dir := open(t)
			one := event(t, "native", "one", events.ToolCall{ID: "one", Kind: events.ToolKindExecute, ToolName: "shell", Provider: events.ToolProviderAgent, RawInput: map[string]any{"number": json.Number("9007199254740993"), "null": nil}})
			two := event(t, "native", "two", events.AgentMessage{MessageID: "two", Content: []events.ContentBlock{events.Text("complete payload")}})
			_, err := st.Append("native", []events.SessionEvent{one, two})
			require.NoError(t, err)
			first, err := st.ListHistory("native", store.HistoryQuery{Types: []string{"agent.message", "agent.tool_call"}, Limit: 1})
			require.NoError(t, err)
			require.NotEmpty(t, first.Next)
			require.NoError(t, st.Close())
			path := filepath.Join(dir, "history.sqlite")
			switch damage {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("not SQLite"), 0o600))
			default:
				db := diskSQL(t, dir)
				switch damage {
				case "stale":
					_, err = db.ExecContext(t.Context(), `DELETE FROM event; UPDATE sync_progress SET event_id=NULL`)
				case "sql-only":
					ghost := event(t, "native", "ghost", events.AgentMessage{MessageID: "ghost", Content: []events.ContentBlock{events.Text("not authority")}})
					b, e := json.Marshal(ghost)
					require.NoError(t, e)
					_, err = db.ExecContext(t.Context(), `INSERT INTO event VALUES(?,?,?,?,?)`, "native", 3, ghost.EventID, ghost.Type(), b)
				case "incompatible":
					_, err = db.ExecContext(t.Context(), `DROP TABLE event; CREATE TABLE event (unsupported INTEGER)`)
				}
				require.NoError(t, err)
				require.NoError(t, db.Close())
			}
			// Metadata authority may be inaccessible; no EventStore operation may inspect it.
			metaPath := filepath.Join(dir, "metadata.sqlite")
			require.NoError(t, os.Mkdir(metaPath, 0o700))
			beforeLog := readBytes(t, logPath(dir))
			beforeHeader := readBytes(t, filepath.Join(dir, ".events.json"))
			beforeSQL, _ := os.ReadFile(path)
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			for _, readonly := range []bool{true, false} {
				restored, err := file.Open(dir, readonly)
				ownOpened(t, restored)
				require.NoError(t, err)
				page, err := restored.ListHistory("native", store.HistoryQuery{Types: []string{"agent.tool_call", "agent.message"}, After: first.Next, Limit: 1})
				require.NoError(t, err)
				require.Len(t, page.Events, 1)
				require.Equal(t, two.EventID, page.Events[0].EventID)
				require.Empty(t, page.Next)
				require.Equal(t, two.EventID, *page.SyncedEventID)
				all, err := restored.ListHistory("native", store.HistoryQuery{})
				require.NoError(t, err)
				require.Len(t, all.Events, 2)
				require.Equal(t, json.Number("9007199254740993"), all.Events[0].Payload().(events.ToolCall).RawInput.(map[string]any)["number"])
				require.NoError(t, restored.Close())
				require.Equal(t, beforeLog, readBytes(t, logPath(dir)))
				require.Equal(t, beforeHeader, readBytes(t, filepath.Join(dir, ".events.json")))
				info, err := os.Stat(metaPath)
				require.NoError(t, err)
				require.True(t, info.IsDir())
				entries, err := os.ReadDir(temp)
				require.NoError(t, err)
				require.Empty(t, entries)
				if readonly {
					after, _ := os.ReadFile(path)
					require.Equal(t, beforeSQL, after)
				}
			}
		})
	}
}

func TestReopenRejectsChangedOrMissingCommittedRecords(t *testing.T) {
	for _, damage := range []string{"valid-change", "removed-record"} {
		t.Run(damage, func(t *testing.T) {
			st, dir := open(t)
			one := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("original committed body")}})
			two := event(t, "native", "two", events.AgentMessage{MessageID: "two", Content: []events.ContentBlock{events.Text("last committed body")}})
			_, err := st.Append("native", []events.SessionEvent{one, two})
			require.NoError(t, err)
			require.NoError(t, st.Close())
			b := readBytes(t, logPath(dir))
			if damage == "valid-change" {
				b = bytes.Replace(b, []byte("original committed body"), []byte("modified committed body"), 1)
			} else {
				b = b[:bytes.IndexByte(b, '\n')+1]
			}
			require.NoError(t, os.WriteFile(logPath(dir), b, 0o600))
			for _, readonly := range []bool{true, false} {
				reopened, err := file.Open(dir, readonly)
				if reopened != nil {
					require.NoError(t, reopened.Close())
				}
				require.Error(t, err, "a syntactically valid change is still committed-history corruption")
				require.Equal(t, b, readBytes(t, logPath(dir)))
			}
		})
	}
}

func TestOpenedProjectionTamperingCannotHideOrDuplicateFacts(t *testing.T) {
	for _, damage := range []string{"stale", "sql-only", "payload", "type", "progress", "checkpoint", "missing-progress"} {
		for _, operation := range []string{"list", "progress", "append"} {
			t.Run(damage+"/"+operation, func(t *testing.T) {
				st, dir := open(t)
				one := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("original body")}})
				_, err := st.Append("native", []events.SessionEvent{one})
				require.NoError(t, err)
				before := readBytes(t, logPath(dir))
				db := diskSQL(t, dir)
				switch damage {
				case "stale":
					_, err = db.ExecContext(t.Context(), `DELETE FROM event; UPDATE sync_progress SET event_id=NULL`)
				case "sql-only":
					ghost := one
					ghost.EventID = "00000000-0000-4000-8000-000000000001"
					b, e := json.Marshal(ghost)
					require.NoError(t, e)
					_, err = db.ExecContext(t.Context(), `INSERT INTO event VALUES(?,?,?,?,?)`, "native", 2, ghost.EventID, ghost.Type(), b)
				case "payload":
					_, err = db.ExecContext(t.Context(), `UPDATE event SET payload=replace(CAST(payload AS TEXT),'original body','modified body')`)
				case "type":
					_, err = db.ExecContext(t.Context(), `UPDATE event SET type='agent.tool_call'`)
				case "progress":
					_, err = db.ExecContext(t.Context(), `UPDATE sync_progress SET event_id=NULL`)
				case "checkpoint":
					_, err = db.ExecContext(t.Context(), `UPDATE sync_progress SET file_end=0`)
				case "missing-progress":
					_, err = db.ExecContext(t.Context(), `DELETE FROM sync_progress`)
				}
				require.NoError(t, err)
				switch operation {
				case "list":
					_, err = st.ListHistory("native", store.HistoryQuery{})
				case "progress":
					_, err = st.GetSyncProgress("native")
				case "append":
					_, err = st.Append("native", []events.SessionEvent{one})
				}
				require.ErrorContains(t, err, "outcome_unknown")
				require.Equal(t, before, readBytes(t, logPath(dir)))
				require.NoError(t, db.Close())
				require.NoError(t, st.Reconcile())
				_, err = st.Append("native", []events.SessionEvent{one})
				require.NoError(t, err)
				page, err := st.ListHistory("native", store.HistoryQuery{})
				require.NoError(t, err)
				require.Len(t, page.Events, 1)
				require.Equal(t, one.EventID, *page.SyncedEventID)
				require.Equal(t, before, readBytes(t, logPath(dir)))
			})
		}
	}
}

func ownOpened(t *testing.T, st *file.Store) {
	t.Helper()
	if st != nil {
		t.Cleanup(func() { require.NoError(t, st.Close()) })
	}
}

func rejectOpen(t *testing.T, dir string, readonly bool) {
	t.Helper()
	st, err := file.Open(dir, readonly)
	ownOpened(t, st)
	require.Error(t, err)
}

func TestCommitCheckpointAndCleanupFailureKeepRecoverableEvidence(t *testing.T) {
	for _, stage := range []string{"checkpoint-write", "intent-cleanup"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			function := "cleanup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			pending := filepath.Join(dir, ".events.append.pending.json")
			saved := filepath.Join(dir, "saved-append.json")
			// SQL calls this filesystem fault only after writing its new checkpoint.
			require.NoError(t, sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
				if err := os.Rename(pending, saved); err != nil {
					return nil, err
				}
				if err := os.Mkdir(pending, 0o700); err != nil {
					return nil, err
				}
				return nil, os.WriteFile(filepath.Join(pending, "prevent-removal"), []byte("owned fault"), 0o600)
			}))
			st, err := file.Open(dir, false)
			ownOpened(t, st)
			require.NoError(t, err)
			old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("committed")}})
			_, err = st.Append("native", []events.SessionEvent{old})
			require.NoError(t, err)
			commit := filepath.Join(filepath.Dir(logPath(dir)), "commit.json")
			backup := filepath.Join(dir, "saved-commit.json")
			if stage == "checkpoint-write" {
				require.NoError(t, os.Rename(commit, backup))
				require.NoError(t, os.Mkdir(commit, 0o700))
			} else {
				db := diskSQL(t, dir)
				_, err = db.ExecContext(t.Context(), "CREATE TRIGGER obstruct_cleanup AFTER UPDATE ON sync_progress BEGIN SELECT "+function+"(); END")
				require.NoError(t, err)
			}
			fact := event(t, "native", "new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new committed")}})
			_, err = st.Append("native", []events.SessionEvent{fact})
			require.ErrorContains(t, err, "outcome_unknown")
			committed := readBytes(t, logPath(dir))
			require.Contains(t, string(committed), fact.EventID)
			_, err = st.GetSyncProgress("native")
			require.ErrorContains(t, err, "outcome_unknown")
			if stage == "checkpoint-write" {
				require.NotEmpty(t, readBytes(t, pending))
				require.NoError(t, os.Remove(commit))
				require.NoError(t, os.Rename(backup, commit))
			} else {
				require.NotEmpty(t, readBytes(t, saved))
				require.NoError(t, os.Remove(filepath.Join(pending, "prevent-removal")))
				require.NoError(t, os.Remove(pending))
				require.NoError(t, os.Rename(saved, pending))
			}
			rejectOpen(t, dir, true)
			require.NoError(t, st.Reconcile())
			_, err = st.Append("native", []events.SessionEvent{fact})
			require.NoError(t, err)
			require.Equal(t, committed, readBytes(t, logPath(dir)))
			page, err := st.ListHistory("native", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			require.Equal(t, fact.EventID, *page.SyncedEventID)
		})
	}
}

func TestSilentSQLTriggersCannotAcknowledgeInconsistentProjection(t *testing.T) {
	for _, fault := range []string{"ignore", "progress", "payload", "sql-only"} {
		t.Run(fault, func(t *testing.T) {
			st, dir := open(t)
			old := event(t, "native", "old", events.AgentMessage{MessageID: "old", Content: []events.ContentBlock{events.Text("committed old")}})
			_, err := st.Append("native", []events.SessionEvent{old})
			require.NoError(t, err)
			fact := event(t, "native", "new", events.AgentMessage{MessageID: "new", Content: []events.ContentBlock{events.Text("new original")}})
			statements := map[string]string{
				"ignore":   `CREATE TRIGGER silent BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(IGNORE); END`,
				"progress": `CREATE TRIGGER silent AFTER UPDATE ON sync_progress BEGIN UPDATE sync_progress SET event_id=NULL; END`,
				"payload":  `CREATE TRIGGER silent AFTER UPDATE ON sync_progress BEGIN UPDATE event SET payload=replace(CAST(payload AS TEXT),'new original','new modified'); END`,
				"sql-only": `CREATE TRIGGER silent AFTER UPDATE ON sync_progress BEGIN INSERT INTO event SELECT session_id,position+1000,'ghost',type,payload FROM event WHERE position=2; END`,
			}
			db := diskSQL(t, dir)
			_, err = db.ExecContext(t.Context(), statements[fault])
			require.NoError(t, err)
			_, err = st.Append("native", []events.SessionEvent{fact})
			require.ErrorContains(t, err, "outcome_unknown")
			committed := readBytes(t, logPath(dir))
			require.Contains(t, string(committed), fact.EventID)
			_, err = st.ListHistory("native", store.HistoryQuery{})
			require.ErrorContains(t, err, "outcome_unknown")
			require.NoError(t, db.Close())
			require.NoError(t, st.Reconcile())
			_, err = st.Append("native", []events.SessionEvent{fact})
			require.NoError(t, err)
			require.Equal(t, committed, readBytes(t, logPath(dir)))
			page, err := st.ListHistory("native", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			require.Equal(t, fact.EventID, *page.SyncedEventID)
		})
	}
}
