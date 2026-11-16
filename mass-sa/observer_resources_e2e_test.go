package sa

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type resourcePeer struct {
	server        *httptest.Server
	mu            sync.Mutex
	writeMu       sync.Mutex
	sessions      []Session
	history       []SessionEvent
	active        map[*websocket.Conn]string
	operations    []string
	fail          bool
	gate          chan struct{}
	preAck        int
	extraRuntimes int
}

func newResourcePeer(t *testing.T) *resourcePeer {
	p := &resourcePeer{active: map[*websocket.Conn]string{}}
	p.sessions = []Session{
		{Metadata: SessionMetadata{Namespace: "shared", Name: "current"}, Spec: SessionSpec{RuntimeRef: "runtime-a", Config: &InitialSessionConfig{Model: "persisted-model", Effort: "high"}}, Status: SessionStatus{SessionID: "session-current", State: "running"}},
		{Metadata: SessionMetadata{Namespace: "shared", Name: "old"}, Spec: SessionSpec{RuntimeRef: "runtime-a"}, Status: SessionStatus{SessionID: "session-old", State: "offline"}},
		{Metadata: SessionMetadata{Namespace: "shared", Name: "unowned"}, Spec: SessionSpec{RuntimeRef: "runtime-b"}, Status: SessionStatus{SessionID: "session-unowned", State: "offline"}},
		{Metadata: SessionMetadata{Namespace: RouterNamespace, Name: "old-router"}, Spec: SessionSpec{RuntimeRef: "runtime-c"}, Status: SessionStatus{SessionID: "session-router-old", State: "offline"}},
		{Metadata: SessionMetadata{Namespace: "shared", Name: "opaque"}, Spec: SessionSpec{RuntimeRef: "runtime-b"}, Status: SessionStatus{SessionID: "opaque/with space%?", State: "offline"}},
	}
	for i := 0; i < 120; i++ {
		p.history = append(p.history, resourceEvent(i, "agent.message", fmt.Sprintf("historical text %d", i)))
	}
	upgrader := websocket.Upgrader{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer func() { p.mu.Lock(); delete(p.active, conn); p.mu.Unlock() }()
		initialized := false
		for {
			var req fakeRPCRequest
			if conn.ReadJSON(&req) != nil {
				return
			}
			p.mu.Lock()
			p.operations = append(p.operations, req.Method)
			fail := p.fail
			p.mu.Unlock()
			if fail {
				return
			}
			if req.Method == "initialize" {
				if initialized {
					return
				}
				initialized = true
				p.result(conn, req.ID, map[string]any{"protocolVersion": "1", "serverInfo": map[string]string{"name": "resource-peer", "version": "test"}})
				continue
			}
			if !initialized {
				return
			}
			switch req.Method {
			case "runtime.list", "runtime.get":
				runtimes := []map[string]any{
					{"metadata": map[string]string{"name": "runtime-a", "namespace": ""}, "spec": map[string]any{"type": "claude", "config": map[string]any{"model": "runtime-model", "env": map[string]string{"PRIVATE": "fixture-env-secret"}, "headers": map[string]string{"Authorization": "fixture-header-secret"}, "mcpServers": []string{"fixture-mcp-secret"}, "future": "fixture-future-secret"}}, "status": map[string]string{"state": "running"}},
					{"metadata": map[string]string{"name": "runtime-b", "namespace": ""}, "spec": map[string]any{"type": "codex", "config": map[string]any{}}, "status": map[string]string{"state": "stopped"}},
					{"metadata": map[string]string{"name": "runtime-c", "namespace": ""}, "spec": map[string]any{"type": "claude", "config": map[string]any{}}, "status": map[string]string{"state": "stopped"}},
				}
				p.mu.Lock()
				extra := p.extraRuntimes
				p.mu.Unlock()
				for i := 0; i < extra; i++ {
					runtimes = append(runtimes, map[string]any{"metadata": map[string]string{"name": fmt.Sprintf("runtime-extra-%03d", i)}, "spec": map[string]any{"type": "codex", "config": map[string]any{}}, "status": map[string]string{"state": "stopped"}})
				}
				var params struct {
					RuntimeRef string `json:"runtimeRef"`
					Query      struct {
						Type  string `json:"type"`
						State string `json:"state"`
					} `json:"query"`
				}
				_ = json.Unmarshal(req.Params, &params)
				if req.Method == "runtime.get" {
					found := false
					for _, row := range runtimes {
						if row["metadata"].(map[string]string)["name"] == params.RuntimeRef {
							p.result(conn, req.ID, row)
							found = true
							break
						}
					}
					if !found {
						p.notFound(conn, req.ID)
					}
					continue
				}
				selected := []map[string]any{}
				for _, row := range runtimes {
					if (params.Query.Type == "" || params.Query.Type == row["spec"].(map[string]any)["type"]) && (params.Query.State == "" || params.Query.State == row["status"].(map[string]string)["state"]) {
						selected = append(selected, row)
					}
				}
				p.result(conn, req.ID, selected)
			case "session.list":
				var params struct {
					Query struct {
						Namespace string `json:"namespace"`
						Runtime   string `json:"runtimeRef"`
						State     string `json:"state"`
						Page      struct {
							After string `json:"after"`
							Limit int    `json:"limit"`
						} `json:"page"`
					} `json:"query"`
				}
				_ = json.Unmarshal(req.Params, &params)
				rows := []Session{}
				for _, s := range p.sessions {
					q := params.Query
					if (q.Namespace == "" || s.Metadata.Namespace == q.Namespace) && (q.Runtime == "" || s.Spec.RuntimeRef == q.Runtime) && (q.State == "" || s.Status.State == q.State) {
						rows = append(rows, s)
					}
				}
				start := 0
				if params.Query.Page.After != "" {
					var e error
					start, e = strconv.Atoi(strings.TrimPrefix(params.Query.Page.After, "native-session-"))
					if e != nil || start > len(rows) {
						p.notFound(conn, req.ID)
						continue
					}
				}
				limit := params.Query.Page.Limit
				if limit < 1 {
					limit = 50
				}
				end := min(start+limit, len(rows))
				next := ""
				if end < len(rows) {
					next = fmt.Sprintf("native-session-%d", end)
				}
				p.result(conn, req.ID, map[string]any{"items": rows[start:end], "next": next})
			case "session.get":
				var params struct {
					Session SessionRef `json:"session"`
				}
				_ = json.Unmarshal(req.Params, &params)
				found := false
				for _, s := range p.sessions {
					if s.Status.SessionID == params.Session.SessionID {
						raw, _ := json.Marshal(s)
						var row map[string]any
						_ = json.Unmarshal(raw, &row)
						row["spec"].(map[string]any)["mcpServers"] = []any{map[string]any{"headers": map[string]string{"Authorization": "fixture-header-secret"}, "args": []string{"/fixture/capability-secret"}}}
						p.result(conn, req.ID, row)
						found = true
						break
					}
				}
				if !found {
					p.notFound(conn, req.ID)
				}
			case "session.subscribe":
				var params struct {
					Session SessionRef `json:"session"`
				}
				_ = json.Unmarshal(req.Params, &params)
				p.mu.Lock()
				p.active[conn] = params.Session.SessionID
				preAck := p.preAck
				p.mu.Unlock()
				for i := 0; i < preAck; i++ {
					p.writeMu.Lock()
					_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"subscriptionId": "observation", "event": resourceEvent(300+i, "agent.message_chunk", "before acknowledgement")}})
					p.writeMu.Unlock()
				}
				p.result(conn, req.ID, map[string]string{"subscriptionId": "observation"})
			case "session.history":
				var params struct {
					Session SessionRef `json:"session"`
					Query   struct {
						After string   `json:"after"`
						Limit int      `json:"limit"`
						Types []string `json:"types"`
					} `json:"query"`
				}
				_ = json.Unmarshal(req.Params, &params)
				p.mu.Lock()
				gate := p.gate
				all := append([]SessionEvent(nil), p.history...)
				p.mu.Unlock()
				if gate != nil {
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
				}
				start := 0
				if params.Query.After != "" {
					var e error
					start, e = strconv.Atoi(strings.TrimPrefix(params.Query.After, "native-history-"))
					if e != nil || start > len(all) {
						p.notFound(conn, req.ID)
						continue
					}
				}
				limit := params.Query.Limit
				if limit < 1 {
					limit = 100
				}
				rows := []SessionEvent{}
				last := start
				more := false
				if params.Session.SessionID == "session-current" {
					for i := start; i < len(all); i++ {
						match := len(params.Query.Types) == 0
						for _, kind := range params.Query.Types {
							if all[i].Type == kind {
								match = true
							}
						}
						if !match {
							continue
						}
						if len(rows) == limit {
							more = true
							break
						}
						rows = append(rows, all[i])
						last = i + 1
					}
				}
				next := ""
				if more {
					next = fmt.Sprintf("native-history-%d", last)
				}
				var synced *string
				if len(all) > 0 {
					synced = &all[len(all)-1].EventID
				}
				p.result(conn, req.ID, HistoryPage{Events: rows, Next: next, SyncedEventID: synced})
			default:
				t.Errorf("mutation or unexpected method: %s", req.Method)
				return
			}
		}
	}))
	t.Cleanup(func() { p.drop(); p.server.Close() })
	return p
}

func TestObserverRuntimeBoundedTraversal(t *testing.T) {
	config, peer := resourceFixture(t)
	peer.mu.Lock()
	peer.extraRuntimes = 202
	peer.mu.Unlock()
	base, _ := startObserverProcess(t, config)
	first := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all")
	require.Len(t, first.Items, 50, "Observer default applies even though MASS returns the whole Runtime list")
	require.NotEmpty(t, first.Next)
	maxPage := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all&limit=200")
	require.Len(t, maxPage.Items, 200)
	peer.mu.Lock()
	peer.extraRuntimes = 0
	peer.fail = true
	peer.mu.Unlock()
	tail := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all&limit=200&cursor="+url.QueryEscape(maxPage.Next))
	require.Len(t, tail.Items, 5, "continuation uses the frozen snapshot, including after upstream failure")
	require.Empty(t, tail.Next)
	require.Equal(t, maxPage.ObservedAt, tail.ObservedAt)
	seen := map[string]bool{}
	for _, row := range append(maxPage.Items, tail.Items...) {
		require.False(t, seen[row.Name], "Runtime identities must appear exactly once")
		seen[row.Name] = true
	}
	require.Len(t, seen, 205)
	for _, q := range []string{"limit=201", "limit=0", "scope=associated&cursor=" + url.QueryEscape(first.Next)} {
		response, err := http.Get(base + "/observer/api/runtimes?" + q)
		require.NoError(t, err)
		require.Equal(t, 400, response.StatusCode)
		_ = response.Body.Close()
	}
}
func (p *resourcePeer) result(conn *websocket.Conn, id json.RawMessage, result any) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func (p *resourcePeer) notFound(conn *websocket.Conn, id json.RawMessage) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": "not found", "data": map[string]string{"code": "not_found", "reason": "missing", "message": "fixture-raw-error-secret"}}})
}
func resourceEvent(n int, kind, text string) SessionEvent {
	raw, _ := json.Marshal(map[string]any{"messageId": "message-" + strconv.Itoa(n), "content": []map[string]string{{"type": "text", "text": text}}})
	return SessionEvent{EventID: fmt.Sprintf("ffffffff-ffff-4fff-8fff-%012d", 999999-n), Namespace: "shared", SessionName: "current", SessionID: "session-current", ItemID: "item-" + strconv.Itoa(n), Type: kind, Time: time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC).Add(time.Duration(-n) * time.Second), Payload: raw, Meta: map[string]any{"private": "fixture-meta-secret"}}
}
func (p *resourcePeer) emit(e SessionEvent, persist bool) {
	p.mu.Lock()
	if persist {
		p.history = append(p.history, e)
	}
	targets := []*websocket.Conn{}
	for conn, id := range p.active {
		if id == e.SessionID {
			targets = append(targets, conn)
		}
	}
	p.mu.Unlock()
	for _, conn := range targets {
		p.writeMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"subscriptionId": "observation", "event": e}})
		p.writeMu.Unlock()
	}
}
func (p *resourcePeer) drop() {
	p.mu.Lock()
	connections := []*websocket.Conn{}
	for conn := range p.active {
		connections = append(connections, conn)
	}
	p.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}
func (p *resourcePeer) activeCount() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.active) }
func resourceFixture(t *testing.T) (Config, *resourcePeer) {
	config, _ := observerFixture(t)
	peer := newResourcePeer(t)
	config.MASS.Endpoint = "ws" + strings.TrimPrefix(peer.server.URL, "http") + "/rpc"
	config.Projects = config.Projects[:1]
	config.Projects[0].RuntimeRef = "runtime-a"
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		p.Assignments = map[string]AssignmentState{"a-old": {ID: "a-old", ProjectID: "alpha", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-old"}, RuntimeRef: "runtime-a", Model: "old-request"}, "a-current": {ID: "a-current", ProjectID: "alpha", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-current"}, RuntimeRef: "runtime-a", Model: "request-model"}, "a-same-session": {ID: "a-same-session", ProjectID: "alpha", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-current"}, RuntimeRef: "runtime-a", Model: "prior-request"}}
		p.Tickets = map[string]TicketState{"ticket-a": {ID: "ticket-a", AssignmentID: "a-current"}}
		p.Deliveries = map[string]DeliveryBatchState{"router-old": {ID: "router-old", RoutingPath: "router", TargetSession: SessionRef{SessionID: "session-router-old"}}}
		return nil
	}))
	return config, peer
}

type sseFrame struct {
	kind string
	data json.RawMessage
}

func readSSE(t *testing.T, body io.ReadCloser) <-chan sseFrame {
	t.Helper()
	out := make(chan sseFrame, 4096)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		frame := sseFrame{}
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				frame.kind = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				frame.data = append([]byte(nil), strings.TrimPrefix(line, "data: ")...)
			}
			if line == "" && frame.kind != "" {
				out <- frame
				frame = sseFrame{}
			}
		}
	}()
	return out
}
func awaitSSE(t *testing.T, frames <-chan sseFrame, kind string, predicate func(json.RawMessage) bool) json.RawMessage {
	t.Helper()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-frames:
			require.True(t, ok, "SSE ended while waiting for %s", kind)
			if frame.kind == kind && (predicate == nil || predicate(frame.data)) {
				return frame.data
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for SSE %s", kind)
			return nil
		}
	}
}

func TestObserverResourcesPublicProcessCLI(t *testing.T) {
	config, peer := resourceFixture(t)
	tool := resourceEvent(125, "agent.tool_call", "")
	tool.Payload = json.RawMessage(`{"id":"tool-125","toolName":"shell","provider":"agent","kind":"execute","rawInput":{"count":9007199254740993,"env":{"PRIVATE":"fixture-tool-env-secret"},"headers":{"Authorization":"fixture-tool-header-secret"},"capabilityFile":"/fixture/private-capability-secret","query":"echo visible"}}`)
	peer.mu.Lock()
	peer.history = append(peer.history, tool)
	peer.mu.Unlock()
	before, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	base, path := startObserverProcess(t, config)
	associated := observerGet[ObserverPage[ObserverResourceSession]](t, base, "sessions")
	require.Len(t, associated.Items, 3)
	all := observerGet[ObserverPage[ObserverResourceSession]](t, base, "sessions?scope=all&namespace=shared&state=offline")
	require.Len(t, all.Items, 3)
	cli := runT06JSON[ObserverPage[ObserverResourceSession]](t, "observe", "sessions", "--config", path, "--scope", "all", "--namespace", "shared", "--state", "offline")
	require.Equal(t, all.Items, cli.Items)
	current := observerGet[ObserverResourceSession](t, base, "sessions/session-current")
	require.Len(t, current.Associations, 2)
	require.True(t, current.Associations[0].Current)
	require.False(t, current.Associations[1].Current)
	require.Equal(t, "persisted-model", current.Config.Model)
	require.Empty(t, current.ProviderModel)
	old := observerGet[ObserverResourceSession](t, base, "sessions/session-router-old")
	require.Equal(t, "router", old.Associations[0].Role)
	require.False(t, old.Associations[0].Current)
	opaque := runT06JSON[ObserverResourceSession](t, "observe", "session", "opaque/with space%?", "--scope", "all", "--config", path)
	require.Equal(t, "opaque/with space%?", opaque.SessionID)
	first := observerGet[ObserverPage[ObserverResourceSession]](t, base, "sessions?scope=all&limit=2")
	require.NotEmpty(t, first.Next)
	second := observerGet[ObserverPage[ObserverResourceSession]](t, base, "sessions?scope=all&limit=2&cursor="+url.QueryEscape(first.Next))
	require.Equal(t, "session-unowned", second.Items[0].SessionID)
	response, err := http.Get(base + "/observer/api/sessions?scope=associated&limit=2&cursor=" + url.QueryEscape(first.Next))
	require.NoError(t, err)
	require.Equal(t, 400, response.StatusCode)
	_ = response.Body.Close()
	runtimes := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes")
	require.Len(t, runtimes.Items, 2)
	require.Equal(t, "runtime-c", runtimes.Items[1].Name)
	firstRuntime := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all&limit=1")
	require.Len(t, firstRuntime.Items, 1)
	require.Equal(t, "runtime-a", firstRuntime.Items[0].Name)
	require.NotEmpty(t, firstRuntime.Next)
	secondRuntime := runT06JSON[ObserverPage[ObserverRuntime]](t, "observe", "runtimes", "--scope", "all", "--limit", "1", "--cursor", firstRuntime.Next, "--config", path)
	require.Equal(t, "runtime-b", secondRuntime.Items[0].Name)
	lastRuntime := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all&limit=1&cursor="+url.QueryEscape(secondRuntime.Next))
	require.Equal(t, "runtime-c", lastRuntime.Items[0].Name)
	require.Empty(t, lastRuntime.Next)
	mismatch, err := http.Get(base + "/observer/api/runtimes?scope=associated&cursor=" + url.QueryEscape(firstRuntime.Next))
	require.NoError(t, err)
	require.Equal(t, 400, mismatch.StatusCode)
	_ = mismatch.Body.Close()
	allRuntimes := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?scope=all&type=codex&state=stopped")
	require.Len(t, allRuntimes.Items, 1)
	runtimeCLI := runT06JSON[ObserverRuntime](t, "observe", "runtime", "runtime-a", "--config", path)
	require.Equal(t, runtimes.Items[0], runtimeCLI)
	history := observerGet[ObserverHistory](t, base, "sessions/session-current/history")
	require.Len(t, history.Events, 21)
	require.Equal(t, "historical text 100", history.Events[0].Text)
	require.Equal(t, "native-history-100", history.Cursor)
	require.Len(t, history.EarlierCursors, 2)
	earlier := observerGet[ObserverHistory](t, base, "sessions/session-current/history?window=page&cursor="+url.QueryEscape(history.EarlierCursors[1]))
	require.Equal(t, "historical text 50", earlier.Events[0].Text)
	historyCLI := runT06JSON[ObserverHistory](t, "observe", "history", "session-current", "--config", path)
	require.Equal(t, history.Events, historyCLI.Events)
	require.Contains(t, history.Events[len(history.Events)-1].Payload["rawInput"], "9007199254740993")
	raw, _ := json.Marshal([]any{current, runtimes, history})
	for _, secret := range []string{"fixture-env-secret", "fixture-header-secret", "fixture-mcp-secret", "fixture-future-secret", "fixture-meta-secret", "capability-secret", "fixture-raw-error-secret", "fixture-tool-env-secret", "fixture-tool-header-secret", "/fixture/private-capability-secret"} {
		require.NotContains(t, string(raw), secret)
	}
	for _, q := range []string{"sessions?scope=invalid", "sessions?scope=all&scope=all", "sessions?type=codex", "sessions/session-current/history?eventTypes=session.error", "runtimes?cursor=bad", "sessions?project=missing"} {
		res, e := http.Get(base + "/observer/api/" + q)
		require.NoError(t, e)
		require.GreaterOrEqual(t, res.StatusCode, 400)
		_ = res.Body.Close()
	}
	peer.mu.Lock()
	peer.fail = true
	peer.mu.Unlock()
	response, err = http.Get(base + "/observer/api/sessions")
	require.NoError(t, err)
	require.Equal(t, 503, response.StatusCode)
	_ = response.Body.Close()
	projects := observerGet[ObserverPage[ObserverProject]](t, base, "projects")
	require.Len(t, projects.Items, 1)
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestObserverSessionSSEOverlapRecoveryCancel(t *testing.T) {
	config, peer := resourceFixture(t)
	base, _ := startObserverProcess(t, config)
	peer.mu.Lock()
	peer.gate = make(chan struct{})
	gate := peer.gate
	peer.mu.Unlock()
	response, err := http.Get(base + "/observer/api/sessions/session-current/events")
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	defer response.Body.Close()
	frames := readSSE(t, response.Body)
	waitT06(t, 5*time.Second, "dedicated subscription", func() bool { return peer.activeCount() == 1 })
	preview := resourceEvent(120, "agent.message_chunk", "preview-only text")
	peer.emit(preview, false)
	complete := preview
	complete.Type = "agent.message"
	complete.Payload = json.RawMessage(`{"messageId":"message-120","content":[{"type":"text","text":"complete authoritative text <img src=https://invalid.example> token=fixture-event-secret"}]}`)
	peer.emit(complete, true)
	peer.emit(complete, false)
	close(gate)
	peer.mu.Lock()
	peer.gate = nil
	peer.mu.Unlock()
	history := awaitSSE(t, frames, "history", nil)
	require.Contains(t, string(history), "historical text 100")
	previewRaw := awaitSSE(t, frames, "preview", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("preview-only text")) })
	require.Contains(t, string(previewRaw), `"preview":true`)
	// The overlap's complete observation was already in the authoritative tail;
	// its later live copies must remain previews rather than duplicate facts.
	require.Contains(t, string(history), "complete authoritative text")
	require.NotContains(t, string(history), "fixture-event-secret")
	livePreview := resourceEvent(121, "agent.message_chunk", "second preview")
	peer.emit(livePreview, false)
	awaitSSE(t, frames, "preview", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("second preview")) })
	liveComplete := livePreview
	liveComplete.Type = "agent.message"
	liveComplete.Payload = json.RawMessage(`{"messageId":"message-121","content":[{"type":"text","text":"second complete authoritative fact"}]}`)
	peer.emit(liveComplete, true)
	durable := awaitSSE(t, frames, "history", func(raw json.RawMessage) bool {
		return bytes.Contains(raw, []byte("second complete authoritative fact"))
	})
	var tail ObserverHistory
	require.NoError(t, json.Unmarshal(durable, &tail))
	require.Equal(t, liveComplete.EventID, tail.Events[len(tail.Events)-1].EventID)
	checkpointRaw := awaitSSE(t, frames, "checkpoint", nil)
	var checkpoint struct {
		Cursor string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(checkpointRaw, &checkpoint))
	require.Equal(t, "native-history-100", checkpoint.Cursor)
	peer.mu.Lock()
	ops := append([]string(nil), peer.operations...)
	peer.mu.Unlock()
	subscribe, historyIndex := -1, -1
	for i, op := range ops {
		if op == "session.subscribe" && subscribe < 0 {
			subscribe = i
		}
		if op == "session.history" && historyIndex < 0 {
			historyIndex = i
		}
	}
	require.Less(t, subscribe, historyIndex)
	peer.drop()
	disconnected := awaitSSE(t, frames, "status", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("disconnected")) })
	require.Contains(t, string(disconnected), "previewGap")
	missing := resourceEvent(122, "agent.message", "written during disconnect")
	peer.emit(missing, true)
	reconnect, err := http.Get(base + "/observer/api/sessions/session-current/events?cursor=" + url.QueryEscape(checkpoint.Cursor))
	require.NoError(t, err)
	recovered := readSSE(t, reconnect.Body)
	backfill := awaitSSE(t, recovered, "history", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("written during disconnect")) })
	require.Contains(t, string(backfill), missing.EventID)
	require.NoError(t, reconnect.Body.Close())
	waitT06(t, 5*time.Second, "cancel releases observation", func() bool { return peer.activeCount() == 0 })
	peer.mu.Lock()
	defer peer.mu.Unlock()
	for _, method := range peer.operations {
		require.Contains(t, []string{"initialize", "session.list", "session.get", "runtime.list", "runtime.get", "session.subscribe", "session.history"}, method)
	}
}

func TestObserverBoundedSubscriptionSlowConsumer(t *testing.T) {
	config, peer := resourceFixture(t)
	client, err := OpenMASS(t.Context(), config.MASS)
	require.NoError(t, err)
	defer client.Close()
	sub, err := client.Subscribe(t.Context(), SessionRef{SessionID: "session-current"})
	require.NoError(t, err)
	defer sub.Close()
	for i := 0; i < massEventQueueCapacity+50; i++ {
		peer.emit(resourceEvent(200+i, "agent.message_chunk", "slow consumer"), false)
	}
	waitT06(t, 5*time.Second, "slow consumer releases dedicated connection", func() bool { return peer.activeCount() == 0 })
	for i := 0; i <= massEventQueueCapacity; i++ {
		_, err = sub.Next(t.Context())
		if err != nil {
			break
		}
	}
	require.True(t, IsErrorCode(err, "resource_exhausted"), "expected explicit slow_subscriber: %v", err)
	// The ordinary RPC connection survives the observation's eviction.
	_, err = client.GetSession(t.Context(), SessionRef{SessionID: "session-current"})
	require.NoError(t, err)
}

func TestObserverResourceBrowserFixture(t *testing.T) {
	if os.Getenv("OBSERVER_RESOURCE_BROWSER_TEST") != "1" {
		t.Skip("explicit resource browser fixture only")
	}
	config, peer := resourceFixture(t)
	config.Management.Listen = "127.0.0.1:18795"
	for i := 0; i < 55; i++ {
		peer.sessions = append(peer.sessions, Session{Metadata: SessionMetadata{Namespace: "fixture-extra", Name: fmt.Sprintf("bulk-%d", i)}, Spec: SessionSpec{RuntimeRef: "runtime-b"}, Status: SessionStatus{SessionID: fmt.Sprintf("bulk-%d", i), State: "offline"}})
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		n := 150
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e := resourceEvent(n, "agent.message_chunk", "live preview "+strconv.Itoa(n))
				peer.emit(e, false)
				e.Type = "agent.message"
				e.Payload = json.RawMessage(fmt.Sprintf(`{"messageId":"message-%d","content":[{"type":"text","text":"live complete %d <img src=https://invalid.example>"}]}`, n, n))
				peer.emit(e, true)
				n++
			}
		}
	}()
	if err := RunServe(ctx, config, WithObserverOnly()); err != nil {
		t.Fatal(err)
	}
}

func TestObserverPublicSSESlowConsumer(t *testing.T) {
	config, peer := resourceFixture(t)
	base, _ := startObserverProcess(t, config)
	peer.mu.Lock()
	peer.gate = make(chan struct{})
	gate := peer.gate
	peer.mu.Unlock()
	response, err := http.Get(base + "/observer/api/sessions/session-current/events")
	require.NoError(t, err)
	defer response.Body.Close()
	waitT06(t, 5*time.Second, "subscription before gated history", func() bool { return peer.activeCount() == 1 })
	for i := 0; i < massEventQueueCapacity+40; i++ {
		peer.emit(resourceEvent(400+i, "agent.message_chunk", "queued preview"), false)
	}
	waitT06(t, 5*time.Second, "overflow closes upstream observer", func() bool { return peer.activeCount() == 0 })
	close(gate)
	peer.mu.Lock()
	peer.gate = nil
	peer.mu.Unlock()
	frames := readSSE(t, response.Body)
	terminal := awaitSSE(t, frames, "status", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("slow_subscriber")) })
	require.Contains(t, string(terminal), "disconnected")
}
func TestObserverSubscriptionSetupIsBounded(t *testing.T) {
	config, peer := resourceFixture(t)
	peer.preAck = massEventQueueCapacity + 50
	client, err := OpenMASS(t.Context(), config.MASS)
	require.NoError(t, err)
	defer client.Close()
	sub, err := client.Subscribe(t.Context(), SessionRef{SessionID: "session-current"})
	if sub != nil {
		sub.Close()
	}
	require.True(t, IsErrorCode(err, "resource_exhausted"), "setup must report bounded queue failure: %v", err)
	waitT06(t, 5*time.Second, "setup overflow released observer", func() bool { return peer.activeCount() == 0 })
}

// Exercise the real net/http response controller with a short server-wide
// WriteTimeout, rather than waiting thirty seconds in every protocol test.
func TestObserverSSELocalWriteDeadline(t *testing.T) {
	config, peer := resourceFixture(t)
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	bridge, err := NewTicketBridge(config, store)
	require.NoError(t, err)
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	handler, err := NewObserverHandler(service, listener.Addr().String())
	require.NoError(t, err)
	server := &http.Server{Handler: handler, WriteTimeout: 200 * time.Millisecond, ReadHeaderTimeout: time.Second}
	t.Cleanup(func() { server.Close() })
	go func() { _ = server.Serve(listener) }()
	response, err := http.Get("http://" + listener.Addr().String() + "/observer/api/sessions/session-current/events")
	require.NoError(t, err)
	defer response.Body.Close()
	frames := readSSE(t, response.Body)
	awaitSSE(t, frames, "checkpoint", nil)
	timer := time.NewTimer(500 * time.Millisecond)
	<-timer.C
	peer.emit(resourceEvent(999, "agent.message_chunk", "past the server write timeout"), false)
	awaitSSE(t, frames, "preview", func(raw json.RawMessage) bool { return bytes.Contains(raw, []byte("past the server write timeout")) })
	require.NoError(t, response.Body.Close())
	waitT06(t, 5*time.Second, "local deadline test cancellation", func() bool { return peer.activeCount() == 0 })
}

func TestObserverSSEEmptyCursorRecoveryDoesNotSkipPages(t *testing.T) {
	config, peer := resourceFixture(t)
	peer.mu.Lock()
	peer.history = peer.history[:3]
	peer.mu.Unlock()
	base, _ := startObserverProcess(t, config)
	first, err := http.Get(base + "/observer/api/sessions/session-current/events")
	require.NoError(t, err)
	frames := readSSE(t, first.Body)
	raw := awaitSSE(t, frames, "checkpoint", nil)
	var checkpoint struct {
		Cursor string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(raw, &checkpoint))
	require.Empty(t, checkpoint.Cursor)
	require.NoError(t, first.Body.Close())
	waitT06(t, 5*time.Second, "first short observation closed", func() bool { return peer.activeCount() == 0 })
	for i := 0; i < 150; i++ {
		peer.emit(resourceEvent(600+i, "agent.message", fmt.Sprintf("disconnect gap %d", i)), true)
	}
	recovered, err := http.Get(base + "/observer/api/sessions/session-current/events?window=page")
	require.NoError(t, err)
	defer recovered.Body.Close()
	stream := readSSE(t, recovered.Body)
	count := 0
	foundFirstGap := false
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame := <-stream:
			if frame.kind == "history" {
				var page ObserverHistory
				require.NoError(t, json.Unmarshal(frame.data, &page))
				count += len(page.Events)
				for _, event := range page.Events {
					if event.Text == "disconnect gap 0" {
						foundFirstGap = true
					}
				}
			}
			if frame.kind == "checkpoint" {
				require.Equal(t, 153, count)
				require.True(t, foundFirstGap, "reconnect must not show only the recent tail")
				return
			}
		case <-timer.C:
			t.Fatal("recovery checkpoint missing")
			return
		}
	}
}
