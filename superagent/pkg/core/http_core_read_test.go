package core

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func startCoreResourceFixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "core.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), schema+";"+traceAssociationSchema)
	require.NoError(t, err)
	s := &Server{store: &Store{db: db, hasTraceAssociations: true, telemetry: newCoreTelemetry(TelemetryOptions{})}}
	httpServer := httptest.NewUnstartedServer(nil)
	s.listener = httpServer.Listener
	httpServer.Config.Handler = s.guardHTTP(s.newHTTPAPI().router)
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	return s, httpServer
}

func resourceRequest(t *testing.T, endpoint, method, path string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint+path, http.NoBody)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, res.Header, body
}

func TestThreadResourceEscapedKey(t *testing.T) {
	s, server := startCoreResourceFixture(t)
	const key = "https://example.test/projects/a%2Fb/name?cursor=10%25&N=中文#marker"
	_, err := s.store.db.ExecContext(t.Context(), "INSERT INTO threads(key,source,native_id,created_at,last_message_at) VALUES(?,'fixture','native',?,'')", key, now())
	require.NoError(t, err)
	backend, err := url.Parse(server.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(backend) }})
	t.Cleanup(proxy.Close)
	path := "/v1/core/threads/" + url.PathEscape(key)
	for _, endpoint := range []string{server.URL, proxy.URL} {
		status, header, body := resourceRequest(t, endpoint, http.MethodGet, path)
		require.Equal(t, http.StatusOK, status, "%s", body)
		require.Equal(t, "application/json", header.Get("Content-Type"))
		var found Thread
		require.NoError(t, json.Unmarshal(body, &found))
		require.Equal(t, key, found.Key)
		status, _, body = resourceRequest(t, endpoint, http.MethodHead, path)
		require.Equal(t, http.StatusOK, status)
		require.Empty(t, body)
	}
	status, header, _ := resourceRequest(t, server.URL, http.MethodPost, path)
	require.Equal(t, http.StatusMethodNotAllowed, status)
	require.ElementsMatch(t, []string{http.MethodGet, http.MethodHead}, header.Values("Allow"))
}

func TestInternalEventResourceMessageFilter(t *testing.T) {
	s, server := startCoreResourceFixture(t)
	_, err := s.store.db.ExecContext(t.Context(), `
INSERT INTO topics(created_at,body,creation_key,creation_digest) VALUES('2026-01-01','{"title":"Saved Topic"}','fixture','');
INSERT INTO internal_events(event_id,kind,sender,recipient,body,refs,created_at,status) VALUES
 ('original','routed_message','{"kind":"router"}','{"kind":"topic","topic_id":"mass-t1"}','{"text":"delivery"}',
 '[{"kind":"trigger_message","id":"message-1"}]','2026-01-02','sent'),
 ('returned','route_request','{"kind":"topic","topic_id":"mass-t1"}','{"kind":"router"}','{"reason":"re-route"}',
 '[{"kind":"trigger_message","id":"message-1"},{"kind":"internal_event","id":"original"}]','2026-01-03','pending'),
 ('independent','context_supplement','{"kind":"core"}','{"kind":"router"}',NULL,'[]','2026-01-04','pending');
INSERT INTO trace_associations VALUES('internal-events','returned','enqueue',0,'00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01','','2026-01-03');`)
	require.NoError(t, err)
	const collection = "/v1/core/internal-events"
	const filter = "?messageId=message-1&limit=1&order=newest&preview=true&trace=true"
	status, _, body := resourceRequest(t, server.URL, http.MethodGet, collection+filter)
	require.Equal(t, http.StatusOK, status, "%s", body)
	var first Page
	require.NoError(t, json.Unmarshal(body, &first))
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	require.Contains(t, string(first.Items[0]), `"event_id":"returned"`)
	require.Contains(t, string(first.Items[0]), `"topicTitle":"Saved Topic"`)
	require.Contains(t, string(first.Items[0]), `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`)

	status, _, body = resourceRequest(t, server.URL, http.MethodGet, collection+filter+"&cursor="+url.QueryEscape(first.NextCursor))
	require.Equal(t, http.StatusOK, status, "%s", body)
	var second Page
	require.NoError(t, json.Unmarshal(body, &second))
	require.Len(t, second.Items, 1)
	require.Empty(t, second.NextCursor)
	require.Contains(t, string(second.Items[0]), `"event_id":"original"`)

	status, _, body = resourceRequest(t, server.URL, http.MethodGet, collection+"/returned?trace=true")
	require.Equal(t, http.StatusOK, status, "%s", body)
	require.Contains(t, string(body), `"topicId":"mass-t1"`)
	require.Contains(t, string(body), `"returnedEventId":"original"`)
	require.Contains(t, string(body), `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`)
	status, _, body = resourceRequest(t, server.URL, http.MethodGet, collection+"/original")
	require.Equal(t, http.StatusOK, status, "%s", body)
	require.Contains(t, string(body), `"returned":true`)
	status, _, body = resourceRequest(t, server.URL, http.MethodGet, collection)
	require.Equal(t, http.StatusOK, status, "%s", body)
	require.Contains(t, string(body), `"event_id":"independent"`)
	status, _, body = resourceRequest(t, server.URL, http.MethodGet, collection+strings.Replace(filter, "message-1", "message-2", 1)+"&cursor="+first.NextCursor)
	require.Equal(t, http.StatusBadRequest, status, "%s", body)
	status, _, body = resourceRequest(t, server.URL, http.MethodHead, collection+"?limit=bad")
	require.Equal(t, http.StatusBadRequest, status)
	require.Empty(t, body)
}
