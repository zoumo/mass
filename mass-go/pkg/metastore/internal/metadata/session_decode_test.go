package metadata

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/internal/fileio"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

func TestOpenSessionWithRemovedTurnEvidence(t *testing.T) {
	for _, mode := range []string{"sqlite", "file", "file pending commit"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			open := OpenFile
			if mode == "sqlite" {
				open = OpenSQLite
			}
			s, err := open(dir, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			session, err := s.CreateSession(resource.Session{
				Metadata: resource.Metadata{Namespace: "one", Name: "original"},
				Spec:     resource.SessionSpec{RuntimeRef: "codex"},
				Status:   resource.SessionStatus{SessionID: "original", State: events.Unknown},
			})
			require.NoError(t, err)
			path := filepath.Join(dir, "sessions", "one", "original", "state.json")
			legacy := []byte(`"reconciledTurn":{"turnId":"turn-original","status":"completed"},"sessionId":"original"`)
			sessionID := []byte(`"sessionId":"original"`)
			var record any = session
			if mode == "file pending commit" {
				session.Status.Reason = "session_observation_lost"
				next := s.data
				next.Sessions = map[string]resource.Session{"one/original": session}
				record = metadataIntent{Format: 1, Next: next, Created: map[string]bool{}}
			}
			payload, err := json.Marshal(record)
			require.NoError(t, err)
			require.Equal(t, 1, bytes.Count(payload, sessionID))
			payload = bytes.Replace(payload, sessionID, legacy, 1)
			pending := filepath.Join(dir, ".pending.json")
			switch mode {
			case "sqlite":
				_, err = s.sql.conn.ExecContext(t.Context(), "UPDATE session SET payload=? WHERE session_id=?", payload, session.Status.SessionID)
			case "file":
				err = fileio.AtomicBytes(path, payload)
			case "file pending commit":
				err = fileio.AtomicBytes(pending, payload)
			}
			require.NoError(t, err)
			require.NoError(t, s.Close())

			reopened, err := open(dir, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			ref := resource.SessionRef{SessionID: session.Status.SessionID}
			got, err := reopened.GetSession(ref)
			require.NoError(t, err)
			require.Equal(t, session, got)
			jsonResponse, err := json.Marshal(got)
			require.NoError(t, err)
			require.NotContains(t, string(jsonResponse), `"reconciledTurn"`)
			if mode == "file pending commit" {
				require.NoFileExists(t, pending)
				body, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NotContains(t, string(body), `"reconciledTurn"`)
			}

			got.Status.State = "offline"
			got.Status.Reason = ""
			updated, err := reopened.UpdateSession(got)
			require.NoError(t, err)
			require.Equal(t, got.Status, updated.Status)
			var rewritten []byte
			if mode == "sqlite" {
				err = reopened.sql.conn.QueryRowContext(t.Context(), "SELECT payload FROM session WHERE session_id=?", session.Status.SessionID).Scan(&rewritten)
			} else {
				rewritten, err = os.ReadFile(path)
			}
			require.NoError(t, err)
			require.NotContains(t, string(rewritten), `"reconciledTurn"`)
		})
	}
}

func TestStoredSessionRejectsOtherUnknownFields(t *testing.T) {
	var session storedSession
	err := fileio.Decode([]byte(`{"status":{"reconciledTurn":null,"unexpected":true}}`), &session)
	require.ErrorContains(t, err, `unknown field "unexpected"`)
}
