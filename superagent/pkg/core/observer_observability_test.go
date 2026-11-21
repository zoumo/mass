package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObserverMCPMetricsCallerScope(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observer.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()
	_, err = db.ExecContext(ctx, schema+";"+mcpUsageSchema+traceAssociationSchema)
	require.NoError(t, err)
	s := &Store{db: db, hasTraceAssociations: true}
	traceID := "db64663e60fb444b8a93ea9c06c24413"
	at := time.Now().UTC().Add(-time.Minute).Format(utcTimestamp)
	_, err = db.ExecContext(ctx, `INSERT INTO trace_associations VALUES('messages','mass-m1','admission',0,?,'',?)`, "00-"+traceID+"-5b147a1058d5e09f-01", at)
	require.NoError(t, err)
	insert := func(role, owner, bindingID, traceID, outcome string, duration int, replayed bool) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO mcp_tool_calls
 (at,caller_role,caller_id,topic_id,issue_id,session_binding_id,target_issue_id,event_id,tool,action,outcome,duration_ns,trace_id,span_id,replayed,changed)
 VALUES(?,?,?,'mass-t1','',?,'','','update_issue','edit',?,?,?,'',?,0)`, at, role, owner, bindingID, outcome, duration, traceID, replayed)
		require.NoError(t, err)
	}
	for duration := 1; duration <= 20; duration++ {
		insert("topic", "mass-t1", "topic-binding", traceID, "ok", duration, duration == 1)
	}
	insert("issue", "mass-t1-i1", "issue-binding", "", "conflict", 100, false)
	metrics := func(query string) mcpMetrics {
		t.Helper()
		q, err := url.ParseQuery(query)
		require.NoError(t, err)
		value, err := s.mcpToolUsage(ctx, q, "metrics")
		require.NoError(t, err)
		return value.(mcpMetrics)
	}
	topic := metrics("role=topic&callerId=mass-t1")
	require.Equal(t, mcpMetricStats{Count: 20, Replayed: 1, DurationNS: 210, P95NS: 19}, topic.Total)
	require.Len(t, topic.Tools, 1)
	require.Len(t, topic.Trend, 1)
	require.Equal(t, topic.Total.Count, topic.Trend[0].Count)
	issue := metrics("role=issue&callerId=mass-t1-i1")
	require.Equal(t, mcpMetricStats{Count: 1, Failed: 1, DurationNS: 100, P95NS: 100}, issue.Total)
	require.Equal(t, issue.Total, metrics("sessionBindingId=issue-binding").Total)
	require.EqualValues(t, 21, metrics("topicId=mass-t1").Total.Count)
	require.EqualValues(t, 20, metrics("messageId=mass-m1").Total.Count)
	require.Empty(t, metrics("messageId=unknown").Tools)
}

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
	reader, err := newObserverTraceReader(&ObserverTraceConfig{Endpoint: server.URL})
	require.NoError(t, err)
	t.Cleanup(reader.client.CloseIdleConnections)
	result, err := reader.read(t.Context(), id, nil)
	require.NoError(t, err)
	require.Len(t, result.Spans, 2)
	require.Equal(t, spanID, result.Spans[1].ParentID)
	require.EqualValues(t, 137, result.Spans[1].OffsetUS)
	require.EqualValues(t, 42, result.Spans[1].DurationUS)
	body, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(body), "secret")
	_, err = reader.read(t.Context(), "invalid", nil)
	require.Error(t, err)
}

func TestObserverIssueTracePreservesReports(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observer.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()
	_, err = db.ExecContext(ctx, schema+";"+mcpUsageSchema+traceAssociationSchema)
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
