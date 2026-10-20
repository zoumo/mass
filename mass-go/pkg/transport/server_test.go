package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type ServerSuite struct{ suite.Suite }

// This fixture verifies protocol dispatch, not native Agent behavior.
type recordingAPI struct {
	transport.API
	calls        []string
	ref          resource.SessionRef
	answer       any
	input        runtime.Input
	history      store.HistoryQuery
	sessionQuery resource.SessionQuery
	err          error
	start        func(context.Context)
}

func (a *recordingAPI) record(op string) { a.calls = append(a.calls, op) }
func (a *recordingAPI) CreateRuntime(v resource.Runtime) (resource.Runtime, error) {
	a.record("runtime.create")
	v.Status.State = "stopped"
	return v, a.err
}

func (a *recordingAPI) GetRuntime(name string) (resource.Runtime, error) {
	a.record("runtime.get")
	return resource.Runtime{Metadata: resource.Metadata{Name: name}}, a.err
}

func (a *recordingAPI) ListRuntimes(resource.RuntimeQuery) ([]resource.Runtime, error) {
	a.record("runtime.list")
	return []resource.Runtime{}, a.err
}

func (a *recordingAPI) UpdateRuntime(name string, spec resource.RuntimeSpec) (resource.Runtime, error) {
	a.record("runtime.update")
	return resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: spec}, a.err
}

func (a *recordingAPI) StartRuntime(ctx context.Context, name string) (resource.Runtime, error) {
	a.record("runtime.start")
	if a.start != nil {
		a.start(ctx)
	}
	return resource.Runtime{Metadata: resource.Metadata{Name: name}}, a.err
}

func (a *recordingAPI) StopRuntime(context.Context, string) (resource.Runtime, error) {
	a.record("runtime.stop")
	return resource.Runtime{}, a.err
}
func (a *recordingAPI) DeleteRuntime(string) error { a.record("runtime.delete"); return a.err }
func (a *recordingAPI) CreateSession(ctx context.Context, v resource.Session) (resource.Session, error) {
	a.record("session.create")
	v.Status.SessionID = "a/b"
	return v, a.err
}

func (a *recordingAPI) ResolveSession(namespace, nameOrID string) (resource.SessionRef, error) {
	return resource.SessionRef{SessionID: "a/b"}, a.err
}

func (a *recordingAPI) GetSession(ref resource.SessionRef) (resource.Session, error) {
	a.record("session.get")
	a.ref = ref
	return resource.Session{Status: resource.SessionStatus{SessionID: ref.SessionID}}, a.err
}

func (a *recordingAPI) ListSessions(q resource.SessionQuery) (resource.SessionPage, error) {
	a.record("session.list")
	a.sessionQuery = q
	return resource.SessionPage{Items: []resource.Session{}}, a.err
}

func (a *recordingAPI) ResumeSession(context.Context, resource.SessionRef) (resource.Session, error) {
	a.record("session.resume")
	return resource.Session{}, a.err
}

func (a *recordingAPI) ArchiveSession(context.Context, resource.SessionRef) (resource.Session, error) {
	a.record("session.archive")
	return resource.Session{}, a.err
}

func (a *recordingAPI) Cancel(context.Context, resource.SessionRef) (runtime.CancelReceipt, error) {
	a.record("session.cancel")
	return runtime.CancelReceipt{Result: "noActiveWork"}, a.err
}

func (a *recordingAPI) SendPrompt(ctx context.Context, ref resource.SessionRef, input runtime.Input) (runtime.InputReceipt, error) {
	a.record("session.input")
	a.ref = ref
	a.input = input
	return runtime.InputReceipt{Submission: "submission", Acceptance: runtime.Acceptance{NativeAccepted: runtime.Yes()}}, a.err
}

func (a *recordingAPI) AnswerInteraction(ctx context.Context, ref resource.SessionRef, id string, answer any) (runtime.InteractionReceipt, error) {
	a.record("session.answer")
	a.ref = ref
	a.answer = answer
	return runtime.InteractionReceipt{Interaction: id, Evidence: runtime.Acceptance{Sent: runtime.Yes()}}, a.err
}

func (a *recordingAPI) ListHistoryEvents(ref resource.SessionRef, q store.HistoryQuery) (store.HistoryPage, error) {
	a.record("session.history")
	a.ref = ref
	a.history = q
	return store.HistoryPage{Events: []events.SessionEvent{}}, a.err
}

func (a *recordingAPI) SubscribeEvents(resource.SessionRef) (*service.Subscription, error) {
	a.record("session.observe")
	return nil, store.ErrNotFound
}

func server(t *testing.T, a *recordingAPI) *transport.Server {
	t.Helper()
	s, err := transport.New(a, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	t.Cleanup(s.Shutdown)
	return s
}

func request(s http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func (serverSuite *ServerSuite) TestRESTRoutes() {
	rows := []struct {
		method, path, body, op string
		status                 int
	}{
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}}}`, "runtime.create", 201},
		{"GET", "/apis/v1/runtimes?state=stopped&type=codex", "", "runtime.list", 200},
		{"GET", "/apis/v1/runtimes/agent", "", "runtime.get", 200},
		{"PUT", "/apis/v1/runtimes/agent", `{"type":"claude","config":{"large":9007199254740993}}`, "runtime.update", 200},
		{"POST", "/apis/v1/runtimes/agent/start", "", "runtime.start", 200},
		{"POST", "/apis/v1/runtimes/agent/stop", "{}", "runtime.stop", 200},
		{"DELETE", "/apis/v1/runtimes/agent", "", "runtime.delete", 204},
		{"POST", "/apis/v1/sessions", `{"metadata":{"namespace":"demo","name":"task"},"spec":{"runtimeRef":"agent","cwd":"/remote/path"}}`, "session.create", 201},
		{"GET", "/apis/v1/sessions?namespace=demo&runtimeRef=agent&state=archived&limit=2&after=opaque", "", "session.list", 200},
	}
	for _, row := range rows {
		serverSuite.Run(row.op, func() {
			t := serverSuite.T()
			a := &recordingAPI{}
			w := request(server(t, a), row.method, row.path, row.body)
			require.Equal(t, row.status, w.Code, w.Body.String())
			require.Equal(t, []string{row.op}, a.calls)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if row.status == 204 {
				require.Empty(t, w.Body.String())
			} else {
				var v map[string]json.RawMessage
				if row.op != "runtime.list" {
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v))
					require.NotContains(t, v, "result")
					require.NotContains(t, v, "data")
				}
			}
			if row.op == "runtime.create" {
				require.Equal(t, "/apis/v1/runtimes/agent", w.Header().Get("Location"))
			}
			if row.op == "session.create" {
				require.Equal(t, "/apis/v1/sessions/a%2Fb", w.Header().Get("Location"))
			}
			if row.op == "runtime.update" {
				require.Contains(t, w.Body.String(), "9007199254740993")
			}
			if row.op == "session.list" {
				require.Equal(t, 2, a.sessionQuery.Limit)
				require.Equal(t, "opaque", a.sessionQuery.After)
			}
		})
	}
	for _, base := range []string{"/apis/v1/sessions/a%2Fb", "/apis/v1/namespaces/demo/sessions/task"} {
		for _, row := range []struct {
			method, suffix, body, op string
			status                   int
		}{
			{"GET", "", "", "session.get", 200},
			{"POST", "/resume", "{}", "session.resume", 200},
			{"POST", "/archive", "", "session.archive", 200},
			{"POST", "/cancel", "", "session.cancel", 200},
			{"POST", "/inputs", `{"content":[{"type":"text","text":""}]}`, "session.input", 200},
			{"POST", "/interactions/Li4/answer", `null`, "session.answer", 200},
			{"GET", "/events?types=agent.message&types=agent.tool_result&limit=2&after=opaque", "", "session.history", 200},
			{"GET", "/events/stream", "", "session.observe", 404},
		} {
			serverSuite.Run(base+row.suffix, func() {
				t := serverSuite.T()
				a := &recordingAPI{}
				w := request(server(t, a), row.method, base+row.suffix, row.body)
				require.Equal(t, row.status, w.Code, w.Body.String())
				require.Equal(t, []string{row.op}, a.calls)
				if row.op == "session.answer" {
					require.Nil(t, a.answer)
					require.Contains(t, w.Body.String(), `"interaction":".."`)
					require.Contains(t, w.Body.String(), `"evidence"`)
				}
				if row.op == "session.history" {
					require.Equal(t, []string{"agent.message", "agent.tool_result"}, a.history.Types)
					require.Equal(t, "opaque", a.history.After)
				}
			})
		}
	}
}

func (serverSuite *ServerSuite) TestStrictHTTPBoundary() {
	rows := []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/apis/v1/runtimes", `null`, 400},
		{"POST", "/apis/v1/runtimes", `{} {}`, 400},
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent","uid":""},"spec":{"type":"codex","config":{}}}`, 400},
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{"x":1,"x":2}}}`, 400},
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":"codex"}}`, 400},
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":null,"config":{}}}`, 400},
		{"POST", "/apis/v1/runtimes", `{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}} ,"status":{}}`, 400},
		{"GET", "/apis/v1/runtimes?state=bad", "", 400},
		{"GET", "/apis/v1/runtimes?type=x&type=y", "", 400},
		{"GET", "/apis/v1/sessions?sessionId=x", "", 400},
		{"GET", "/apis/v1/sessions?limit=0", "", 400},
		{"GET", "/apis/v1/sessions?limit=+1", "", 400},
		{"GET", "/apis/v1/sessions?unknown=x", "", 400},
		{"GET", "/apis/v1/sessions/a%2Fb/events?types=", "", 400},
		{"GET", "/apis/v1/sessions/a%2Fb/events?types=bad", "", 400},
		{"GET", "/apis/v1/sessions/a%2Fb/events?types[]=agent.message", "", 400},
		{"GET", "/apis/v1/sessions/a%2Fb/events?limit=1&limit=2", "", 400},
		{"GET", "/apis/v1/sessions/a%2Fb/events?after=", "", 400},
		{"GET", "/apis/v1/sessions/%2E%2E", "", 400},
		{"GET", "/apis/v1/sessions/%2E", "", 400},
		{"GET", "/apis/v1/sessions/%FF", "", 400},
		{"GET", "/apis/v1/runtimes/agent", "{}", 400},
		{"DELETE", "/apis/v1/runtimes/agent", "{}", 400},
		{"POST", "/apis/v1/runtimes/agent/start", "null", 400},
		{"PATCH", "/apis/v1/runtimes/agent", "{}", 405},
		{"HEAD", "/apis/v1/runtimes", "", 405},
		{"DELETE", "/apis/v1/sessions/a%2Fb", "", 405},
		{"POST", "/experimental/operations/runtime.list", "{}", 404},
		{"POST", "/apis/v1/sessions/a%2Fb/inputs", `{"content":[{"type":"text","text":"x","extra":true}]}`, 400},
		{"POST", "/apis/v1/sessions/a%2Fb/inputs", `{"content":[{"type":"image","mimeType":"image/png"}]}`, 400},
		{"POST", "/apis/v1/sessions/a%2Fb/inputs", `{"content":[{"type":"image","mimeType":"image/png","data":"!invalid"}]}`, 400},
		{"POST", "/apis/v1/sessions/a%2Fb/inputs", `{"content":[{"type":"resource","resource":{"uri":"file:///a","blob":"!invalid"}}]}`, 400},
	}
	for _, row := range rows {
		serverSuite.Run(row.method+row.path+row.body, func() {
			t := serverSuite.T()
			a := &recordingAPI{}
			w := request(server(t, a), row.method, row.path, row.body)
			require.Equal(t, row.status, w.Code, w.Body.String())
			require.Empty(t, a.calls, "invalid requests must not dispatch")
			var e transport.Error
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &e))
			require.NotEmpty(t, e.Code)
			if row.status == 405 {
				require.NotEmpty(t, w.Header().Get("Allow"))
			}
		})
	}
}

func (serverSuite *ServerSuite) TestAuthenticationMediaAndProbes() {
	t := serverSuite.T()
	for _, row := range []struct {
		path, token, accept, content, key, lastID string
		status                                    int
	}{
		{path: "/apis/v1/runtimes", status: 401},
		{path: "/not-found", token: "bad", status: 401},
		{path: "/apis/v1/sessions/a%2Fb/events/stream", status: 401},
		{path: "/apis/v1/runtimes", token: "test-token", accept: "text/plain", status: 406},
		{path: "/apis/v1/runtimes", token: "test-token", accept: "application/json;q=0, */*;q=1", status: 406},
		{path: "/apis/v1/runtimes", token: "test-token", key: "unsupported", status: 400},
		{path: "/apis/v1/sessions/a%2Fb/events/stream", token: "test-token", lastID: "event", status: 400},
		{path: "/livez", status: 200},
		{path: "/readyz", status: 200},
	} {
		a := &recordingAPI{}
		s := server(t, a)
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, row.path, nil)
		if row.token != "" {
			r.Header.Set("Authorization", "Bearer "+row.token)
		}
		r.Header.Set("Accept", row.accept)
		if row.key != "" {
			r.Header.Set("Idempotency-Key", row.key)
		}
		r.Header.Set("Last-Event-ID", row.lastID)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, row.status, w.Code, w.Body.String())
		require.Empty(t, a.calls)
		if row.status == 401 {
			require.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
		}
	}
	a := &recordingAPI{}
	s := server(t, a)
	for _, row := range []struct {
		body, content string
		status        int
	}{{`{}`, "text/plain", 415}, {strings.Repeat("x", (1<<20)+1), "application/json", 400}} {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/apis/v1/runtimes", strings.NewReader(row.body))
		r.Header.Set("Authorization", "Bearer test-token")
		r.Header.Set("Content-Type", row.content)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, row.status, w.Code)
	}
	s.Shutdown()
	require.Equal(t, 503, request(s, "GET", "/readyz", "").Code)
	require.Equal(t, 200, request(s, "GET", "/livez", "").Code)
	require.Equal(t, 405, request(s, "POST", "/readyz", "").Code)
	require.Equal(t, 503, request(s, "GET", "/apis/v1/runtimes", "").Code)
	_, err := transport.New(a, transport.Options{})
	require.Error(t, err)
	_, err = transport.New(a, transport.Options{LocalOnly: true, Token: "token"})
	require.Error(t, err)
	local, err := transport.New(a, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	defer local.Shutdown()
	require.Equal(t, 401, request(local, "GET", "/apis/v1/runtimes", "").Code)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/apis/v1/runtimes", nil)
	r.RemoteAddr = "127.0.0.1:10"
	w := httptest.NewRecorder()
	local.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code)
}

func (serverSuite *ServerSuite) TestOpaqueRefsAndAnswers() {
	t := serverSuite.T()
	for _, id := range []string{"a/b", "a%2Fb", "a..b", " %/雪"} {
		a := &recordingAPI{}
		base := "/apis/v1/sessions/" + url.PathEscape(id)
		w := request(server(t, a), "GET", base, "")
		require.Equal(t, 200, w.Code)
		require.Equal(t, id, a.ref.SessionID)
	}
	for _, body := range []string{`null`, `false`, `0`, `"answer"`, `[]`, `{"number":9007199254740993}`} {
		a := &recordingAPI{}
		w := request(server(t, a), "POST", "/apis/v1/sessions/a%2Fb/interactions/Li4/answer", body)
		require.Equal(t, 200, w.Code, w.Body.String())
		if strings.Contains(body, "number") {
			require.Equal(t, json.Number("9007199254740993"), a.answer.(map[string]any)["number"])
		}
	}
	for _, body := range []string{`{"content":[{"type":"image","data":"YQ==","mimeType":"image/png"}]}`, `{"content":[{"type":"audio","data":"YQ==","mimeType":"audio/wav"}]}`, `{"content":[{"type":"resource","resource":{"uri":"file:///a","text":""}}]}`, `{"content":[{"type":"resource_link","uri":"file:///a","name":"a"}]}`} {
		a := &recordingAPI{}
		w := request(server(t, a), "POST", "/apis/v1/sessions/a%2Fb/inputs", body)
		require.Equal(t, 200, w.Code, w.Body.String())
		encoded, err := json.Marshal(a.input)
		require.NoError(t, err)
		require.JSONEq(t, body, string(encoded))
	}
}

func (serverSuite *ServerSuite) TestErrorMappingAndDisconnectOwnership() {
	t := serverSuite.T()
	for code, status := range map[string]int{"invalid_argument": 400, "unauthenticated": 401, "permission_denied": 403, "not_found": 404, "already_exists": 409, "conflict": 409, "unsupported": 422, "resource_exhausted": 429, "unavailable": 503, "deadline_exceeded": 504, "outcome_unknown": 500, "internal": 500, "future_code": 500} {
		a := &recordingAPI{err: &transport.Error{Code: code, Reason: "original_reason", Message: "Public explanation"}}
		w := request(server(t, a), "GET", "/apis/v1/runtimes/agent", "")
		require.Equal(t, status, w.Code)
		var e transport.Error
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &e))
		require.Equal(t, code, e.Code)
		require.Equal(t, "original_reason", e.Reason)
	}
	a := &recordingAPI{err: errors.New("private-path-and-secret")}
	w := request(server(t, a), "GET", "/apis/v1/runtimes/agent", "")
	require.NotContains(t, w.Body.String(), "private-path")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	a = &recordingAPI{start: func(ctx context.Context) { once.Do(func() { close(entered) }); <-release; finished <- ctx.Err() }}
	s := server(t, a)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/apis/v1/runtimes/agent/start", http.NoBody)
	r.Header.Set("Authorization", "Bearer test-token")
	done := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(r)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not dispatched")
	}
	cancel()
	require.Error(t, <-done)
	close(release)
	require.NoError(t, <-finished, "disconnect must not cancel dispatched Service context")
}

func (serverSuite *ServerSuite) TestResponseRejectsDynamicCoercion() {
	t := serverSuite.T()
	a := &recordingAPI{}
	s := server(t, a)
	// Input JSON remains numeric JSON values; output coercion is covered using an API override below.
	value := resource.Runtime{Spec: resource.RuntimeSpec{Config: runtime.Config{"object": time.Now()}}}
	api := &dynamicAPI{recordingAPI: *a, value: value}
	actual, err := transport.New(api, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	defer actual.Shutdown()
	w := request(actual, "GET", "/apis/v1/runtimes/agent", "")
	require.Equal(t, 500, w.Code)
	require.Contains(t, w.Body.String(), `"code":"internal"`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/apis/v1/runtimes/agent/start", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, 504, w.Code)
	require.Empty(t, a.calls)
}

type dynamicAPI struct {
	recordingAPI
	value resource.Runtime
}

func (a *dynamicAPI) GetRuntime(string) (resource.Runtime, error) { return a.value, nil }

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineRecorder) SetReadDeadline(v time.Time) error {
	w.deadlines = append(w.deadlines, v)
	return nil
}

type expiredBody struct{}

func (expiredBody) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
func (expiredBody) Close() error             { return nil }
func (serverSuite *ServerSuite) TestBodyReadDoesNotImposeDeadline() {
	t := serverSuite.T()
	a := &recordingAPI{}
	s := server(t, a)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/apis/v1/runtimes", nil)
	r.Body = expiredBody{}
	r.Header.Set("Authorization", "Bearer test-token")
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	s.ServeHTTP(w, r)
	require.Equal(t, 504, w.Code)
	require.Empty(t, a.calls)
	for _, deadline := range w.deadlines {
		require.True(t, deadline.IsZero() || !deadline.After(time.Now()), "transport must not impose a positive body-read timeout")
	}

}

func (serverSuite *ServerSuite) TestSlowBodyCompletesWithoutImplicitCapacityLimit() {
	t := serverSuite.T()
	a := &recordingAPI{}
	handler := server(t, a)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(httpServer.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	body := `{"metadata":{"name":"agent"},"spec":{"type":"codex","config":{}}}`
	_, err = fmt.Fprintf(conn, "POST /apis/v1/runtimes HTTP/1.1\r\nHost: local\r\nAuthorization: Bearer test-token\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n{", len(body))
	require.NoError(t, err)
	_, err = fmt.Fprint(conn, body[1:])
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 201, response.StatusCode)
}

func (serverSuite *ServerSuite) TestEarlyAuthenticationFailureDoesNotDrainBody() {
	t := serverSuite.T()
	handler := server(t, &recordingAPI{})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(httpServer.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	_, err = fmt.Fprintf(conn, "POST /apis/v1/runtimes HTTP/1.1\r\nHost: local\r\nContent-Length: 10\r\n\r\n{")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	require.Equal(t, 401, response.StatusCode)
	response.Body.Close()
}

func TestServerSuite(t *testing.T) { suite.Run(t, new(ServerSuite)) }
