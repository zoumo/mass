package integration_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func (s *entrypointSuite) TestTicket05CloseResumeAndLegacyArchiveMigration() {
	t := s.T()
	ref, _, _ := s.open("codex", "close-resume")
	s.ticket05Input(ref, "hello")

	var closed resource.Session
	s.call("session.close", workflowParams{Session: ref}, &closed)
	s.Equal(events.SessionState("offline"), closed.Status.State)
	s.Equal(ref.SessionID, closed.Status.SessionID)

	var runtimeAfterClose resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeAfterClose)
	s.Equal("running", runtimeAfterClose.Status.State, "close must not stop the owning Runtime")

	raw, err := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "-o", "json", "session", "prompt", ref.SessionID, "--text", "blocked").CombinedOutput()
	require.Error(t, err, "prompt after close must fail")
	require.Contains(t, string(raw), `"reason":"session_resume_required"`)
	require.Contains(t, string(raw), "resume")

	raw, err = exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "-o", "json", "session", "archive", ref.SessionID).CombinedOutput()
	require.Error(t, err, "archive command must not exist")
	require.NotEmpty(t, raw)

	req, err := http.NewRequestWithContext(ctx(s.T()), http.MethodPost, s.url+"/apis/v1/sessions/"+ref.SessionID+"/archive", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusNotFound, response.StatusCode)

	err = workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.list", workflowParams{SessionQuery: resource.SessionQuery{State: "archived"}}, nil)
	var public *transport.Error
	require.ErrorAs(t, err, &public)
	require.Equal(t, "invalid_argument", public.Code)

	var resumed resource.Session
	s.call("session.resume", workflowParams{Session: ref}, &resumed)
	s.Equal(ref.SessionID, resumed.Status.SessionID)
	s.Equal(closed.Metadata.UID, resumed.Metadata.UID)
	s.ticket05Input(ref, "continue")
	s.call("session.close", workflowParams{Session: ref}, &closed)
	s.Equal(events.SessionState("offline"), closed.Status.State)

	probe := filepath.Join(s.data, "must-not-activate")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\n: > \"$0.called\"\nexit 79\n"), 0o700))
	runtimeAfterClose.Spec.Config["executable"] = probe
	s.call("runtime.update", workflowParams{Name: "agent", Spec: &runtimeAfterClose.Spec}, nil)

	history := s.history(ref)
	s.stopForHistory()
	legacy := readPersistedSession(s.T(), s.data, s.backend, ref.SessionID)
	legacy.Status.State = "archived"
	legacy.Status.Reason = "legacy archive marker"
	writeLegacyArchived(s.T(), s.data, s.backend, legacy)
	s.startForHistory()

	var migrated resource.Session
	s.call("session.get", workflowParams{Session: ref}, &migrated)
	s.Equal(events.SessionState("offline"), migrated.Status.State)
	s.Empty(migrated.Status.Reason)
	s.Equal(legacy.Metadata.UID, migrated.Metadata.UID)
	s.Equal(legacy.Metadata.CreatedAt, migrated.Metadata.CreatedAt)
	s.Equal(legacy.Metadata.Generation, migrated.Metadata.Generation)
	s.Equal(legacy.Spec, migrated.Spec)
	s.Equal(legacy.Status.SessionID, migrated.Status.SessionID)
	s.NotEqual(legacy.Metadata.ResourceVersion, migrated.Metadata.ResourceVersion)
	s.Equal(history, s.history(ref), "migration must preserve committed history and synchronization position")

	var runtimeAfterRestart resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeAfterRestart)
	s.Equal("stopped", runtimeAfterRestart.Status.State)
	_, err = os.Stat(probe + ".called")
	s.True(os.IsNotExist(err), "startup migration must not activate the Runtime or Session")

	err = workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.list", workflowParams{SessionQuery: resource.SessionQuery{State: "archived"}}, nil)
	require.ErrorAs(t, err, &public)
	s.Equal("invalid_argument", public.Code)
}

func (s *entrypointSuite) ticket05Input(ref resource.SessionRef, text string) {
	before := len(s.history(ref).Events)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, nil)
	s.Require().Eventually(func() bool {
		var current resource.Session
		s.call("session.get", workflowParams{Session: ref}, &current)
		return current.Status.State == events.Idle && len(s.history(ref).Events) > before
	}, 5*time.Second, 10*time.Millisecond)
}

func readPersistedSession(t *testing.T, data, backendName, id string) resource.Session {
	var payload []byte
	if backendName == "file" {
		var err error
		payload, err = os.ReadFile(filepath.Join(data, "sessions", "demo", "session", "state.json"))
		require.NoError(t, err)
	} else {
		db, err := sql.Open("sqlite", filepath.Join(data, "metadata-authority.sqlite"))
		require.NoError(t, err)
		defer func() { require.NoError(t, db.Close()) }()
		require.NoError(t, db.QueryRowContext(t.Context(), "SELECT payload FROM session WHERE session_id=?", id).Scan(&payload))
	}
	var persisted resource.Session
	require.NoError(t, json.Unmarshal(payload, &persisted))
	return persisted
}

func writeLegacyArchived(t *testing.T, data, backendName string, legacy resource.Session) {
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	if backendName == "file" {
		require.NoError(t, os.WriteFile(filepath.Join(data, "sessions", "demo", "session", "state.json"), payload, 0o600))
		return
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "metadata-authority.sqlite"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	result, err := db.ExecContext(t.Context(), "UPDATE session SET state='archived', payload=? WHERE session_id=?", payload, legacy.Status.SessionID)
	require.NoError(t, err)
	count, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
}
