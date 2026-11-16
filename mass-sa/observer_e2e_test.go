package sa

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func observerFixture(t *testing.T) (Config, *fakeMASSServer) {
	t.Helper()
	mass := newFakeMASSServer(t, "")
	alpha := newTestProject("alpha")
	alpha.Name = "Alpha"
	alpha.CWD = t.TempDir()
	alpha.Memory = `Inspect work safely. <img src=https://invalid.example/secret onerror=alert(1)> token=fixture-memory-secret {"api_key":"fixture-json-secret"}`
	beta := newTestProject("beta")
	beta.Name = "Beta"
	beta.CWD = t.TempDir()
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), alpha, beta)
	config.Management.Listen = freeLoopbackAddress(t)
	config.Management.TokenFile = filepath.Join(t.TempDir(), "management-credential")
	if err := os.WriteFile(config.Management.TokenFile, []byte("fixture-management-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	mass.addSession(Session{
		Metadata: SessionMetadata{Namespace: RouterNamespace, Name: RouterName(alpha)},
		Spec:     SessionSpec{RuntimeRef: alpha.RuntimeRef, Cwd: alpha.CWD, Config: &InitialSessionConfig{Model: "observed-model", Effort: "high"}, MCPServers: []MCPServer{{Name: "private", Args: []string{"fixture-mcp-secret", "/private/capability-path"}}}},
		Status:   SessionStatus{SessionID: "fixture-router-session", State: "idle", Reason: "fixture-upstream-secret"},
	})
	store, err := OpenStateStore(config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRouter("alpha", RouterState{
		SessionRef: SessionRef{SessionID: "fixture-router-session", Namespace: RouterNamespace, Name: RouterName(alpha)},
		RuntimeRef: alpha.RuntimeRef, ObservedState: "offline", CapabilityFile: "/private/capability-path", CapabilityDigest: "fixture-capability-secret", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return config, mass
}

func startObserverProcess(t *testing.T, config Config) (string, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(t.TempDir(), "observer.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(massSABinary, "serve", "--observer-only", "--config", configPath)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("observer stop: %v", err)
			}
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("observer did not stop")
		}
		_ = logFile.Close()
	})
	base := "http://" + config.Management.Listen
	waitT06(t, 10*time.Second, "observer-only startup", func() bool {
		response, err := http.Get(base + "/observer/api/projects")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	})
	return base, configPath
}

func observerGet[T any](t *testing.T, base, path string) T {
	t.Helper()
	response, err := http.Get(base + "/observer/api/" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s: %d: %s", path, response.StatusCode, raw)
	}
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestObserverPublicHTTPCLIReadOnlyAndSecurity(t *testing.T) {
	config, mass := observerFixture(t)
	before, err := os.ReadFile(config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	base, configPath := startObserverProcess(t, config)
	page := observerGet[ObserverPage[ObserverRouter]](t, base, "routers")
	if len(page.Items) != 2 || page.Items[0].Project.ID != "alpha" || page.Items[1].Project.ID != "beta" {
		t.Fatalf("configured routers missing: %+v", page)
	}
	first := page.Items[0]
	second := page.Items[1]
	if first.Persisted == nil || first.Persisted.ObservedState != "offline" || first.Observation.Session.State != "idle" || first.Observation.Session.Config.Model != "observed-model" {
		t.Fatalf("configuration/reference/observation collapsed: %+v", first)
	}
	if second.Persisted != nil || second.Observation.Status != "not_found" {
		t.Fatalf("unestablished router missing: %+v", second)
	}
	cli := runT06JSON[ObserverPage[ObserverRouter]](t, "observe", "routers", "--config", configPath)
	if len(cli.Items) != 2 || cli.Items[0].Observation.Session.SessionID != first.Observation.Session.SessionID || cli.Items[1].Observation.Status != "not_found" {
		t.Fatalf("CLI projection differs: %+v", cli)
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"fixture-management-secret", "fixture-memory-secret", "fixture-json-secret", "fixture-capability-secret", "fixture-mcp-secret", "fixture-upstream-secret", "/private/capability-path", config.Management.TokenFile, "tokenFile", "MCPServers", "capabilityDigest"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("browser projection exposed %q", secret)
		}
	}
	if !strings.Contains(first.Instructions, "[redacted]") {
		t.Error("known secret form was not masked")
	}
	if !strings.Contains(first.Instructions, "<img") {
		t.Error("fixture must exercise escaped free text in the browser")
	}
	// Host, port, Origin and Fetch Metadata are checked on both assets and reads.
	for _, p := range []string{"/observer/", "/observer/api/routers"} {
		for _, test := range []struct{ name, host, origin, site string }{
			{name: "foreign-host", host: "attacker.invalid:" + strings.Split(config.Management.Listen, ":")[1]},
			{name: "wrong-port", host: "127.0.0.1:1"},
			{name: "foreign-origin", origin: "https://attacker.invalid"},
			{name: "null-origin", origin: "null"},
			{name: "cross-site", site: "cross-site"},
			{name: "same-site", site: "same-site"},
		} {
			request, _ := http.NewRequest(http.MethodGet, base+p, nil)
			if test.host != "" {
				request.Host = test.host
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.site != "" {
				request.Header.Set("Sec-Fetch-Site", test.site)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 403 {
				t.Errorf("%s %s: status %d", p, test.name, response.StatusCode)
			}
		}
	}
	request, _ := http.NewRequest(http.MethodGet, base+"/observer/api/routers", nil)
	request.Header.Set("Origin", base)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Errorf("same origin: %d", response.StatusCode)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		request, _ := http.NewRequest(method, base+"/observer/api/routers", nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 405 {
			t.Errorf("%s accepted: %d", method, response.StatusCode)
		}
	}
	for _, p := range []string{"/v1/management/health", "/v1/management/observe/routers", "/v1/tools/alpha"} {
		method := http.MethodGet
		if strings.HasPrefix(p, "/v1/tools/") {
			method = http.MethodPost
		}
		request, _ := http.NewRequest(method, base+p, nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 401 {
			t.Errorf("original authentication changed for %s: %d", p, response.StatusCode)
		}
	}
	for _, p := range []string{"routers?rpc=session.resume", "routers?limit=201", "routers?cursor=bad", "routers?project=alpha&project=beta"} {
		response, err := http.Get(base + "/observer/api/" + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Errorf("invalid query %s: %d", p, response.StatusCode)
		}
	}
	for _, p := range []string{"session.resume", "outputs/ack", "routers/alpha/dispatch"} {
		response, err := http.Get(base + "/observer/api/" + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 404 {
			t.Errorf("unlisted resource %s: %d", p, response.StatusCode)
		}
	}
	firstPage := observerGet[ObserverPage[ObserverProject]](t, base, "projects?limit=1")
	next := observerGet[ObserverPage[ObserverProject]](t, base, "projects?limit=1&cursor="+url.QueryEscape(firstPage.Next))
	if firstPage.Items[0].ID != "alpha" || next.Items[0].ID != "beta" || next.Next != "" {
		t.Fatal("unstable project traversal")
	}
	response, err = http.Get(base + "/observer/api/projects?project=alpha&cursor=" + url.QueryEscape(firstPage.Next))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 400 {
		t.Error("cursor reused across filters")
	}
	// Keep the last successful value while reporting a new failed observation.
	mass.mu.Lock()
	mass.getError = &OperationError{Code: "unavailable", Reason: "injected", Message: "fixture-error-secret"}
	mass.mu.Unlock()
	stale := observerGet[ObserverRouter](t, base, "routers/alpha")
	if !stale.Observation.Stale || stale.Observation.Status != "error" || stale.Observation.LastSuccessAt == nil || stale.Observation.Session.SessionID != "fixture-router-session" || strings.Contains(stale.Observation.Error, "fixture-error-secret") {
		t.Fatalf("stale/error evidence missing: %+v", stale.Observation)
	}
	mass.server.Close()
	disconnected := observerGet[ObserverRouter](t, base, "routers/alpha")
	if !disconnected.Observation.Stale || disconnected.Observation.Status != "error" || disconnected.Observation.Session.SessionID != "fixture-router-session" {
		t.Fatalf("disconnect lost the last observation: %+v", disconnected.Observation)
	}
	health := observerGet[map[string]any](t, base, "health")
	if health["massd"] != "error" || health["massSa"] != "reachable" {
		t.Fatalf("disconnect health was hidden: %+v", health)
	}
	after, _ := os.ReadFile(config.StateFile)
	if !bytes.Equal(before, after) {
		t.Error("observation changed coordination state")
	}
	mass.mu.Lock()
	operations := append([]string(nil), mass.operations...)
	mass.mu.Unlock()
	for _, op := range operations {
		if op != "initialize" && op != "session.get" {
			t.Errorf("observer invoked %s", op)
		}
	}
	response, err = http.Get(base + "/observer/routers")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	_, assetsErr := observerAssets()
	if assetsErr != nil {
		if response.StatusCode != 503 || !bytes.Contains(body, []byte("not built")) {
			t.Fatal("unbuilt frontend falsely available")
		}
	} else if response.StatusCode != 200 || !bytes.Contains(body, []byte("/observer/assets/")) {
		t.Fatal("production SPA not embedded")
	}
}

func configWithNewAddress(t *testing.T, config Config) Config {
	config.Management.Listen = freeLoopbackAddress(t)
	return config
}

// Playwright and agent-browser use this isolated production-asset fixture. No
// real MASS, credentials or Agent execution are needed.
func TestObserverBrowserFixture(t *testing.T) {
	if os.Getenv("OBSERVER_BROWSER_TEST") != "1" {
		t.Skip("explicit browser fixture only")
	}
	config, mass := observerFixture(t)
	seedObserverTriggerFixture(t, config)
	seedObserverTicketFixture(t, config, mass)
	seedObserverOutputFixture(t, config)
	seedObserverIntegratedFixture(t, config, mass)
	before, err := os.ReadFile(config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	massBefore := len(mass.operationOrder())

	t.Cleanup(func() {
		after, err := os.ReadFile(config.StateFile)
		if err != nil || !bytes.Equal(before, after) {
			t.Error("browser observation changed durable state, consumers or checkpoints")
		}
		for _, operation := range mass.operationOrder()[massBefore:] {
			switch operation {
			case "session.get", "session.list", "session.history", "session.subscribe", "session.unsubscribe", "runtime.list", "runtime.get":
			default:
				t.Errorf("browser observation mutated MASS: %s", operation)
			}
		}
	})
	port := os.Getenv("OBSERVER_BROWSER_PORT")
	if port == "" {
		port = "18788"
	}
	config.Management.Listen = "127.0.0.1:" + port
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := RunServe(ctx, config, WithObserverOnly()); err != nil {
		t.Fatal(err)
	}
}
