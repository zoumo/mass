package subcommand

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/transport"
	"github.com/zoumo/mass/superagent/triggers"
)

const (
	composeFixtureDir    = "TEST_COMPOSE_FIXTURE_DIR"
	composeGitHubCoreEnv = "TEST_COMPOSE_GITHUB_CORE"
	composeGitHubEnv     = "TEST_COMPOSE_GITHUB_UPSTREAM"
	composeDWSCoreEnv    = "TEST_COMPOSE_DWS_CORE"
	composeTriggerToken  = "fixture-trigger-credential-shared-across-adapters"
	composeGitHubToken   = "fixture-github-graphql-credential"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "watchdog" {
		os.Exit(Execute())
	}
	if dir := os.Getenv(composeFixtureDir); dir != "" && len(os.Args) > 1 {
		os.Exit(runComposeFixture(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type composeFixtureEvent struct {
	Name         string
	PID          int
	Parent       int
	Args         []string
	Env          [3]bool
	TriggerToken bool
}

func composeTokenPath(dir, file string) string {
	return filepath.Join(dir, "core-state", "credentials", file)
}

func recordComposeEvent(dir, name string) {
	data, _ := os.ReadFile(composeTokenPath(dir, "triggers.token"))
	token := strings.TrimSpace(string(data))
	f, err := os.OpenFile(filepath.Join(dir, "events.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(composeFixtureEvent{
		Name: name, PID: os.Getpid(), Parent: os.Getppid(), Args: os.Args[1:],
		Env:          [3]bool{os.Getenv(composeGitHubCoreEnv) != "", os.Getenv(composeGitHubEnv) != "", os.Getenv(composeDWSCoreEnv) != ""},
		TriggerToken: token != "" && (os.Getenv(composeGitHubCoreEnv) == token || os.Getenv(composeDWSCoreEnv) == token),
	})
	if err != nil {
		panic(err)
	}
}

func runComposeFixture(dir string, args []string) int {
	switch args[0] {
	case "compose":
		return Execute()
	case "serve":
		if os.Getenv("TEST_COMPOSE_CORE_FIXTURE") != "" {
			path := composeTokenPath(dir, "triggers.token")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				panic(err)
			}
			for _, file := range []string{"triggers.token", "admin.token"} {
				token := composeTriggerToken
				if file == "admin.token" {
					token = strings.Repeat("a", 48)
				}
				if err := os.WriteFile(composeTokenPath(dir, file), []byte(token), 0o600); err != nil {
					panic(err)
				}
			}
			return runComposeHTTPFixture(dir, "core")
		}
		recordComposeEvent(dir, "core:start")
		code := Execute()
		recordComposeEvent(dir, "core:stop")
		return code
	case "triggers":
		role := args[1] + ":" + args[2]
		if role == "github:serve" {
			return runComposeHTTPFixture(dir, "github")
		}
		recordComposeEvent(dir, role)
		if role == "dws:serve" && os.Getenv("TEST_COMPOSE_INVALID_DWS_DATABASE_ON_SERVE") != "" {
			if err := os.WriteFile(filepath.Join(dir, "dws.sqlite"), []byte("invalid SQLite fixture"), 0o600); err != nil {
				panic(err)
			}
		}
		if role == "github:check" {
			// The Trigger process tests exercise the actual HTTP preflight.
			// This fixture verifies Compose's isolated configuration and wiring.
			path, endpoint := slices.Index(args, "--config"), slices.Index(args, "--compose-core-endpoint")
			if path < 0 || endpoint < 0 {
				return 1
			}
			_, err := triggers.Load(triggers.GitHub, args[path+1], os.Stdin, args[endpoint+1])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			recordComposeEvent(dir, role+":stop")
			return 0
		}
		code := Execute()
		recordComposeEvent(dir, role+":stop")
		return code
	case "--version", "event", "chat":
		return runComposeDWSFixture(dir, args)
	default:
		return 1
	}
}

func runComposeDWSFixture(dir string, args []string) int {
	switch {
	case args[0] == "--version":
		recordComposeEvent(dir, "dws-cli:preflight")
		if os.Getenv("TEST_COMPOSE_DWS_BAD_CLI") != "" {
			fmt.Fprintln(os.Stderr, "PRIVATE-FIXTURE")
			return 1
		}
		fmt.Println("dws version v1.0.0-fixture")
		return 0
	case args[len(args)-1] == "--help":
		fmt.Println("--flatten --profile --format --open-dingtalk-id --group --message-id --ref-sender --content --idempotency-key --yes --open-task-id")
		return 0
	case args[0] == "event":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		recordComposeEvent(dir, "consumer:start")
		if awaitComposeFile(ctx, filepath.Join(dir, "consumer-ready")) {
			fmt.Fprintln(os.Stderr, "[event] ready event_key=user_im_message_receive_o2o fixture=true")
			if os.Getenv("TEST_COMPOSE_FEED_DWS") != "" {
				fmt.Println(`{"type":"user_im_message_receive_o2o","message_id":"last-message","conversation_id":"fixture-self-chat","sender_open_dingtalk_id":"self","content":"admit this message","event_time":1794761143211}`)
			}
			<-ctx.Done()
		}
		recordComposeEvent(dir, "consumer:stop")
		if err := os.WriteFile(filepath.Join(dir, "consumer-stopping"), nil, 0o600); err != nil {
			panic(err)
		}
		return 0
	default:
		return 1
	}
}

func awaitComposeFile(ctx context.Context, path string) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func runComposeHTTPFixture(dir, name string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	config, err := io.ReadAll(os.Stdin)
	if err != nil || len(config) == 0 || strings.Contains(string(config), "\ntriggers:") {
		panic("expected one isolated configuration section")
	}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			if name == "core" && os.Getenv("TEST_COMPOSE_CORE_FIXTURE") == "watchdog" {
				recordComposeEvent(dir, "core:readiness-probe")
				if _, err := os.Stat(filepath.Join(dir, "core-ready")); err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				recordComposeEvent(dir, "core:ready")
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/core/reports":
			recordComposeEvent(dir, "core:reports")
			fmt.Fprintln(w, `{"items":[]}`)
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			mode, _ := os.ReadFile(filepath.Join(dir, "github-status-mode"))
			switch string(mode) {
			case "wait":
				<-r.Context().Done()
			case "malformed":
				fmt.Fprintln(w, `{"source":"github","private":"fixture-private-github-status"}`)
			case "ready":
				fmt.Fprintln(w, `{"source":"github","notifications":{"state":"ready"},"repositories":{}}`)
			default:
				fmt.Fprintln(w, `{"source":"github","notifications":{"state":"stale","lastError":"fixture-private-github-status","nextAttemptAt":"2026-11-22T12:00:00Z"},"repositories":{}}`)
			}
		case "/v1/core/trigger-messages":
			composeFixtureAdmission(dir, w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	recordComposeEvent(dir, name+":start")
	origin := "http://" + listener.Addr().String()
	switch name {
	case "github":
		if os.Getenv("TEST_COMPOSE_HOLD_GITHUB_STARTUP") == "" || awaitComposeFile(ctx, filepath.Join(dir, "github-startup-release")) {
			fmt.Printf("{\"status\":\"starting\",\"statusEndpoint\":%q}\n", origin)
		}
	case "core":
		fmt.Printf("{\"status\":\"ready\",\"endpoint\":%q}\n", origin)
	}
	<-ctx.Done()
	recordComposeEvent(dir, name+":stop")
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return 1
	}
	return 0
}

func composeFixtureAdmission(dir string, w http.ResponseWriter, r *http.Request) {
	token, err := os.ReadFile(composeTokenPath(dir, "triggers.token"))
	if err != nil || r.Header.Get("Authorization") != "Bearer "+strings.TrimSpace(string(token)) || r.Method != http.MethodPost ||
		r.Header.Get("X-Superagent-Source") != "dws" || r.Header.Get("X-Superagent-Account-Id") != "fixture-dws" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || !strings.Contains(string(body), "last-message") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	recordComposeEvent(dir, "core:submission")
	switch os.Getenv("TEST_COMPOSE_CORE_FIXTURE") {
	case "wait-for-shutdown":
		if !awaitComposeFile(r.Context(), filepath.Join(dir, "consumer-stopping")) {
			return
		}
	case "unavailable":
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	recordComposeEvent(dir, "core:receipt")
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"accepted","messageId":"fixture-message","threadId":"fixture-thread","acceptedAt":"2026-11-22T10:00:00Z"}`)
}

type composeProcess struct {
	cmd          *exec.Cmd
	stdout, logs string
	done         chan struct{}
	err          error
}

func startComposeFixture(t *testing.T, dir, yaml string, environment ...string) *composeProcess {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return startComposeProcess(t, self, dir, yaml, append([]string{
		composeFixtureDir + "=" + dir,
		composeGitHubCoreEnv + "=ignored-inherited-github-value",
		composeGitHubEnv + "=" + composeGitHubToken,
		composeDWSCoreEnv + "=ignored-inherited-dws-value",
	}, environment...)...)
}

func startComposeProcess(t *testing.T, binary, dir, yaml string, environment ...string) *composeProcess {
	t.Helper()
	stack := filepath.Join(dir, "stack.yaml")
	require.NoError(t, os.WriteFile(stack, []byte(yaml), 0o600))
	stdout, err := os.CreateTemp(dir, "stdout-")
	require.NoError(t, err)
	defer stdout.Close()
	stderr, err := os.CreateTemp(dir, "stderr-")
	require.NoError(t, err)
	defer stderr.Close()
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), binary, "compose", "--config", stack)
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "OTEL_TRACES_EXPORTER=none", "OTEL_METRICS_EXPORTER=none")
	cmd.Env = append(cmd.Env, environment...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	require.NoError(t, cmd.Start())
	p := &composeProcess{cmd: cmd, stdout: stdout.Name(), logs: stderr.Name(), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.err = cmd.Wait()
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				<-p.done
				t.Error("Compose fixture did not stop")
			}
		}
		logs := readComposeFile(t, p.logs)
		for _, value := range []string{composeTriggerToken, composeGitHubToken, "DATA RACE"} {
			require.NotContains(t, logs, value)
			require.NotContains(t, readComposeFile(t, p.stdout), value)
		}
	})
	return p
}

func (p *composeProcess) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(30 * time.Second):
		t.Fatalf("Compose did not exit: %s", readComposeFile(t, p.logs))
		return nil
	}
}

func (p *composeProcess) startup(t *testing.T) composeStartup {
	t.Helper()
	var startup composeStartup
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(p.stdout)
		return json.Unmarshal(data, &startup) == nil && startup.Status == "ready"
	}, 12*time.Second, 20*time.Millisecond, "Compose did not publish its startup record")
	return startup
}

func readComposeFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	return string(data)
}

func composeEvents(dir string) []composeFixtureEvent {
	data, _ := os.ReadFile(filepath.Join(dir, "events.ndjson"))
	var events []composeFixtureEvent
	for line := range strings.SplitSeq(string(data), "\n") {
		var event composeFixtureEvent
		if json.Unmarshal([]byte(line), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

func awaitComposeEvent(t *testing.T, dir, name string) composeFixtureEvent {
	t.Helper()
	var got composeFixtureEvent
	require.Eventually(t, func() bool {
		for _, event := range composeEvents(dir) {
			if event.Name == name {
				got = event
				return true
			}
		}
		return false
	}, 12*time.Second, 20*time.Millisecond, "%s event", name)
	return got
}

func assertComposeOrder(t *testing.T, dir string, names ...string) {
	t.Helper()
	events := composeEvents(dir)
	last := -1
	for _, name := range names {
		index := slices.IndexFunc(events, func(event composeFixtureEvent) bool { return event.Name == name })
		require.Greater(t, index, last, "expected %s in order %v, events %v", name, names, events)
		last = index
	}
}

func composeFixtureConfig(t *testing.T, dir, coreListen string, github, dws bool) string {
	t.Helper()
	config := fmt.Sprintf(`core:
  listen: %q
  dataDir: ./core-state
`, coreListen)
	if !github && !dws {
		return config
	}
	config += "triggers:\n"
	if github {
		config += fmt.Sprintf(`  github:
    accountId: fixture-github
    instanceId: github-adapter
    coreTokenEnv: %s
    githubTokenEnv: %s
    databasePath: ./github.sqlite
    coreEndpoint: invalid-and-overridden
    repositories: [{repository: fixture/example}]
`, composeGitHubCoreEnv, composeGitHubEnv)
	}
	if dws {
		self, err := os.Executable()
		require.NoError(t, err)
		config += fmt.Sprintf(`  dws:
    dwsExecutable: %q
    profile: corp:fixture-dws
    source: dws
    accountId: fixture-dws
    instanceId: dws-adapter
    selfOpenDingtalkId: self
    coreTokenEnv: %s
    databasePath: ./dws.sqlite
    retryMillis: 20
    requestTimeoutMillis: 2000
    selfChat: {enabled: true, conversationId: fixture-self-chat, match: [{prefix: "admit "}]}
`, self, composeDWSCoreEnv)
		if strings.HasPrefix(coreListen, "[") {
			config += "    statusListen: '[::1]:0'\n"
		}
	}
	return config
}

func TestComposeEmbeddedMASSProcess(t *testing.T) {
	binary := os.Getenv("SUPERAGENT_CORE")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "superagent")
		build := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, "./cmd/superagent")
		build.Dir = "../../.."
		build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
		out, err := build.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	t.Run("default without sessions", func(t *testing.T) {
		dir := t.TempDir()
		const traceID = "db64663e60fb444b8a93ea9c06c24413"
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.String() != "/observability/api/fixture_org/_search?type=traces" {
				http.Error(w, "unexpected OpenObserve request", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"hits":[{"trace_id":%q,"span_id":"5b147a1058d5e09f","operation_name":"fixture-span","start_time":1795196579345675000,"duration":1000}]}`, traceID)
		}))
		t.Cleanup(upstream.Close)
		config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + fmt.Sprintf(`  openobserve:
    endpoint: %q
    organization: fixture_org
`, upstream.URL+"/observability/")
		first := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		startup := first.startup(t)
		trace := composeHTTPBody(t, startup.Endpoint+"/v1/telemetry/traces/"+traceID)
		require.Contains(t, trace, "fixture-span")
		require.DirExists(t, filepath.Join(dir, "core-state", "massd"))
		require.Equal(t, "ws"+strings.TrimPrefix(startup.Endpoint, "http")+"/massd/jsonrpc", startup.MassdEndpoint)
		require.NoDirExists(t, filepath.Join(dir, "core-state", "mcp-files"))
		checkStackStatus(t, startup.Endpoint, "ready", "ready", "disabled", "disabled")
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, startup.Endpoint+"/massd/readyz", http.NoBody)
		require.NoError(t, err)
		response, err := (&http.Client{Timeout: time.Second}).Do(req)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)

		admin := readComposeFile(t, composeTokenPath(dir, "admin.token"))
		trigger := readComposeFile(t, composeTokenPath(dir, "triggers.token"))
		require.NotEqual(t, admin, trigger)
		second := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		require.Error(t, second.wait(t))
		require.Contains(t, readComposeFile(t, second.logs), "already owned by another Core")
		require.Equal(t, admin, readComposeFile(t, composeTokenPath(dir, "admin.token")))
		require.Equal(t, trigger, readComposeFile(t, composeTokenPath(dir, "triggers.token")))
		require.Empty(t, readComposeFile(t, second.stdout))
		composeHTTPBody(t, startup.Endpoint+"/status")
		require.NoError(t, first.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, first.wait(t), readComposeFile(t, first.logs))

		// The next startup opens MASS before Core's bind attempt fails.
		contender := strings.Replace(config, "./core-state", "./contender-state", 1)
		occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer occupied.Close()
		failed := startComposeProcess(t, binary, dir, strings.Replace(contender, "127.0.0.1:0", occupied.Addr().String(), 1), "PATH="+dir)
		require.Error(t, failed.wait(t))
		require.Contains(t, readComposeFile(t, failed.logs), "cannot bind listen address")
		require.Empty(t, readComposeFile(t, failed.stdout))
		recovered := startComposeProcess(t, binary, dir, contender, "PATH="+dir)
		recovered.startup(t)
		require.NoError(t, recovered.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, recovered.wait(t), readComposeFile(t, recovered.logs))

		restarted := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		instance := restarted.startup(t)
		require.Equal(t, admin, readComposeFile(t, composeTokenPath(dir, "admin.token")))
		require.Equal(t, trigger, readComposeFile(t, composeTokenPath(dir, "triggers.token")))
		command := exec.CommandContext(t.Context(), binary, "scheduled-tasks", "--config", filepath.Join(dir, "stack.yaml"), "--endpoint", instance.Endpoint, "list")
		out, err := command.CombinedOutput()
		require.NoError(t, err, "%s", out)
		require.JSONEq(t, `{"items":[]}`, string(out))
		require.NoError(t, restarted.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, restarted.wait(t), readComposeFile(t, restarted.logs))
		for _, path := range []string{restarted.stdout, restarted.logs} {
			for _, secret := range []string{admin, trigger} {
				require.NotContains(t, readComposeFile(t, path), strings.TrimSpace(secret))
			}
		}
	})
	t.Run("sessions and authenticated RPC", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cua-mcp"), []byte("#!/bin/sh\nexit 1\n"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cua.yaml"), []byte("{}\n"), 0o600))
		config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + `  externalURL: http://127.0.0.1:24370
  remoteComputer:
    cua: {command: ./cua-mcp, configFile: ./cua.yaml}
  sessions:
    router: {workspace: ., runtimeRef: codex, model: fixture-router, effort: low}
    topic: {runtimeRef: codex, model: fixture-topic}
    issue:
      templates:
        - {runtimeRef: codex, model: fixture-model, description: Fixture Issue}
`
		p := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		startup := p.startup(t)
		token := strings.TrimSpace(readComposeFile(t, composeTokenPath(dir, "admin.token")))
		trigger := strings.TrimSpace(readComposeFile(t, composeTokenPath(dir, "triggers.token")))
		require.DirExists(t, filepath.Join(dir, "core-state", "massd"))
		require.DirExists(t, filepath.Join(dir, "core-state", "mcp-files"))
		require.Equal(t, "ws"+strings.TrimPrefix(startup.Endpoint, "http")+"/massd/jsonrpc", startup.MassdEndpoint)
		require.JSONEq(t, `{"status":"ready"}`, composeHTTPBody(t, startup.Endpoint+"/massd/readyz"))
		dial := &websocket.Dialer{HandshakeTimeout: time.Second}
		for _, credential := range []string{"", trigger} {
			header := make(http.Header)
			if credential != "" {
				header.Set("Authorization", "Bearer "+credential)
			}
			conn, response, err := dial.DialContext(t.Context(), startup.MassdEndpoint, header)
			require.Error(t, err)
			require.Nil(t, conn)
			require.NotNil(t, response)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
		}
		c, err := client.New(startup.MassdEndpoint, token, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, c.Close()) })
		var codex resource.Runtime
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		require.NoError(t, c.GetRuntime(ctx, "codex", &codex))
		require.Equal(t, "codex", codex.Metadata.Name)
		require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
	})
	t.Run("external MASS", func(t *testing.T) {
		dir := t.TempDir()
		const token = "fixture-standalone-massd-credential"
		instance, err := massd.Start(t.Context(), massd.Options{
			DataDir: t.TempDir(), Transport: transport.Options{Token: token, WriteTimeout: time.Second},
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })
		external := httptest.NewServer(instance.Handler())
		t.Cleanup(external.Close)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "external.token"), []byte(token), 0o600))
		config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + fmt.Sprintf(`  massd:
    endpoint: %q
    tokenFile: ./external.token
`, "ws"+strings.TrimPrefix(external.URL, "http")+"/jsonrpc")
		p := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		startup := p.startup(t)
		require.Empty(t, startup.MassdEndpoint)
		require.NoDirExists(t, filepath.Join(dir, "core-state", "massd"))
		checkStackStatus(t, startup.Endpoint, "ready", "ready", "disabled", "disabled")
		require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
	})
}

func TestComposeProcessCombinations(t *testing.T) {
	for _, tc := range []struct {
		name, listen, origin string
		github, dws          bool
		signal               syscall.Signal
	}{
		{"without triggers", "127.0.0.1:0", "http://127.0.0.1:", false, false, syscall.SIGTERM},
		{"github only", "0.0.0.0:0", "http://127.0.0.1:", true, false, syscall.SIGINT},
		{"dws only", "127.0.0.1:0", "http://127.0.0.1:", false, true, syscall.SIGTERM},
		{"hostname and wildcard Trigger", "localhost:0", "http://127.0.0.1:", false, true, syscall.SIGTERM},
		{"both on IPv6 wildcard", "[::]:0", "http://[::1]:", true, true, syscall.SIGTERM},
		{"both on IPv6 loopback", "[::1]:0", "http://[::1]:", true, true, syscall.SIGINT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			unrelated := exec.CommandContext(t.Context(), "/bin/sleep", "60")
			require.NoError(t, unrelated.Start())
			t.Cleanup(func() {
				_ = unrelated.Process.Kill()
				_ = unrelated.Wait()
			})
			config := composeFixtureConfig(t, dir, tc.listen, tc.github, tc.dws)
			if tc.listen == "localhost:0" {
				config += "    statusListen: '0.0.0.0:0'\n"
			}
			p := startComposeFixture(t, dir, config, "TEST_COMPOSE_FEED_DWS=1")
			startup := p.startup(t)
			if tc.listen == "localhost:0" {
				parsed, err := url.Parse(startup.Endpoint)
				require.NoError(t, err)
				parsed.Host = net.JoinHostPort("localhost", parsed.Port())
				composeHTTPBody(t, parsed.String()+"/status")
			} else {
				require.True(t, strings.HasPrefix(startup.Endpoint, tc.origin), startup.Endpoint)
			}
			require.False(t, strings.HasSuffix(startup.Endpoint, ":0"), startup.Endpoint)
			require.NotEmpty(t, startup.MassdEndpoint)
			require.DirExists(t, filepath.Join(dir, "core-state", "massd"))
			server := awaitComposeEvent(t, dir, "core:start")
			require.Equal(t, p.cmd.Process.Pid, server.Parent)
			require.Equal(t, [3]bool{!tc.github, !tc.github, !tc.dws}, server.Env)
			require.False(t, server.TriggerToken)
			if tc.github {
				check := awaitComposeEvent(t, dir, "github:check")
				collector := awaitComposeEvent(t, dir, "github:start")
				for _, child := range []composeFixtureEvent{check, collector} {
					require.Equal(t, p.cmd.Process.Pid, child.Parent)
					require.True(t, child.TriggerToken)
					require.Equal(t, [3]bool{true, true, !tc.dws}, child.Env)
				}
				require.Contains(t, collector.Args, startup.Endpoint)
				require.Contains(t, composeHTTPBody(t, startup.TriggerEndpoints["github"]+"/status"), `"state":"stale"`)
				assertComposeOrder(t, dir, "core:start", "github:check", "github:start")
			} else {
				require.NotContains(t, startup.TriggerEndpoints, "github")
			}
			if tc.dws {
				check := awaitComposeEvent(t, dir, "dws:check")
				collector := awaitComposeEvent(t, dir, "dws:serve")
				for _, child := range []composeFixtureEvent{check, collector} {
					require.Equal(t, p.cmd.Process.Pid, child.Parent)
					require.True(t, child.TriggerToken)
					require.Equal(t, [3]bool{!tc.github, !tc.github, true}, child.Env)
				}
				require.Contains(t, collector.Args, startup.Endpoint)
				preflight := awaitComposeEvent(t, dir, "dws-cli:preflight")
				require.Equal(t, check.PID, preflight.Parent)
				require.False(t, preflight.TriggerToken)
				require.Equal(t, [3]bool{!tc.github, !tc.github, false}, preflight.Env)
				consumer := awaitComposeEvent(t, dir, "consumer:start")
				require.Equal(t, collector.PID, consumer.Parent)
				require.False(t, consumer.TriggerToken)
				require.Equal(t, [3]bool{!tc.github, !tc.github, false}, consumer.Env)
				endpoint := startup.TriggerEndpoints["dws"]
				require.True(t, strings.HasPrefix(endpoint, tc.origin), endpoint)
				require.Contains(t, composeHTTPBody(t, endpoint+"/status"), `"connection":"connecting"`)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer-ready"), nil, 0o600))
				require.Eventually(t, func() bool {
					var status struct {
						Connection    string `json:"connection"`
						ConsumerReady bool   `json:"consumerReady"`
						Accepted      int    `json:"accepted"`
					}
					return json.Unmarshal([]byte(composeHTTPBody(t, endpoint+"/status")), &status) == nil &&
						status.Connection == "ready" && status.ConsumerReady && status.Accepted == 1
				}, 5*time.Second, 20*time.Millisecond)
				require.Contains(t, composeHTTPBody(t, startup.Endpoint+"/v1/core/trigger-messages"), "last-message")
				assertComposeOrder(t, dir, "core:start", "dws:check", "dws:serve", "consumer:start")
			} else {
				require.NotContains(t, startup.TriggerEndpoints, "dws")
			}
			require.NoError(t, p.cmd.Process.Signal(tc.signal))
			require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
			require.NotContains(t, readComposeFile(t, p.logs), "Watchdog check")
			require.NoError(t, unrelated.Process.Signal(syscall.Signal(0)))
			assertComposeOrder(t, dir, "core:start", "core:stop")
			if tc.github {
				assertComposeOrder(t, dir, "github:stop", "core:stop")
			}
			if tc.dws {
				assertComposeOrder(t, dir, "consumer:stop", "dws:serve:stop", "core:stop")
			}
		})
	}
}

func TestComposeWatchdogWaitsForCore(t *testing.T) {
	dir := t.TempDir()
	config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + "watchdog: {interval: 1h}\n"
	p := startComposeFixture(t, dir, config, "TEST_COMPOSE_CORE_FIXTURE=watchdog")
	awaitComposeEvent(t, dir, "core:readiness-probe")
	require.Never(t, func() bool {
		return slices.ContainsFunc(composeEvents(dir), func(event composeFixtureEvent) bool { return event.Name == "core:reports" })
	}, 250*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "core-ready"), nil, 0o600))
	startup := p.startup(t)
	require.NotEmpty(t, startup.Endpoint)
	awaitComposeEvent(t, dir, "core:reports")
	assertComposeOrder(t, dir, "core:start", "core:ready", "core:reports")
	require.Eventually(t, func() bool {
		logs, _ := os.ReadFile(p.logs)
		return strings.Contains(string(logs), `"reason":"no_candidate"`)
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
}

func TestComposeProcessPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, old, new, errorText string
		environment               []string
	}{
		{"unknown Trigger field", "    repositories:", "    unused: PRIVATE-FIXTURE\n    repositories:", "triggers.github configuration", nil},
		{"credential shared across scopes", "", "", "triggers.github preflight: native service credentials must be distinct", []string{composeGitHubEnv + "=" + composeTriggerToken}},
		{"credential environment shared across scopes", "coreTokenEnv: " + composeDWSCoreEnv, "coreTokenEnv: " + composeGitHubEnv, "triggers.github githubTokenEnv conflicts with triggers.dws coreTokenEnv", nil},
		{"state under MASS", "./dws.sqlite", "./core-state/massd/dws.sqlite", "triggers.dws databasePath conflicts with core dataDir", nil},
		{"state under Core", "./dws.sqlite", "./core-state/dws.sqlite", "triggers.dws databasePath conflicts with core dataDir", nil},
		{"shared state", "./dws.sqlite", "./github.sqlite-wal", "triggers.dws databasePath conflicts with triggers.github databasePath", nil},
		{"invalid status port", "    databasePath: ./dws.sqlite", "    statusListen: 127.0.0.1:invalid\n    databasePath: ./dws.sqlite", "statusListen must use host:port with a numeric port", nil},
		{"invalid Core address", "127.0.0.1:0", "'bad/name:0'", "listen must use host:port with a numeric port", nil},
		{"shared status port", "    databasePath:", "    statusListen: 127.0.0.1:15443\n    databasePath:", "triggers.dws statusListen conflicts with triggers.github", nil},
		{"wildcard status port", "    databasePath:", "    statusListen: 0.0.0.0:15443\n    databasePath:", "triggers.dws statusListen conflicts with triggers.github", nil},
		{"DWS CLI local contract", "", "", "triggers.dws preflight: DWS executable/version check failed", []string{"TEST_COMPOSE_DWS_BAD_CLI=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := composeFixtureConfig(t, dir, "127.0.0.1:0", true, true)
			if tc.old != "" {
				require.Contains(t, config, tc.old)
				config = strings.ReplaceAll(config, tc.old, tc.new)
			}
			if tc.name == "credential shared across scopes" {
				path := composeTokenPath(dir, "triggers.token")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(composeTriggerToken), 0o600))
			}
			p := startComposeFixture(t, dir, config, tc.environment...)
			require.Error(t, p.wait(t))
			require.Empty(t, readComposeFile(t, p.stdout))
			logs := readComposeFile(t, p.logs)
			require.Contains(t, logs, tc.errorText)
			require.NotContains(t, logs, "PRIVATE-FIXTURE")
			for _, event := range composeEvents(dir) {
				require.False(t, strings.HasSuffix(event.Name, ":start") && event.Name != "core:start", event.Name)
			}
			if tc.name == "credential shared across scopes" || tc.name == "DWS CLI local contract" {
				assertComposeOrder(t, dir, "core:start", "core:stop")
				started := awaitComposeEvent(t, dir, "core:start")
				require.ErrorIs(t, syscall.Kill(started.PID, 0), syscall.ESRCH)
			} else {
				require.Empty(t, composeEvents(dir))
			}
		})
	}
}

func TestComposeProcessSecondTriggerStartupFailure(t *testing.T) {
	dir := t.TempDir()
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true),
		"TEST_COMPOSE_INVALID_DWS_DATABASE_ON_SERVE=1")
	require.Error(t, p.wait(t))
	require.Empty(t, readComposeFile(t, p.stdout))
	require.Contains(t, readComposeFile(t, p.logs), "triggers.dws")
	assertComposeOrder(t, dir, "core:start", "github:start", "dws:serve", "dws:serve:stop", "github:stop", "core:stop")
}

func TestComposeProcessTriggerFailureIsolated(t *testing.T) {
	dir := t.TempDir()
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true))
	startup := p.startup(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer-ready"), nil, 0o600))
	require.Eventually(t, func() bool {
		return strings.Contains(composeHTTPBody(t, startup.TriggerEndpoints["dws"]+"/status"), `"connection":"ready"`)
	}, 5*time.Second, 20*time.Millisecond)
	github := awaitComposeEvent(t, dir, "github:start")
	require.NoError(t, syscall.Kill(github.PID, syscall.SIGTERM))
	require.Eventually(t, func() bool {
		logs, _ := os.ReadFile(p.logs)
		return strings.Contains(string(logs), `"component":"triggers.github"`)
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, p.cmd.Process.Signal(syscall.Signal(0)))
	checkStackStatus(t, startup.Endpoint, "degraded", "ready", "unavailable", "ready")
	require.JSONEq(t, `{"status":"ready"}`, composeHTTPBody(t, startup.Endpoint+"/readyz"))
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
}

func TestComposeProcessCoreFailure(t *testing.T) {
	dir := t.TempDir()
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true))
	p.startup(t)
	target := awaitComposeEvent(t, dir, "core:start")
	require.NoError(t, syscall.Kill(target.PID, syscall.SIGTERM))
	require.Error(t, p.wait(t))
	require.Contains(t, readComposeFile(t, p.logs), "core exited unexpectedly")
	assertComposeOrder(t, dir, "github:start", "github:stop")
	assertComposeOrder(t, dir, "dws:serve", "dws:serve:stop")
}

func TestComposeProcessCoreFailureDuringStartup(t *testing.T) {
	dir := t.TempDir()
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true), "TEST_COMPOSE_HOLD_GITHUB_STARTUP=1")
	awaitComposeEvent(t, dir, "github:start")
	target := awaitComposeEvent(t, dir, "core:start")
	require.NoError(t, syscall.Kill(target.PID, syscall.SIGTERM))
	require.Error(t, p.wait(t))
	require.Empty(t, readComposeFile(t, p.stdout))
	require.Contains(t, readComposeFile(t, p.logs), "waiting for triggers.github startup: core exited unexpectedly")
	assertComposeOrder(t, dir, "github:start", "github:stop")
	assertComposeOrder(t, dir, "core:start", "core:stop")
	for _, event := range composeEvents(dir) {
		require.NotEqual(t, "dws:serve", event.Name)
	}
}

func TestComposeProcessDWSDuringShutdown(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer-ready"), nil, 0o600))
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true),
		"TEST_COMPOSE_CORE_FIXTURE=wait-for-shutdown", "TEST_COMPOSE_FEED_DWS=1")
	p.startup(t)
	awaitComposeEvent(t, dir, "core:submission")
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
	assertComposeOrder(t, dir, "core:submission", "consumer:stop", "core:receipt", "dws:serve:stop", "core:stop")
	require.Contains(t, readComposeFile(t, p.logs), `"event":"core_accepted"`)
}

func TestComposeProcessDWSRetainsPendingOnShutdown(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer-ready"), nil, 0o600))
	config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, true)
	first := startComposeFixture(t, dir, config, "TEST_COMPOSE_CORE_FIXTURE=unavailable", "TEST_COMPOSE_FEED_DWS=1")
	first.startup(t)
	awaitComposeEvent(t, dir, "core:submission")
	require.NoError(t, first.cmd.Process.Signal(syscall.SIGTERM))
	require.Error(t, first.wait(t))
	require.Contains(t, readComposeFile(t, first.logs), "pending SQLite submissions retained")
	require.NoError(t, os.Remove(filepath.Join(dir, "events.ndjson")))
	require.NoError(t, os.Remove(filepath.Join(dir, "consumer-ready")))
	restarted := startComposeFixture(t, dir, config, "TEST_COMPOSE_CORE_FIXTURE=healthy")
	restarted.startup(t)
	awaitComposeEvent(t, dir, "consumer:start")
	assertComposeOrder(t, dir, "dws:serve", "core:submission", "core:receipt", "consumer:start")
	require.NoError(t, restarted.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, restarted.wait(t), readComposeFile(t, restarted.logs))
}

func composeHTTPBody(t *testing.T, endpoint string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, http.NoBody)
	require.NoError(t, err)
	httpClient := &http.Client{Timeout: time.Second}
	response, err := httpClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(data)
}
