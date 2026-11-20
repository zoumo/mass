package core_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"

	"github.com/zoumo/mass/superagent/pkg/core"
)

// This is the controlled execution boundary. Core, SQLite, MASS client,
// WebSocket transport and Service remain real; no model or external account runs.
type routeDriver struct {
	pipe          *events.EventPipe
	mu            sync.Mutex
	opens         int
	failOpens     int
	inputs        map[string][]runtime.Input
	sources       map[string]events.Source
	injectErr     error
	injectReceipt runtime.InjectReceipt
}

func (d *routeDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*routeDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (d *routeDriver) OpenSession(_ context.Context, a runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens++
	if d.failOpens > 0 {
		d.failOpens--
		return runtime.DriverSession{}, errors.Join(runtime.Unavailable("fixture_prepare_failure"), runtime.ErrResourcesReleased)
	}
	id, source := a.Namespace+"/"+a.SessionName, events.NewSource()
	d.sources[id] = source
	return runtime.DriverSession{SessionID: id, Source: source, Capabilities: runtime.Capabilities{"textInput": true}}, nil
}

func (*routeDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected resume")
}

func (*routeDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return errors.New("unexpected load")
}

func (*routeDriver) CancelHistoryLoad(context.Context, string, string) error {
	return errors.New("unexpected load cancel")
}

func (d *routeDriver) SendPrompt(_ context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inputs[id] = append(d.inputs[id], input)
	return runtime.InputReceipt{Submission: "start", Result: "applied_new_work", Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}}, nil
}

func (d *routeDriver) Inject(_ context.Context, id string, input runtime.Input) (runtime.InjectReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inputs[id] = append(d.inputs[id], input)
	return d.injectReceipt, d.injectErr
}

func (*routeDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (*routeDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, errors.New("unexpected answer")
}
func (*routeDriver) CloseSession(context.Context, string) error { return nil }
func (d *routeDriver) Stop(context.Context) error               { d.pipe.End(nil); return nil }

type routeFactory struct{ driver *routeDriver }

func (routeFactory) Validate(runtime.Config) error                        { return nil }
func (f routeFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.driver, nil }

type routeBoundary struct {
	url            string
	driver         *routeDriver
	svc            *service.Service
	dropNextCreate atomic.Bool
	dropped        atomic.Int32
}

func newRouteBoundary(t *testing.T) *routeBoundary {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	d := &routeDriver{pipe: events.NewEventPipe(64), inputs: map[string][]runtime.Input{}, sources: map[string]events.Source{}}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": routeFactory{d}}))
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	api, err := transport.New(svc, transport.Options{LocalOnly: true, WriteTimeout: time.Second, ServerVersion: "controlled-route-boundary"})
	require.NoError(t, err)
	upstream := httptest.NewServer(api)
	f := &routeBoundary{driver: d, svc: svc}
	f.url = dropRPCResponse(t, "ws"+strings.TrimPrefix(upstream.URL, "http")+"/rpc", func(method string) bool {
		return method == "session.create" && f.dropNextCreate.CompareAndSwap(true, false)
	}, func() { f.dropped.Add(1) })
	t.Cleanup(func() {
		api.Shutdown()
		upstream.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
		t.Log("controlled MASS transport and Driver released")
	})
	return f
}

func dropRPCResponse(t *testing.T, upstream string, drop func(string) bool, dropped func()) string {
	t.Helper()
	return proxyRPC(t, upstream, func(method string, _ func()) bool { return drop(method) }, dropped)
}

func proxyRPC(t *testing.T, upstream string, intercept func(string, func()) bool, dropped func()) string {
	t.Helper()
	// Drop a successful WebSocket response after massd has performed the operation.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := http.Header{}
		if p := r.Header.Get("Sec-WebSocket-Protocol"); p != "" {
			headers.Set("Sec-WebSocket-Protocol", p)
		}
		up, resp, dialErr := websocket.DefaultDialer.DialContext(r.Context(), upstream, headers)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if dialErr != nil {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{up.Subprotocol()}}
		down, upgradeErr := upgrader.Upgrade(w, r, nil)
		if upgradeErr != nil {
			_ = up.Close()
			return
		}
		var mu sync.Mutex
		dropID := ""
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = up.Close(); _ = down.Close() }()
			for {
				kind, data, readErr := down.ReadMessage()
				if readErr != nil {
					return
				}
				var req struct {
					Method string          `json:"method"`
					ID     json.RawMessage `json:"id"`
				}
				if json.Unmarshal(data, &req) == nil && intercept(req.Method, func() { _ = down.Close() }) {
					mu.Lock()
					dropID = string(req.ID)
					mu.Unlock()
				}
				if up.WriteMessage(kind, data) != nil {
					return
				}
			}
		}()
		for {
			kind, data, readErr := up.ReadMessage()
			if readErr != nil {
				break
			}
			var result struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(data, &result) == nil {
				mu.Lock()
				discard := dropID != "" && string(result.ID) == dropID && len(result.Result) > 0
				mu.Unlock()
				if discard {
					dropped()
					break
				}
			}
			if down.WriteMessage(kind, data) != nil {
				break
			}
		}
		_ = down.Close()
		_ = up.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	return "ws" + strings.TrimPrefix(proxy.URL, "http") + "/rpc"
}

const fixtureExecutionProfiles = `  roles:
    router:
      runtimeRef: agent
      model: router-model
      effort: low
    topic:
      runtimeRef: agent
      model: topic-model
  workerTemplates:
    - runtimeRef: agent
      model: worker-model
      effort: medium
      description: Implement changes
    - runtimeRef: agent
      model: review-model
      description: Review results
`

func (f *fixture) execution(boundary *routeBoundary) {
	f.t.Helper()
	b, err := os.ReadFile(f.config)
	require.NoError(f.t, err)
	b = append(b, fmt.Appendf(nil, "execution:\n  massdEndpoint: %s\n  routerWorkspace: %s\n  namespace: superagent\n", boundary.url, f.dir)...)
	b = append(b, fixtureExecutionProfiles...)
	require.NoError(f.t, os.WriteFile(f.config, []byte(fixedListen(f.t, string(b))), 0o600))
}

func bindings(t *testing.T, p *process) []core.Binding {
	t.Helper()
	_, data := request(t, http.MethodGet, p.endpoint, "/v1/observer/bindings", "", nil)
	var page struct {
		Items []core.Binding `json:"items"`
	}
	require.NoError(t, json.Unmarshal(data, &page))
	return page.Items
}

func waitBinding(t *testing.T, p *process, role, owner, state string) core.Binding {
	t.Helper()
	var found core.Binding
	t.Cleanup(func() {
		if t.Failed() && found.State != state {
			t.Logf("last binding at failed readiness check: %+v", found)
		}
	})
	require.Eventually(t, func() bool {
		for _, b := range bindings(t, p) {
			if b.Role == role && b.OwnerID == owner {
				found = b
				if b.State == state {
					return true
				}
			}
		}
		return false
	}, 15*time.Second, 50*time.Millisecond, "binding role=%s owner=%s state=%s", role, owner, state)
	return found
}

func roleToken(t *testing.T, f *fixture, b core.Binding) string {
	t.Helper()
	path := testCapabilityPath(f, b)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var c struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(data, &c))
	require.NotEmpty(t, c.Token)
	return c.Token
}

func testCapabilityPath(f *fixture, b core.Binding) string {
	id := b.ID
	if b.CapabilityFileID != "" {
		id = b.CapabilityFileID
	}
	return filepath.Join(f.data, "capabilities", id+".json")
}

func roleCall(t *testing.T, p *process, token, tool string, args any) json.RawMessage {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/"+tool, token, args)
	require.Equal(t, http.StatusOK, status, string(data))
	var result struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &result))
	return result.Result
}

func newRoute(op string, ids []string, workspace string) core.RouteRequest {
	rounds := map[string]int{}
	for _, id := range ids {
		rounds[id] = 1
	}
	return core.RouteRequest{OperationID: op, MessageIDs: ids, Rounds: rounds, Kind: "route", Reason: "independent requested work", Targets: []core.RouteTarget{{Create: &core.NewTopic{Title: "Topic " + op, Workspace: workspace}, MessageIDs: ids}}}
}

func decide(t *testing.T, p *process, token string, r core.RouteRequest) core.RouteDecision {
	t.Helper()
	data := roleCall(t, p, token, "route_decide", r)
	var result core.RouteDecision
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

type observedRouteDelivery struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Sender struct {
		Kind    string `json:"kind"`
		TopicID string `json:"topic_id"`
	} `json:"sender"`
	Recipient struct {
		Kind    string `json:"kind"`
		TopicID string `json:"topic_id"`
	} `json:"recipient"`
	Body               string `json:"body"`
	Returned           bool   `json:"returned"`
	ReturnedDeliveryID string `json:"returnedDeliveryId"`
}

func routingDeliveries(t *testing.T, p *process, messageID string) []observedRouteDelivery {
	t.Helper()
	records := readRecord[struct{ Items []observedRouteDelivery }](t, p, "routing-deliveries?limit=200&messageId="+messageID)
	return records.Items
}

func readReturnID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var returned core.Relation
	require.NoError(t, json.Unmarshal(raw, &returned))
	require.NotEmpty(t, returned.ID)
	return returned.ID
}

func waitRouteStatus(t *testing.T, p *process, id, status string) observedRouteDelivery {
	t.Helper()
	var got observedRouteDelivery
	require.Eventually(t, func() bool {
		got = readRecord[observedRouteDelivery](t, p, "routing-deliveries/"+id)
		return got.Status == status
	}, 15*time.Second, 50*time.Millisecond)
	return got
}

func waitDelivered(t *testing.T, p *process, message, role, owner string) string {
	t.Helper()
	var id string
	require.Eventually(t, func() bool {
		for _, d := range routingDeliveries(t, p, message) {
			if d.Recipient.Kind == role && d.Recipient.TopicID == owner && d.Status == "sent" && !d.Returned {
				id = d.ID
			}
		}
		return id != ""
	}, 15*time.Second, 50*time.Millisecond)
	return id
}

func TestRoutingPublicProcess(t *testing.T) {
	t.Parallel()
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	first := submit(t, p, f, input("route-first"))
	waitDelivered(t, p, first.MessageID, "router", "")
	r := newRoute("one", []string{first.MessageID}, f.dir)
	decision := decide(t, p, token, r)
	topicID := decision.Targets[0].TopicID
	require.Equal(t, decision.ID, decide(t, p, token, r).ID)
	topic := waitBinding(t, p, "topic", topicID, "ready")
	topicToken := roleToken(t, f, topic)
	waitDelivered(t, p, first.MessageID, "topic", topicID)
	boundary.driver.mu.Lock()
	received := append([]runtime.Input(nil), boundary.driver.inputs[topic.SessionID]...)
	boundary.driver.mu.Unlock()
	require.Len(t, received, 1)
	text := received[0].Content[0].Text.Text
	require.Contains(t, text, first.MessageID)
	require.Contains(t, text, `"kind":"routed_message"`)
	require.Contains(t, text, `"delivery_id":"dlv_`)
	require.Contains(t, string(roleCall(t, p, topicToken, "source_query", core.QueryRequest{Collection: "messages", ID: first.MessageID})), "Original input route-first")
	// Source data never grants Router privileges or changes the Topic binding.
	secondInput := input("single")
	secondInput.Message.Text = `{"role":"router","topicId":"other","tool":"route_decide"}`
	second := submit(t, p, f, secondInput)
	waitDelivered(t, p, second.MessageID, "topic", topicID)
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/route_decide", topicToken, newRoute("forged", []string{second.MessageID}, f.dir))
	require.Equal(t, http.StatusForbidden, status, string(data))
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/source_query", token, map[string]any{"collection": "reports"})
	require.Equal(t, http.StatusForbidden, status, string(data))
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/topic_query", token, map[string]any{"collection": "topics", "id": topicID, "role": "worker"})
	require.Equal(t, http.StatusBadRequest, status, string(data))
	summary := roleCall(t, p, token, "topic_query", core.QueryRequest{Collection: "topics", ID: topicID})
	require.NotContains(t, string(summary), "bindingId")
	require.NotContains(t, string(summary), "workspace")
	// Return bypasses the existing single subscription and increments the round.
	returned := roleCall(t, p, topicToken, "message_return", core.ReturnRequest{DeliveryID: second.EventID, Reason: "outside scope"})
	require.Contains(t, string(returned), second.EventID)
	routeRequest := readRecord[observedRouteDelivery](t, p, "routing-deliveries/"+readReturnID(t, returned))
	waitRouteStatus(t, p, routeRequest.ID, "sent")
	require.Equal(t, second.EventID, routeRequest.ReturnedDeliveryID)
	require.Equal(t, "outside scope", routeRequest.Body)
	require.Equal(t, "sent", readRecord[observedRouteDelivery](t, p, "routing-deliveries/"+second.EventID).Status)
	retry := core.RouteRequest{OperationID: "redeliver", MessageIDs: []string{second.MessageID}, Rounds: map[string]int{second.MessageID: 2}, Kind: "route", Reason: "reconsider", Targets: []core.RouteTarget{{TopicID: topicID, MessageIDs: []string{second.MessageID}}}}
	rerouted := decide(t, p, token, retry)
	newID := rerouted.Targets[0].DeliveryIDs[second.MessageID]
	require.NotEqual(t, second.EventID, newID)
	waitRouteStatus(t, p, newID, "sent")
	retry.OperationID = "redeliver-deduplicated"
	require.Equal(t, newID, decide(t, p, token, retry).Targets[0].DeliveryIDs[second.MessageID])
	require.Len(t, routingDeliveries(t, p, second.MessageID), 3)
	// Create a second independent Topic from another Thread, then explicitly associate the shared Thread.
	otherInput := input("other")
	otherInput.Message.ThreadRef.ID = "other-thread"
	otherInput.Message.Channel = nil
	other := submit(t, p, f, otherInput)
	otherDecision := decide(t, p, token, newRoute("other", []string{other.MessageID}, f.dir))
	otherID := otherDecision.Targets[0].TopicID
	otherBinding := waitBinding(t, p, "topic", otherID, "ready")
	otherToken := roleToken(t, f, otherBinding)
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/source_query", otherToken, core.QueryRequest{Collection: "messages", ID: first.MessageID})
	require.Equal(t, http.StatusNotFound, status, string(data))
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/topic_query", topicToken, core.QueryRequest{Collection: "topics", ID: otherID})
	require.Equal(t, http.StatusNotFound, status, string(data))
	shared := submit(t, p, f, input("shared"))
	roleCall(t, p, topicToken, "message_return", core.ReturnRequest{DeliveryID: shared.EventID, Reason: "contains two independent requests"})
	many := core.RouteRequest{OperationID: "many", MessageIDs: []string{shared.MessageID}, Rounds: map[string]int{shared.MessageID: 2}, Kind: "route", Reason: "explicit independent scopes", Targets: []core.RouteTarget{{TopicID: topicID, MessageIDs: []string{shared.MessageID}}, {TopicID: otherID, MessageIDs: []string{shared.MessageID}}}}
	decide(t, p, token, many)
	waitDelivered(t, p, shared.MessageID, "topic", topicID)
	waitDelivered(t, p, shared.MessageID, "topic", otherID)
	multi := submit(t, p, f, input("multi"))
	waitDelivered(t, p, multi.MessageID, "router", "")
	require.Len(t, routingDeliveries(t, p, multi.MessageID), 1)
	require.Equal(t, "router", routingDeliveries(t, p, multi.MessageID)[0].Recipient.Kind)
	// Router source query exposes rounds; Topic queries remain restricted even on a shared Thread.
	source := roleCall(t, p, token, "source_query", core.QueryRequest{Collection: "messages", ID: multi.MessageID})
	require.Contains(t, string(source), `"round":1`)
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/source_query", topicToken, core.QueryRequest{Collection: "messages", ID: multi.MessageID})
	require.Equal(t, http.StatusNotFound, status, string(data))
	runRoleMCP(t, f, p, router, topic, topicID, otherID)
	// Commit/restart preserves Router identity, Topic bindings, decisions, subscriptions and delivered inputs.
	before := bindings(t, p)
	liveMCP := startRoleMCP(t, f, router)
	p.stop(t, true)
	p = f.start()
	require.Equal(t, router.ID, waitBinding(t, p, "router", "", "ready").ID)
	require.Empty(t, liveMCP.call("tools/call", map[string]any{"name": "topic_query", "arguments": core.QueryRequest{Collection: "topics", ID: topicID}})["error"])
	require.Len(t, bindings(t, p), len(before))
	require.Equal(t, decision.ID, decide(t, p, token, r).ID)
	_, data = request(t, http.MethodGet, p.endpoint, "/v1/observer/subscriptions", "", nil)
	require.Contains(t, string(data), topicID)
	require.Contains(t, string(data), otherID)
	t.Log("0/1/many subscriptions, explicit multi-Topic, retry, return round, scope rejection, stdio MCP and restart passed")
}

type stdioClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	seq    int
	stderr bytes.Buffer
}

func startRoleMCP(t *testing.T, f *fixture, b core.Binding) *stdioClient {
	t.Helper()
	c := &stdioClient{t: t}
	c.cmd = exec.CommandContext(context.Background(), f.coreExecutable(), "mcp", "--capability-file", testCapabilityPath(f, b))
	c.cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	c.cmd.Stderr = &c.stderr
	in, err := c.cmd.StdinPipe()
	require.NoError(t, err)
	c.in = in
	out, err := c.cmd.StdoutPipe()
	require.NoError(t, err)
	c.out = bufio.NewReader(out)
	require.NoError(t, c.cmd.Start())
	t.Cleanup(func() {
		_ = c.in.Close()
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case err := <-done:
			require.NoError(t, err, c.stderr.String())
		case <-time.After(5 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
			t.Error("MCP did not release")
		}
		t.Logf("MCP released pid=%d", c.cmd.Process.Pid)
	})
	c.call("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "process-test", "version": "1"}})
	return c
}

func (c *stdioClient) call(method string, params any) map[string]json.RawMessage {
	c.t.Helper()
	c.seq++
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.seq, "method": method, "params": params})
	require.NoError(c.t, err)
	_, err = c.in.Write(append(data, '\n'))
	require.NoError(c.t, err)
	line, err := c.out.ReadBytes('\n')
	require.NoError(c.t, err)
	var result map[string]json.RawMessage
	require.NoError(c.t, json.Unmarshal(line, &result))
	return result
}

func runRoleMCP(t *testing.T, f *fixture, p *process, router, topic core.Binding, topicID, otherID string) {
	t.Helper()
	r := startRoleMCP(t, f, router)
	s := startRoleMCP(t, f, topic)
	listed := r.call("tools/list", map[string]any{"_meta": map[string]any{"progressToken": 1}})
	require.Empty(t, listed["error"], "MCP client metadata must not prevent scoped tool discovery")
	require.Contains(t, string(listed["result"]), "route_decide")
	require.NotContains(t, string(listed["result"]), "message_return")
	require.NotContains(t, string(listed["result"]), "binding_get")
	invalidList := r.call("tools/list", map[string]any{"role": "topic"})
	require.Contains(t, string(invalidList["error"]), "Invalid params")
	listed = s.call("tools/list", map[string]any{})
	require.Contains(t, string(listed["result"]), "message_return")
	require.NotContains(t, string(listed["result"]), "route_decide")
	require.NotContains(t, string(listed["result"]), "binding_get")
	mcpWorkError(t, r, "binding_get", "forbidden", struct{}{})
	mcpWorkError(t, s, "binding_get", "forbidden", struct{}{})
	for _, binding := range []core.Binding{router, topic} {
		identity := roleCall(t, p, roleToken(t, f, binding), "binding_get", struct{}{})
		require.JSONEq(t, fmt.Sprintf(`{"id":%q,"role":%q,"ownerId":%q,"generation":%d}`,
			binding.ID, binding.Role, binding.OwnerID, binding.Generation), string(identity))
	}
	got := r.call("tools/call", map[string]any{"name": "topic_query", "arguments": map[string]any{"collection": "topics", "id": topicID}})
	require.Empty(t, got["error"])
	require.NotContains(t, string(got["result"]), "bindingId")
	denied := s.call("tools/call", map[string]any{"name": "topic_query", "arguments": map[string]any{"collection": "topics", "id": otherID}})
	require.Contains(t, string(denied["error"]), "not_found")
	denied = s.call("tools/call", map[string]any{"name": "route_decide", "arguments": map[string]any{}})
	require.Contains(t, string(denied["error"]), "forbidden")
	incoming := input("mcp-ignore")
	incoming.Message.ThreadRef.ID = "mcp-thread"
	incoming.Message.Channel = nil
	receipt := submit(t, p, f, incoming)
	ignored := r.call("tools/call", map[string]any{"name": "route_decide", "arguments": core.RouteRequest{OperationID: "mcp-ignore", MessageIDs: []string{receipt.MessageID}, Rounds: map[string]int{receipt.MessageID: 1}, Kind: "ignore", Reason: "explicitly not actionable"}})
	require.Empty(t, ignored["error"])

	business := input("mcp-create")
	business.Message.ThreadRef.ID = "mcp-create-thread"
	business.Message.Channel = nil
	m := submit(t, p, f, business)
	routed := r.call("tools/call", map[string]any{"name": "route_decide", "arguments": newRoute("mcp-create", []string{m.MessageID}, f.dir)})
	require.Empty(t, routed["error"])
	var result struct {
		StructuredContent core.RouteDecision `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal(routed["result"], &result))
	bound := waitBinding(t, p, "topic", result.StructuredContent.Targets[0].TopicID, "ready")
	waitDelivered(t, p, m.MessageID, "topic", bound.OwnerID)
	topicMCP := startRoleMCP(t, f, bound)
	returned := topicMCP.call("tools/call", map[string]any{"name": "message_return", "arguments": core.ReturnRequest{DeliveryID: result.StructuredContent.Targets[0].DeliveryIDs[m.MessageID], Reason: "explicit stdio return"}})
	require.Empty(t, returned["error"])
	require.Contains(t, string(returned["result"]), result.StructuredContent.Targets[0].DeliveryIDs[m.MessageID])
	waitDelivered(t, p, m.MessageID, "router", "")
	// Capability scope cannot be supplied as an argument, even by native inherited callers.
	denied = s.call("tools/call", map[string]any{"name": "source_query", "arguments": map[string]any{"collection": "messages", "role": "router"}})
	require.Contains(t, string(denied["error"]), "invalid_argument")
}

func TestRoutingAtomicConcurrentDecisions(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	a := submit(t, p, f, input("overlap-a"))
	b := submit(t, p, f, input("overlap-b"))
	c := submit(t, p, f, input("overlap-c"))
	requests := []core.RouteRequest{newRoute("left", []string{a.MessageID, b.MessageID}, f.dir), newRoute("right", []string{b.MessageID, c.MessageID}, f.dir)}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Go(func() {
			code, _ := request(t, http.MethodPost, p.endpoint, "/v1/role/route_decide", token, r)
			codes <- code
		})
	}
	wg.Wait()
	close(codes)
	result := []int{}
	for code := range codes {
		result = append(result, code)
	}
	require.ElementsMatch(t, []int{200, 409}, result)
	_, data := request(t, http.MethodGet, p.endpoint, "/v1/observer/topics", "", nil)
	var page core.Page
	require.NoError(t, json.Unmarshal(data, &page))
	require.Len(t, page.Items, 1)
	_, data = request(t, http.MethodGet, p.endpoint, "/v1/observer/relations", "", nil)
	require.NoError(t, json.Unmarshal(data, &page))
	require.Len(t, page.Items, 2)
	// A valid create followed by an invalid existing target rolls back the whole request.
	fresh := input("rollback")
	fresh.Message.ThreadRef.ID = "rollback-thread"
	fresh.Message.Channel = nil
	m := submit(t, p, f, fresh)
	r := newRoute("rollback", []string{m.MessageID}, f.dir)
	r.Targets = append(r.Targets, core.RouteTarget{TopicID: "missing-topic", MessageIDs: []string{m.MessageID}})
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/route_decide", token, r)
	require.Equal(t, 404, status, string(data))
	_, data = request(t, http.MethodGet, p.endpoint, "/v1/observer/topics", "", nil)
	require.NoError(t, json.Unmarshal(data, &page))
	require.Len(t, page.Items, 1)
	require.Len(t, bindings(t, p), 2)
	t.Log("overlapping source rounds commit one entire decision; later target failure leaves no Topic, binding, subscription or delivery")
}

func TestRoutingLostCreateAndPreparationRetry(t *testing.T) {
	boundary := newRouteBoundary(t)
	boundary.dropNextCreate.Store(true)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	require.EqualValues(t, 1, boundary.dropped.Load())
	token := roleToken(t, f, router)
	boundary.driver.mu.Lock()
	require.Equal(t, 1, boundary.driver.opens)
	boundary.driver.failOpens = 1
	boundary.driver.mu.Unlock()
	receipt := submit(t, p, f, input("prepare-retry"))
	decision := decide(t, p, token, newRoute("prepare", []string{receipt.MessageID}, f.dir))
	id := decision.Targets[0].TopicID
	topic := waitBinding(t, p, "topic", id, "pending")
	require.Eventually(t, func() bool {
		for _, b := range bindings(t, p) {
			if b.ID == topic.ID && b.LastError != "" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	p.stop(t, true)
	p = f.start()
	boundary.dropNextCreate.Store(true)
	require.Equal(t, "topic-g1", topic.Name)
	waitBinding(t, p, "topic", id, "ready")
	require.EqualValues(t, 2, boundary.dropped.Load())
	waitDelivered(t, p, receipt.MessageID, "topic", id)
	_, data := request(t, http.MethodGet, p.endpoint, "/v1/observer/topics", "", nil)
	var page core.Page
	require.NoError(t, json.Unmarshal(data, &page))
	require.Len(t, page.Items, 1)
	boundary.driver.mu.Lock()
	require.Equal(t, 3, boundary.driver.opens)
	boundary.driver.mu.Unlock()
	// A conflicting fixed name is never adopted or prompted.
	f2 := fixtureAt(t)
	f2.execution(boundary)
	p.stop(t, false)
	mass, err := client.New(boundary.url, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	defer func() { _ = mass.Close() }()
	var original resource.Session
	require.NoError(t, mass.GetSession(t.Context(), resource.SessionRef{Namespace: router.Namespace, Name: router.Name}, &original))
	p2 := f2.start()
	blocked := waitBinding(t, p2, "router", "", "blocked")
	require.Equal(t, "session_configuration_conflict", blocked.LastError)
	var unchanged resource.Session
	require.NoError(t, mass.GetSession(t.Context(), resource.SessionRef{Namespace: router.Namespace, Name: router.Name}, &unchanged))
	require.Equal(t, original.Metadata.UID, unchanged.Metadata.UID)
	require.Equal(t, original.Spec, unchanged.Spec)
	t.Log("actual creation response dropped after commit; same Session reconciled; Topic preparation retried with fixed identity; unrelated conflict rejected")
}

func TestRoutingObserverBrowserFixture(t *testing.T) {
	if os.Getenv("SUPERAGENT_ROUTING_BROWSER_TEST") != "1" {
		t.Skip("explicit routing browser fixture only")
	}
	boundary := newRouteBoundary(t)
	audit := &observerBrowserAudit{methods: make(map[string]int)}
	boundary.url = proxyRPC(t, boundary.url, audit.recordRPC, func() {})
	f := fixtureAt(t)
	configureOutboxSource(t, f)
	prToken := addPRSource(t, f)
	f.execution(boundary)
	data, err := os.ReadFile(f.config)
	require.NoError(t, err)
	port := os.Getenv("SUPERAGENT_ROUTING_BROWSER_PORT")
	if port == "" {
		port = "18794"
	}
	oldListen := strings.Split(strings.SplitN(string(data), "listen: ", 2)[1], "\n")[0]
	write(t, f.config, strings.Replace(string(data), "listen: "+oldListen, "listen: 127.0.0.1:"+port, 1))
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	m := submit(t, p, f, input("observer-routing"))
	r := newRoute("observer-routing", []string{m.MessageID}, f.dir)
	r.Targets[0].Create.Title = "Observer routing Topic"
	decision := decide(t, p, token, r)
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	waitDelivered(t, p, m.MessageID, "topic", topic.OwnerID)
	roleCall(t, p, roleToken(t, f, topic), "message_return", core.ReturnRequest{DeliveryID: decision.Targets[0].DeliveryIDs[m.MessageID], Reason: "Observer return reason"})
	waitDelivered(t, p, m.MessageID, "router", "")
	decide(t, p, token, core.RouteRequest{
		OperationID: "observer-ignore", MessageIDs: []string{m.MessageID}, Rounds: map[string]int{m.MessageID: 2},
		Kind: "ignore", Reason: "Observer ignore reason",
	})
	finishedInput := input("observer-finished")
	finishedInput.Message.ThreadRef.ID, finishedInput.Message.Channel.ConversationID = "observer-finished", "observer-finished"
	finished := submit(t, p, f, finishedInput)
	finishedRoute := newRoute("observer-finished", []string{finished.MessageID}, f.dir)
	finishedRoute.Targets[0].Create.Title = "Archived sample Topic"
	finishedDecision := decide(t, p, token, finishedRoute)
	finishedBinding := waitBinding(t, p, "topic", finishedDecision.Targets[0].TopicID, "ready")
	waitDelivered(t, p, finished.MessageID, "topic", finishedBinding.OwnerID)
	roleWork[core.Topic](t, p, roleToken(t, f, finishedBinding), "topic_complete", core.CompleteRequest{
		OperationID: "observer-finished-accept", ExpectedRevision: 1, Reason: "Reviewed the archived result", Evidence: []string{"artifacts/archive.log"},
	})
	boundary.driver.mu.Lock()
	boundary.driver.injectReceipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}}
	boundary.driver.mu.Unlock()
	queueInternal(t, p, newInternalEvent("browser-accepted", "context_supplement", deliveryParty{Kind: "router"}))
	waitWorkDelivery(t, p, "browser-accepted", "sent")
	topicToken := roleToken(t, f, topic)
	timerRequest := timerInput("observer-timer", timerFuture)
	timerRequest.Materials = []core.Material{{Label: "Archive checklist", URI: "artifacts/archive-checklist.md"}}
	timer := roleWork[core.Timer](t, p, topicToken, "timer_set", timerRequest)
	roleWork[core.Timer](t, p, topicToken, "timer_update", core.UpdateTimerRequest{
		CancelTimerRequest: cancelTimerInput("observer-timer-update", timer),
		DueAt:              timerPast, Content: "Review the archived build.", Materials: timerRequest.Materials,
	})
	firedTimer := waitTimer(t, p, timer.ID, "fired")
	waitTimerDelivery(t, p, firedTimer.Followup.ID, "sent")
	create := issueInput(f.dir, "observer-issue")
	create.Title = "Verify sample build"
	create.Goal = "Run the sample build and inspect its exit code."
	create.Materials = []core.Material{{Label: "Build notes", URI: "artifacts/build-notes.md"}}
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "issue_create", create)
	worker := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	roleWork[core.PullRequest](t, p, topicToken, "pr_register",
		core.RegisterPRRequest{OperationID: "observer-pr-without-input", URL: internalPR})
	roleWork[core.PullRequest](t, p, roleToken(t, f, worker), "pr_register",
		core.RegisterPRRequest{OperationID: "observer-pr-with-input", URL: firstPRAlias})
	acceptPRMessage(t, p, prToken, prMessage("observer-pr", firstPR, "Synthetic PR review arrived."))
	report := roleWork[core.Report](t, p, roleToken(t, f, worker), "report_submit",
		core.ReportRequest{
			OperationID: "observer-report", Kind: "ready", Summary: "Sample build verified",
			Details: "Build exited with code 0.", Evidence: []string{"artifacts/build.log"}, GoalRevision: work.Execution.Revision,
		})
	waitWorkDelivery(t, p, report.EventID, "sent")
	roleWork[core.WorkReceipt](t, p, topicToken, "issue_complete", core.CompleteIssueRequest{
		IssueID: work.Issue.ID,
		CompleteRequest: core.CompleteRequest{
			OperationID: "observer-complete", ExpectedRevision: work.Issue.Revision,
			Reason: "Reviewed the sample build log", Evidence: []string{"artifacts/build.log"},
		},
	})
	create.OperationID = "observer-blocked"
	create.Title = "Review blocked build"
	blocked := roleWork[core.WorkReceipt](t, p, topicToken, "issue_create", create)
	waitWorkDelivery(t, p, blocked.Execution.StartEventID, "sent")
	blockedWorker := waitBinding(t, p, "worker", blocked.Execution.ID, "ready")
	roleWork[core.Report](t, p, roleToken(t, f, blockedWorker), "report_submit", core.ReportRequest{
		OperationID: "observer-blocked-report", Kind: "progress", Summary: "Archive review in progress",
		GoalRevision: blocked.Execution.Revision,
	})
	updated := roleWork[core.WorkReceipt](t, p, topicToken, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "observer-update-requirements", IssueID: blocked.Issue.ID, ExpectedRevision: blocked.Issue.Revision,
		Goal: "Inspect the archived build output.", NotifyWorker: true,
	})
	waitWorkDelivery(t, p, updated.NotificationEventID, "sent")
	markdown := core.StructuredContent{Type: "markdown", Data: json.RawMessage(`{"text":"### Archive review checklist\n\nThe **current build** is ready for review.\n\n- Check the build log\n- Compare the manifest with the previous build\n\nRun ` + "`" + `make verify` + "`" + ` before approving."}`)}
	checklist := roleWork[core.WorkReceipt](t, p, topicToken, "issue_worker_update", core.WorkerUpdateRequest{
		OperationID: "observer-archive-checklist", IssueID: blocked.Issue.ID, ExpectedRevision: updated.Issue.Revision,
		Mode: "context_supplement", Content: &markdown,
	})
	waitWorkDelivery(t, p, checklist.NotificationEventID, "sent")
	card := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**Which archive should we review?**","options":[{"id":"current","label":"Current build"},{"id":"previous","label":"Previous build"}]}`)}
	supplement := roleWork[core.WorkReceipt](t, p, topicToken, "issue_worker_update", core.WorkerUpdateRequest{
		OperationID: "observer-supplement", IssueID: blocked.Issue.ID, ExpectedRevision: updated.Issue.Revision,
		Mode: "context_supplement", Content: &card,
	})
	waitWorkDelivery(t, p, supplement.NotificationEventID, "sent")
	_ = roleWork[core.WorkReceipt](t, p, topicToken, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "observer-add-blockers", IssueID: blocked.Issue.ID, ExpectedRevision: updated.Issue.Revision,
		Add: []core.BlockerInput{human("Confirm the archive with the reviewer"), dependency(work.Issue)},
	})

	create.OperationID, create.Title = "observer-canceled", "Canceled sample validation"
	canceled := roleWork[core.WorkReceipt](t, p, topicToken, "issue_create", create)
	waitWorkDelivery(t, p, canceled.Execution.StartEventID, "sent")
	cancel := issueLifecycle("observer-cancel", canceled.Issue)
	cancel.Reason = "The sample build was withdrawn"
	stopped := roleWork[core.WorkReceipt](t, p, topicToken, "issue_cancel", cancel)
	_ = waitObservedStop(t, p, "executions", stopped.Execution.ID)
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/send" {
			http.NotFound(w, r)
			return
		}
		audit.sends.Add(1)
		_, _ = io.WriteString(w, "observer-actual-message")
	}))
	t.Cleanup(channel.Close)
	seedObserverOutbox(t, p, f, token, channel.URL)
	serveObserverBrowserAudit(t, audit, boundary, f, p, topic, topicToken)
	t.Logf("routing browser fixture ready endpoint=%s topic=%s", p.endpoint, topic.OwnerID)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	<-signals
	p.stop(t, false)
	for _, credential := range []string{f.token, token, topicToken} {
		if strings.Contains(p.stderr.String(), credential) {
			t.Error("Core logs leaked a fixture credential")
		}
	}
	t.Log("Core logs contain no fixture source or role credentials")
}

func TestRoutingCrashAtomicRoute(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"route_before_commit", "route_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			router := waitBinding(t, p, "router", "", "ready")
			token := roleToken(t, f, router)
			m := submit(t, p, f, input(stage))
			r := newRoute(stage, []string{m.MessageID}, f.dir)
			data, err := json.Marshal(r)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+"/v1/role/route_decide", bytes.NewReader(data))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				resp, doErr := http.DefaultClient.Do(req)
				if doErr == nil {
					_ = resp.Body.Close()
				}
			}()
			require.Eventually(t, func() bool { _, statErr := os.Stat(filepath.Join(gate, stage)); return statErr == nil }, 10*time.Second, 20*time.Millisecond)
			p.stop(t, true)
			<-finished
			p = f.start()
			expected := 0
			if stage == "route_after_commit" {
				expected = 1
			}
			require.Len(t, page(t, p, "topics").Items, expected)
			require.Len(t, page(t, p, "subscriptions").Items, expected)
			require.Len(t, page(t, p, "relations").Items, expected)
			require.Len(t, bindings(t, p), expected+1)
			replay := decide(t, p, token, r)
			require.Equal(t, "mass-t1", replay.Targets[0].TopicID)
			topic := waitBinding(t, p, "topic", replay.Targets[0].TopicID, "ready")
			waitDelivered(t, p, m.MessageID, "topic", topic.OwnerID)
			require.Len(t, page(t, p, "topics").Items, 1)
			require.Equal(t, replay.ID, decide(t, p, token, r).ID)
			require.Equal(t, "mass-t1", readRecord[core.Topic](t, p, "topics/mass-t1").ID)
			t.Logf("%s: public restart readback and identical retry preserve one complete decision", stage)
		})
	}
}

func TestRoutingOperationAndCapabilityFences(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)
	workspace := filepath.Join(f.dir, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0o700))
	a := submit(t, p, f, input("fence-a"))
	r := newRoute("fence", []string{a.MessageID}, workspace)
	d := decide(t, p, token, r)
	topic := waitBinding(t, p, "topic", d.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)
	require.NoError(t, os.Remove(workspace))
	require.Equal(t, d.ID, decide(t, p, token, r).ID)
	changed := r
	changed.Reason = "different request"
	status, body := request(t, http.MethodPost, p.endpoint, "/v1/role/route_decide", token, changed)
	require.Equal(t, http.StatusConflict, status, string(body))
	submit(t, p, f, input("fence-b"))
	submit(t, p, f, input("fence-c"))
	raw := roleCall(t, p, topicToken, "source_query", core.QueryRequest{Collection: "messages", Limit: 1})
	var got core.Page
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NotEmpty(t, got.NextCursor)
	status, body = request(t, http.MethodPost, p.endpoint, "/v1/role/source_query", token, core.QueryRequest{Collection: "messages", Limit: 1, Cursor: got.NextCursor})
	require.Equal(t, http.StatusBadRequest, status, string(body))
	// The file's role/owner/generation must agree with the credential's persisted identity.
	capPath := filepath.Join(f.data, "capabilities", topic.ID+".json")
	data, err := os.ReadFile(capPath)
	require.NoError(t, err)
	var capData map[string]any
	require.NoError(t, json.Unmarshal(data, &capData))
	capData["role"] = "router"
	capData["ownerId"] = ""
	forged, err := json.Marshal(capData)
	require.NoError(t, err)
	forgedPath := filepath.Join(f.dir, "forged.json")
	require.NoError(t, os.WriteFile(forgedPath, forged, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, build(t), "mcp", "--capability-file", forgedPath)
	cmd.Env = []string{"HOME=" + f.dir, "PATH=" + f.dir}
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "stale_binding")
	require.NotContains(t, string(output), topicToken)
	t.Log("operation parameters, filesystem-independent replay, scoped cursor and startup capability identity fenced")
}

func TestRoutingCrashAtomicReturn(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"return_before_commit", "return_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			router := waitBinding(t, p, "router", "", "ready")
			routerToken := roleToken(t, f, router)
			m := submit(t, p, f, input(stage))
			d := decide(t, p, routerToken, newRoute(stage, []string{m.MessageID}, f.dir))
			topic := waitBinding(t, p, "topic", d.Targets[0].TopicID, "ready")
			token := roleToken(t, f, topic)
			waitDelivered(t, p, m.MessageID, "topic", topic.OwnerID)
			r := core.ReturnRequest{DeliveryID: d.Targets[0].DeliveryIDs[m.MessageID], Reason: "outside current scope"}
			data, err := json.Marshal(r)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+"/v1/role/message_return", bytes.NewReader(data))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan struct{})
			go func() {
				defer close(done)
				response, doErr := http.DefaultClient.Do(req)
				if doErr == nil {
					_ = response.Body.Close()
				}
			}()
			require.Eventually(t, func() bool { _, statErr := os.Stat(filepath.Join(gate, stage)); return statErr == nil }, 10*time.Second, 20*time.Millisecond)
			p.stop(t, true)
			<-done
			p = f.start()
			expectedRound, expectedRelations := 1, 1
			if stage == "return_after_commit" {
				expectedRound = 2
				expectedRelations = 2
			}
			raw := roleCall(t, p, routerToken, "source_query", core.QueryRequest{Collection: "messages", ID: m.MessageID})
			var current struct {
				Routing struct {
					Round int `json:"round"`
				} `json:"routing"`
			}
			require.NoError(t, json.Unmarshal(raw, &current))
			require.Equal(t, expectedRound, current.Routing.Round)
			require.Len(t, page(t, p, "relations").Items, expectedRelations)
			require.Len(t, page(t, p, "subscriptions").Items, 1)
			original := roleCall(t, p, token, "message_return", r)
			require.JSONEq(t, string(original), string(roleCall(t, p, token, "message_return", r)))
			waitDelivered(t, p, m.MessageID, "router", "")
			require.Len(t, page(t, p, "relations").Items, 2)
			require.Len(t, page(t, p, "subscriptions").Items, 1)
			t.Logf("%s: return reason, routed delivery history and the stable Router request committed atomically", stage)
		})
	}
}

func TestRoutingPreviousSchemaRefused(t *testing.T) {
	f := fixtureAt(t)
	require.NoError(t, os.MkdirAll(f.data, 0o700))
	path := filepath.Join(f.data, "core.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	// This fixture sets only the previous schema header; no business records are manufactured.
	_, err = db.ExecContext(t.Context(), "PRAGMA application_id=1398096210; PRAGMA user_version=1;")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	f.fails("unsupported")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
	t.Log("schema 1 refused; previous database bytes unchanged")
}

func TestRoutingEnableAndExecutionStartupConfig(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	p := f.start()
	m := submit(t, p, f, input("intake-before-execution"))
	require.Empty(t, bindings(t, p))
	p.stop(t, false)
	f.execution(boundary)
	p = f.start()
	router := waitBinding(t, p, "router", "", "ready")
	waitDelivered(t, p, m.MessageID, "router", "")
	p.stop(t, false)
	original, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(original), boundary.url, "ws://127.0.0.1:1/rpc", 1))
	p = f.start()
	waitBinding(t, p, "router", "", "pending")
	require.Len(t, bindings(t, p), 1)
	p.stop(t, false)
	write(t, f.config, strings.Replace(string(original), boundary.url, "ws://", 1))
	_, err = core.LoadConfig(f.config)
	require.Error(t, err)
	write(t, f.config, string(original))
	p = f.start()
	require.Equal(t, router.ID, waitBinding(t, p, "router", "", "ready").ID)
	require.Len(t, page(t, p, "messages").Items, 1)
	require.Len(t, routingDeliveries(t, p, m.MessageID), 1)
	t.Log("intake delivery resumed with its original ID; massd endpoint came from startup configuration and the existing binding identity was retained")
}

func TestRoutedMessageAgentPromptView(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	configureOutboxSource(t, f)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	var routerSession resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: routerBinding.SessionID}, &routerSession))
	require.Contains(t, routerSession.Spec.SystemPrompt, "deduplicate by delivery_id")
	require.Contains(t, routerSession.Spec.SystemPrompt, "routing.round")
	require.Contains(t, routerSession.Spec.SystemPrompt, "content.data.text")
	require.NotContains(t, routerSession.Spec.SystemPrompt, "eventId")

	short := input("short")
	msg := submit(t, p, f, short)
	route := peer.next(t, "turn/start")
	require.Equal(t, routerBinding.SessionID, route.session)
	require.JSONEq(t, fmt.Sprintf(`{"events":[{"delivery_id":%q,"kind":"route_request","refs":[{"kind":"trigger_message","id":%q}],"content":{"type":"markdown","data":{"text":%q}},"threadKey":%q,"routing":{"round":1,"state":"pending"}}]}`, msg.EventID, msg.MessageID, short.Message.Text, msg.ThreadKey), route.text)
	var payload struct {
		Events []struct {
			Refs    []struct{ ID string }
			Routing struct{ Round int }
		}
	}
	require.NoError(t, json.Unmarshal([]byte(route.text), &payload))
	messageID := payload.Events[0].Refs[0].ID
	route.answer <- nativeDeliveryAnswer{complete: true}
	req := newRoute("short-topic", []string{messageID}, f.dir)
	req.Rounds[messageID] = payload.Events[0].Routing.Round
	decision := mcpWork[core.RouteDecision](t, router, "route_decide", req)
	target := decision.Targets[0]
	binding := waitBinding(t, p, "topic", target.TopicID, "ready")
	topic := startRoleMCP(t, f, binding)
	var topicSession resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &topicSession))
	require.Contains(t, topicSession.Spec.SystemPrompt, "content.data.text")
	require.Contains(t, topicSession.Spec.SystemPrompt, "source_query")
	delivered := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, delivered.session)
	require.JSONEq(t, fmt.Sprintf(`{"events":[{"delivery_id":%q,"kind":"routed_message","refs":[{"kind":"trigger_message","id":%q}],"content":{"type":"markdown","data":{"text":%q}}}]}`, target.DeliveryIDs[msg.MessageID], msg.MessageID, short.Message.Text), delivered.text)
	delivered.answer <- nativeDeliveryAnswer{complete: true}

	for _, req := range []core.SubmitRequest{input("long"), input("quoted")} {
		if req.Message.SourceMessageID == "long" {
			req.Message.Text = strings.Repeat("界", 700)
		} else {
			req.Message.QuotedMessage = json.RawMessage(`{"message_id":"native-question"}`)
		}
		receipt := submit(t, p, f, req)
		call := peer.next(t, "turn/start")
		require.Equal(t, binding.SessionID, call.session)
		require.JSONEq(t, fmt.Sprintf(`{"events":[{"delivery_id":%q,"kind":"routed_message","refs":[{"kind":"trigger_message","id":%q}]}]}`, receipt.EventID, receipt.MessageID), call.text)
		call.answer <- nativeDeliveryAnswer{complete: true}

		lookup := core.QueryRequest{Collection: "messages", ID: receipt.MessageID}
		item := mcpWork[json.RawMessage](t, topic, "source_query", lookup)
		require.JSONEq(t, fmt.Sprintf(`{"id":%q,"source":"dws","text":%q,"threadKey":%q,"actor":"sender-1"}`, receipt.MessageID, req.Message.Text, receipt.ThreadKey), string(item))
		items := mcpWork[core.Page](t, topic, "source_query", core.QueryRequest{Collection: "messages", ThreadKey: receipt.ThreadKey})
		require.JSONEq(t, string(item), string(items.Items[len(items.Items)-1]))
		original := readRecord[core.Message](t, p, "messages/"+receipt.MessageID)
		require.Equal(t, req.Message.SourceMessageID, original.SourceMessageID)
		require.Equal(t, req.Message.Channel, original.Channel)
	}

	issue := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "quoted-work")).Issue
	issue = blockers(t, topic, issue, "wait-for-date", human("Confirm the release date"))
	blocker := core.BlockerRef{IssueID: issue.ID, BlockerID: issue.Blockers[0].ID}
	question := mcpWork[core.OutboxItem](t, topic, "outbox_message_reply_structured", core.ReplyRequest{
		OperationID: "ask-date", MessageID: msg.MessageID, Content: markdownReply("Which date?"), BlockerRef: &blocker,
	})
	sent := outboxResult(question, "sent", "", "native-release-date")
	sent.ConversationID = short.Message.Channel.ConversationID
	resultOutbox(t, p, f.token, sent)

	reply := input("matched-quote")
	reply.Message.Text = "Tuesday UTC"
	reply.Message.QuotedMessage = json.RawMessage(`{"message_id":"native-release-date"}`)
	answer := submit(t, p, f, reply)
	call := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, call.session)
	require.JSONEq(t, fmt.Sprintf(`{"events":[{"delivery_id":%q,"kind":"routed_message","refs":[{"kind":"trigger_message","id":%q}],"content":{"type":"markdown","data":{"text":"Tuesday UTC"}},"quotedReply":{"outboxId":%q,"blockerRef":{"issueId":%q,"blockerId":%q}},"issue":{"id":%q,"revision":%d,"status":"blocked","blockers":[{"id":%q,"kind":"human","reason":"Confirm the release date"}]}}]}`, answer.EventID, answer.MessageID, question.ID, issue.ID, blocker.BlockerID, issue.ID, issue.Revision, blocker.BlockerID), call.text)
	view := decodeInternalBatch(t, call, answer.EventID)[0]
	var quotedIssue core.Issue
	require.NoError(t, json.Unmarshal(view.Issue, &quotedIssue))
	call.answer <- nativeDeliveryAnswer{complete: true}
	resolved := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "resolve-date", IssueID: quotedIssue.ID, ExpectedRevision: quotedIssue.Revision, UnblockedStatus: "pending",
		Resolve: []core.BlockerResolution{{BlockerID: quotedIssue.Blockers[0].ID, Reason: reply.Message.Text, MessageID: answer.MessageID}},
	})
	require.Empty(t, resolved.Issue.Blockers)

	long := input("long-router")
	long.Message.ThreadRef.ID, long.Message.Channel = "unsubscribed", nil
	long.Message.Text = strings.Repeat("界", 700)
	longReceipt := submit(t, p, f, long)
	longInput := peer.next(t, "turn/start")
	require.Equal(t, routerBinding.SessionID, longInput.session)
	require.JSONEq(t, fmt.Sprintf(`{"events":[{"delivery_id":%q,"kind":"route_request","refs":[{"kind":"trigger_message","id":%q}]}]}`, longReceipt.EventID, longReceipt.MessageID), longInput.text)
	longInput.answer <- nativeDeliveryAnswer{complete: true}
	type messageReadback struct {
		Text    string
		Routing struct{ Round int }
	}
	longReadback := mcpWork[messageReadback](t, router, "source_query", core.QueryRequest{Collection: "messages", ID: longReceipt.MessageID})
	require.Equal(t, long.Message.Text, longReadback.Text)
	require.Equal(t, 1, longReadback.Routing.Round)
}

func TestRoutingTargetRecoveryAndLateReceipt(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)

	m := submit(t, p, f, input("independent-targets"))
	route := peer.next(t, "turn/start")
	decodeInternalBatch(t, route, m.EventID)
	route.accept()
	waitRouteStatus(t, p, m.EventID, "sent")
	req := newRoute("two-topics", []string{m.MessageID}, f.dir)
	req.Targets = append(req.Targets, core.RouteTarget{
		Create: &core.NewTopic{Title: "Separate scope", Workspace: f.dir}, MessageIDs: req.MessageIDs,
	})
	decision := decide(t, p, routerToken, req)
	targets := map[string]core.Binding{}
	for _, target := range decision.Targets {
		binding := waitBinding(t, p, "topic", target.TopicID, "ready")
		targets[binding.SessionID] = binding
	}
	confirmed := peer.next(t, "turn/start")
	unconfirmed := peer.next(t, "turn/start")
	a, b := targets[confirmed.session], targets[unconfirmed.session]
	require.NotEmpty(t, a.OwnerID)
	require.NotEmpty(t, b.OwnerID)
	require.NotEqual(t, a.OwnerID, b.OwnerID)
	aID := decodeInternalBatch(t, confirmed)[0].ID
	bID := decodeInternalBatch(t, unconfirmed)[0].ID
	confirmed.accept()
	waitRouteStatus(t, p, aID, "sent")
	waitRouteStatus(t, p, bID, "dispatching")
	p.stop(t, true)
	unconfirmed.accept()

	p = f.start()
	replayed := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, replayed.session)
	decodeInternalBatch(t, replayed, bID)
	require.JSONEq(t, unconfirmed.text, replayed.text)
	waitRouteStatus(t, p, bID, "dispatching")
	aToken, bToken := roleToken(t, f, a), roleToken(t, f, b)
	ret := core.ReturnRequest{DeliveryID: bID, Reason: "The second scope needs Router review"}
	status, raw := request(t, http.MethodPost, p.endpoint, "/v1/role/message_return", aToken, ret)
	errorCode(t, status, raw, http.StatusForbidden, "forbidden")
	returned := roleCall(t, p, bToken, "message_return", ret)
	returnID := readReturnID(t, returned)
	require.JSONEq(t, string(returned), string(roleCall(t, p, bToken, "message_return", ret)))
	waitRouteStatus(t, p, bID, "superseded")
	replayed.accept()
	routerInput := peer.next(t, "turn/start")
	require.Equal(t, router.SessionID, routerInput.session)
	event := decodeInternalBatch(t, routerInput, returnID)[0]
	require.Equal(t, "route_request", event.Kind)
	require.Nil(t, event.Content)
	require.JSONEq(t, fmt.Sprintf(`{"kind":"trigger_message","id":%q}`, m.MessageID), string(event.Refs[0]))
	require.JSONEq(t, fmt.Sprintf(`{"kind":"internal_delivery","id":%q}`, bID), string(event.Refs[1]))
	lookup := core.QueryRequest{Collection: "messages", ID: m.MessageID}
	value := roleCall(t, p, routerToken, "source_query", lookup)
	var detail struct {
		Routing   struct{ Round int }
		Relations []struct{ Reason string }
	}
	require.NoError(t, json.Unmarshal(value, &detail))
	require.Equal(t, 2, detail.Routing.Round)
	require.Condition(t, func() bool {
		for _, relation := range detail.Relations {
			if relation.Reason == ret.Reason {
				return true
			}
		}
		return false
	})
	routerInput.accept()
	waitRouteStatus(t, p, returnID, "sent")
	require.True(t, waitRouteStatus(t, p, bID, "superseded").Returned)
	require.False(t, waitRouteStatus(t, p, aID, "sent").Returned)

	req.OperationID = "two-topics-reconsidered"
	req.Rounds[m.MessageID] = 2
	req.Targets = []core.RouteTarget{
		{TopicID: a.OwnerID, MessageIDs: req.MessageIDs},
		{TopicID: b.OwnerID, MessageIDs: req.MessageIDs},
	}
	rerouted := decide(t, p, routerToken, req)
	require.Equal(t, aID, rerouted.Targets[0].DeliveryIDs[m.MessageID])
	newID := rerouted.Targets[1].DeliveryIDs[m.MessageID]
	require.NotEqual(t, bID, newID)
	replacement := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, replacement.session)
	decodeInternalBatch(t, replacement, newID)
	replacement.accept()
	waitRouteStatus(t, p, newID, "sent")
	require.JSONEq(t, string(returned), string(roleCall(t, p, bToken, "message_return", ret)))

	var own core.Page
	raw = roleCall(t, p, bToken, "source_query", core.QueryRequest{Collection: "messages"})
	require.NoError(t, json.Unmarshal(raw, &own))
	require.Len(t, own.Items, 1)
	raw = roleCall(t, p, bToken, "source_query", core.QueryRequest{Collection: "routing-deliveries", MessageID: m.MessageID})
	require.NoError(t, json.Unmarshal(raw, &own))
	require.Len(t, own.Items, 3)
	_, raw = request(t, http.MethodGet, p.endpoint, "/v1/observer/threads/"+url.PathEscape(m.ThreadKey), "", nil)
	require.Contains(t, string(raw), a.OwnerID)
	require.Contains(t, string(raw), b.OwnerID)
	peer.quiet(t, 300*time.Millisecond)
}
