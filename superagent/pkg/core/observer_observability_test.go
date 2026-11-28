package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObserverTraceReadProjectsKnownFields(t *testing.T) {
	id := "db64663e60fb444b8a93ea9c06c24413"
	spanID, childID := "5b147a1058d5e09f", "6a147a1058d5e09f"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.String() != "/api/default/_search?type=traces" {
			t.Error("unexpected trace query route")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct{ Query struct{ SQL string } }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Query.SQL != fmt.Sprintf(`SELECT * FROM "default" WHERE trace_id = '%s' ORDER BY start_time,span_id`, id) {
			t.Error("unexpected trace search query")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"hits":[
 {"trace_id":%q,"span_id":%q,"operation_name":"request","start_time":1795196579345675000,"duration":1000},
 {"trace_id":%q,"span_id":%q,"reference_parent_span_id":%q,"operation_name":"update_issue","start_time":1795196579345812000,"duration":42,"superagent_role":"issue","private_input":"secret"},
 {"trace_id":"ffffffffffffffffffffffffffffffff","span_id":%q,"start_time":1795196579345812000,"duration":10}
 ]}`, id, spanID, id, childID, spanID, childID)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "core.yaml")
	data := fmt.Sprintf(`openobserve:
  endpoint: %q
`, server.URL)
	require.NoError(t, os.WriteFile(configPath, []byte(data), 0o600))
	config, err := LoadConfig(configPath)
	require.NoError(t, err)
	reader, err := newObserverTraceReader(config.OpenObserve)
	require.NoError(t, err)
	require.NotNil(t, reader)
	t.Cleanup(reader.client.CloseIdleConnections)
	s, httpServer := startCoreResourceFixture(t)
	s.observerTraces = reader
	path := telemetryTracePath + "/" + id
	status, _, body := resourceRequest(t, httpServer.URL, http.MethodGet, path)
	require.Equal(t, http.StatusOK, status, "%s", body)
	var result telemetryTrace
	require.NoError(t, json.Unmarshal(body, &result))
	require.Len(t, result.Spans, 2)
	require.Equal(t, spanID, result.Spans[1].ParentID)
	require.InDelta(t, 137, result.Spans[1].OffsetUS, 0)
	require.EqualValues(t, 42, result.Spans[1].DurationUS)
	require.NotContains(t, string(body), "secret")
	status, _, body = resourceRequest(t, httpServer.URL, http.MethodHead, path)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body)
	status, _, body = resourceRequest(t, httpServer.URL, http.MethodGet, path+"?at=invalid")
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, string(body), "invalid Trace time anchor")
}

func TestObserverIssueTracePreservesReports(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observer.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()
	_, err = db.ExecContext(ctx, schema+";"+traceAssociationSchema)
	require.NoError(t, err)
	s := &Store{db: db, hasTraceAssociations: true}
	at := now()
	_, err = db.ExecContext(ctx, `INSERT INTO topics(created_at,body,creation_key,creation_digest) VALUES(?,'{}','topic','')`, at)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO issues(topic_id,number,created_at,body,creation_key,creation_digest) VALUES
 ('mass-t1',1,?,json_object('reports',json_array(json_object('id','mass-t1-i1-r1','eventId','report-event','summary','Report evidence'))),'issue','')`, at)
	require.NoError(t, err)
	traceID := "db64663e60fb444b8a93ea9c06c24413"
	_, err = db.ExecContext(ctx, `INSERT INTO trace_associations VALUES('internal-events','report-event','enqueue',0,?,'',?)`, "00-"+traceID+"-5b147a1058d5e09f-01", at)
	require.NoError(t, err)
	body, err := s.getRecord(ctx, "issues", "mass-t1-i1", nil, nil, true)
	require.NoError(t, err)
	var result struct {
		Reports []struct {
			Summary           string
			TraceAssociations []struct{ TraceID string }
		}
	}
	require.NoError(t, json.Unmarshal(body, &result))
	require.Len(t, result.Reports, 1)
	require.Equal(t, "Report evidence", result.Reports[0].Summary)
	require.Len(t, result.Reports[0].TraceAssociations, 1)
	require.Equal(t, traceID, result.Reports[0].TraceAssociations[0].TraceID)
}
