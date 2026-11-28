package triggertest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
)

type CoreFixture struct {
	t                *testing.T
	Dir, Binary      string
	Endpoint, config string
	mcpFiles         string
	RoleToken        string
	started          time.Time
	Core             *Process
}

func NewCoreFixture(t *testing.T, clock bool) *CoreFixture {
	t.Helper()
	f := &CoreFixture{t: t, Dir: t.TempDir(), Binary: CorePath(t), started: time.Now().UTC()}
	if clock {
		f.Binary = os.Getenv("SUPERAGENT_CORE_CLOCK")
		if f.Binary == "" {
			t.Fatal("SUPERAGENT_CORE_CLOCK must name a real Core build with the coretest clock")
		}
		f.Advance(0)
	}
	// Core provisions the Router MCP credential. No Agent is launched
	// and no massd outcome is needed to enqueue a role-authored reply.
	unavailable := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(unavailable.Close)
	triggerTokenFile := filepath.Join(f.Dir, "test-source")
	massdTokenFile := filepath.Join(f.Dir, "test-massd")
	Write(t, triggerTokenFile, []byte(Token))
	Write(t, massdTokenFile, []byte("test-only-massd-token"))
	f.config = filepath.Join(f.Dir, "core.yaml")
	f.mcpFiles = filepath.Join(f.Dir, "data", "mcp-files")
	Write(t, f.config, fmt.Appendf(nil, `dataDir: %q
listen: %q
accessToken: {triggers: %q}
massd:
  endpoint: %q
  tokenFile: %q
sessions:
  namespace: trigger-fixture
  router:
    workspace: %q
    runtimeRef: test-runtime
    model: test-model
  topic:
    runtimeRef: test-runtime
    model: test-model
  issue:
    templates:
      - runtimeRef: test-runtime
        model: test-model
        description: Fixture Issue template
`, filepath.Join(f.Dir, "data"), FreeAddress(t), triggerTokenFile,
		"ws"+strings.TrimPrefix(unavailable.URL, "http")+"/jsonrpc", massdTokenFile, f.Dir))
	f.StartCore(clock)
	f.RoleToken = f.WaitRoleToken("router", "")
	return f
}

func (f *CoreFixture) WaitRoleToken(role, ownerID string) string {
	f.t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "trigger-fixture", "version": "1"},
		},
	}
	var token string
	Wait(f.t, role+" HTTP MCP credential", func() bool {
		paths, _ := filepath.Glob(filepath.Join(f.mcpFiles, "*.env"))
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			line, prefixed := strings.CutPrefix(string(data), "AUTHORIZATION='Bearer ")
			candidate, suffixed := strings.CutSuffix(line, "'\n")
			if !prefixed || !suffixed || candidate == "" {
				continue
			}
			var response struct {
				Result struct {
					Meta struct {
						Binding struct{ Role, OwnerID string } `json:"mass.dev/session-binding"`
					} `json:"_meta"`
				}
			}
			status := Request(f.t, http.MethodPost, f.Endpoint, "/v1/mcp", candidate, request, &response)
			binding := response.Result.Meta.Binding
			if status == http.StatusOK && binding.Role == role && binding.OwnerID == ownerID {
				token = candidate
				return true
			}
		}
		return false
	})
	return token
}

func (f *CoreFixture) StartCore(clock bool) {
	env := []string{}
	if clock {
		env = append(env, "SUPERAGENT_TEST_OUTBOX_CLOCK="+filepath.Join(f.Dir, "clock"))
	}
	p := Start(f.t, f.Binary, []string{"serve", "--config", f.config}, env)
	f.Endpoint = p.Endpoint(f.t, "endpoint")
	f.Core = p
}

func (f *CoreFixture) Advance(seconds int) {
	Write(f.t, filepath.Join(f.Dir, "clock"), []byte(f.started.Add(time.Duration(seconds)*time.Second).Format(time.RFC3339Nano)))
}

func Write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".next", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
}

func (f *CoreFixture) Proxy(hook func(http.ResponseWriter, *http.Request) bool, after func(*http.Response) error) *httptest.Server {
	f.t.Helper()
	target, err := url.Parse(f.Endpoint)
	if err != nil {
		f.t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
		},
		ModifyResponse: after,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook == nil || !hook(w, r) {
			proxy.ServeHTTP(w, r)
		}
	}))
	f.t.Cleanup(s.Close)
	return s
}

func Request(t *testing.T, method, endpoint, path, token string, body, result any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := trigger.HTTPClient(2 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if result != nil && json.NewDecoder(res.Body).Decode(result) != nil {
		t.Fatalf("invalid fixture response to %s %s", method, path)
	}
	return res.StatusCode
}

func CallRoleTool[T any](t *testing.T, endpoint, token, name string, arguments any) T {
	t.Helper()
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": arguments},
	}
	status := Request(t, http.MethodPost, endpoint, "/v1/mcp", token, request, &reply)
	if status != http.StatusOK || reply.JSONRPC != "2.0" || reply.ID != 1 || len(reply.Error) != 0 || reply.Result.IsError {
		t.Fatalf("Role MCP %s failed: HTTP %d, RPC error %s, tool result %s", name, status, reply.Error, reply.Result.StructuredContent)
	}
	var result T
	if err := json.Unmarshal(reply.Result.StructuredContent, &result); err != nil {
		t.Fatalf("decode Role MCP %s structured result: %v", name, err)
	}
	return result
}

type routingEvent struct {
	Kind      string `json:"kind"`
	Recipient struct {
		TopicID string `json:"topic_id"`
	} `json:"recipient"`
}

func (f *CoreFixture) RoutingEvents(m map[string]any) []routingEvent {
	f.t.Helper()
	var page struct {
		Items []routingEvent `json:"items"`
	}
	path := "/v1/core/internal-events?messageId=" + url.QueryEscape(m["id"].(string))
	if code := Request(f.t, "GET", f.Endpoint, path, "", nil, &page); code != http.StatusOK {
		f.t.Fatalf("read routing events: HTTP %d", code)
	}
	return page.Items
}

func ReadMessages(t *testing.T, endpoint string) []map[string]any {
	t.Helper()
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if code := Request(t, http.MethodGet, endpoint, "/v1/core/trigger-messages?limit=200", "", nil, &page); code != http.StatusOK {
		t.Fatalf("Core Messages: HTTP %d", code)
	}
	return page.Items
}

func (f *CoreFixture) AssertRouted(m map[string]any, want int) {
	f.t.Helper()
	events := f.RoutingEvents(m)
	if len(events) != want {
		f.t.Fatalf("routing events for %v: got %d want %d", m["sourceMessageId"], len(events), want)
	}
	for _, item := range events {
		if item.Kind != "route_request" {
			f.t.Fatalf("unexpected routing event kind: %s", item.Kind)
		}
	}
}
