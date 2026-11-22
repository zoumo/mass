package subcommand

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/superagent/triggers"
)

const (
	composeFixtureDir    = "TEST_COMPOSE_FIXTURE_DIR"
	composeGitHubCoreEnv = "TEST_COMPOSE_GITHUB_CORE"
	composeGitHubEnv     = "TEST_COMPOSE_GITHUB_UPSTREAM"
	composeDWSCoreEnv    = "TEST_COMPOSE_DWS_CORE"
	composeGitHubCore    = "fixture-github-core-credential-unused-by-anyone-else"
	composeGitHubToken   = "fixture-github-graphql-credential"
	composeDWSToken      = "fixture-dws-core-credential-unused-by-anyone-else"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(composeFixtureDir); dir != "" && len(os.Args) > 1 {
		os.Exit(runComposeFixture(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type composeFixtureEvent struct {
	Name   string
	PID    int
	Parent int
	Args   []string
	Env    [3]bool
}

func recordComposeEvent(dir, name string) {
	f, err := os.OpenFile(filepath.Join(dir, "events.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(composeFixtureEvent{
		Name: name, PID: os.Getpid(), Parent: os.Getppid(), Args: os.Args[1:],
		Env: [3]bool{os.Getenv(composeGitHubCoreEnv) != "", os.Getenv(composeGitHubEnv) != "", os.Getenv(composeDWSCoreEnv) != ""},
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
		case "/v1/observer/health":
			w.WriteHeader(http.StatusOK)
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"source":"github","notifications":{"state":"stale","lastError":"github_notifications_request_failed","nextAttemptAt":"2026-11-22T12:00:00Z"},"repositories":{}}`)
		case "/v1/channel/messages":
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
		if os.Getenv("TEST_COMPOSE_HOLD_GITHUB_STARTUP") == "" {
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
	if r.Header.Get("Authorization") != "Bearer "+composeDWSToken || r.Method != http.MethodPost {
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
	fmt.Fprintln(w, `{"status":"accepted","messageId":"fixture-message","threadKey":"fixture-thread","acceptedAt":"2026-11-22T10:00:00Z"}`)
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
		composeGitHubCoreEnv + "=" + composeGitHubCore,
		composeGitHubEnv + "=" + composeGitHubToken,
		composeDWSCoreEnv + "=" + composeDWSToken,
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
		for _, value := range []string{composeGitHubCore, composeGitHubToken, composeDWSToken, "DATA RACE"} {
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "github-token"), []byte(composeGitHubCore), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dws-token"), []byte(composeDWSToken), 0o600))
	config := fmt.Sprintf(`core:
  listen: %q
  dataDir: ./core-state
  sources:
    - {source: github, accountId: fixture-github, instanceId: github-adapter, tokenFile: github-token}
    - {source: dws, accountId: fixture-dws, instanceId: dws-adapter, tokenFile: dws-token, outboxConsumer: true}
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
		config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false)
		first := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		startup := first.startup(t)
		require.DirExists(t, filepath.Join(dir, "massd-data"))
		require.NotContains(t, readComposeFile(t, first.stdout), "massdEndpoint")
		require.NoDirExists(t, filepath.Join(dir, "core-state", "mcp-files"))
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, startup.Endpoint+"/massd/readyz", http.NoBody)
		require.NoError(t, err)
		response, err := (&http.Client{Timeout: time.Second}).Do(req)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusNotFound, response.StatusCode)

		contender := strings.Replace(config, "./core-state", "./contender-state", 1)
		second := startComposeProcess(t, binary, dir, contender, "PATH="+dir)
		require.Error(t, second.wait(t))
		require.Contains(t, readComposeFile(t, second.logs), "data_directory_in_use")
		require.Empty(t, readComposeFile(t, second.stdout))
		composeHTTPBody(t, startup.Endpoint+"/v1/observer/health")
		require.NoError(t, first.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, first.wait(t), readComposeFile(t, first.logs))

		// The next startup opens MASS before Core's bind attempt fails.
		occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer occupied.Close()
		failed := startComposeProcess(t, binary, dir, strings.Replace(contender, "127.0.0.1:0", occupied.Addr().String(), 1), "PATH="+dir)
		require.Error(t, failed.wait(t))
		require.Contains(t, readComposeFile(t, failed.logs), "cannot bind listen address")
		require.Empty(t, readComposeFile(t, failed.stdout))
		restarted := startComposeProcess(t, binary, dir, contender, "PATH="+dir)
		restarted.startup(t)
		require.NoError(t, restarted.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, restarted.wait(t), readComposeFile(t, restarted.logs))
	})
	t.Run("sessions and authenticated RPC", func(t *testing.T) {
		dir := t.TempDir()
		token := strings.Repeat("m", 48)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "massd.token"), []byte(token), 0o600))
		config := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + `  externalURL: http://127.0.0.1:24370
  massd:
    embedded:
      dataDir: ./custom-massd
      sessionIdleTimeout: 0s
      writeTimeout: 2s
      expose: {tokenFile: ./massd.token}
  sessions:
    routerWorkspace: .
    roles:
      router: {runtimeRef: codex, model: fixture-model}
      topic: {runtimeRef: codex, model: fixture-model}
    issueTemplates:
      - {runtimeRef: codex, model: fixture-model, description: Fixture Issue}
`
		p := startComposeProcess(t, binary, dir, config, "PATH="+dir)
		startup := p.startup(t)
		require.DirExists(t, filepath.Join(dir, "custom-massd"))
		require.NoDirExists(t, filepath.Join(dir, "massd-data"))
		require.DirExists(t, filepath.Join(dir, "core-state", "mcp-files"))
		require.Equal(t, "ws"+strings.TrimPrefix(startup.Endpoint, "http")+"/massd/jsonrpc", startup.MassdEndpoint)
		require.JSONEq(t, `{"status":"ready"}`, composeHTTPBody(t, startup.Endpoint+"/massd/readyz"))
		dial := &websocket.Dialer{HandshakeTimeout: time.Second}
		for _, credential := range []string{"", composeGitHubCore} {
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
			p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, tc.listen, tc.github, tc.dws), "TEST_COMPOSE_FEED_DWS=1")
			startup := p.startup(t)
			require.True(t, strings.HasPrefix(startup.Endpoint, tc.origin), startup.Endpoint)
			require.False(t, strings.HasSuffix(startup.Endpoint, ":0"), startup.Endpoint)
			require.Equal(t, startup.Endpoint+"/observer/", startup.ObserverEndpoint)
			require.Empty(t, startup.MassdEndpoint)
			require.DirExists(t, filepath.Join(dir, "massd-data"))
			server := awaitComposeEvent(t, dir, "core:start")
			require.Equal(t, p.cmd.Process.Pid, server.Parent)
			require.Equal(t, [3]bool{!tc.github, !tc.github, !tc.dws}, server.Env)
			if tc.github {
				check := awaitComposeEvent(t, dir, "github:check")
				collector := awaitComposeEvent(t, dir, "github:start")
				for _, child := range []composeFixtureEvent{check, collector} {
					require.Equal(t, p.cmd.Process.Pid, child.Parent)
					require.Equal(t, [3]bool{true, true, !tc.dws}, child.Env)
				}
				require.Contains(t, collector.Args, startup.Endpoint)
				require.Contains(t, composeHTTPBody(t, startup.TriggerEndpoints["github"]+"/status"), `"state":"stale"`)
				assertComposeOrder(t, dir, "github:check", "core:start", "github:start")
			} else {
				require.NotContains(t, startup.TriggerEndpoints, "github")
			}
			if tc.dws {
				check := awaitComposeEvent(t, dir, "dws:check")
				collector := awaitComposeEvent(t, dir, "dws:serve")
				for _, child := range []composeFixtureEvent{check, collector} {
					require.Equal(t, p.cmd.Process.Pid, child.Parent)
					require.Equal(t, [3]bool{!tc.github, !tc.github, true}, child.Env)
				}
				require.Contains(t, collector.Args, startup.Endpoint)
				preflight := awaitComposeEvent(t, dir, "dws-cli:preflight")
				require.Equal(t, check.PID, preflight.Parent)
				require.Equal(t, [3]bool{!tc.github, !tc.github, false}, preflight.Env)
				consumer := awaitComposeEvent(t, dir, "consumer:start")
				require.Equal(t, collector.PID, consumer.Parent)
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
				require.Contains(t, composeHTTPBody(t, startup.Endpoint+"/v1/observer/messages"), "last-message")
				assertComposeOrder(t, dir, "dws:check", "core:start", "dws:serve", "consumer:start")
			} else {
				require.NotContains(t, startup.TriggerEndpoints, "dws")
			}
			require.NoError(t, p.cmd.Process.Signal(tc.signal))
			require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
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

func TestComposeProcessPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, old, new, errorText string
		environment               []string
	}{
		{"unknown Trigger field", "    repositories:", "    unused: PRIVATE-FIXTURE\n    repositories:", "triggers.github configuration", nil},
		{"missing Core credential", "", "", "triggers.github preflight: coreTokenEnv", []string{composeGitHubCoreEnv + "="}},
		{"wrong Core credential", "", "", "triggers.dws preflight: configured Core token does not match", []string{composeDWSCoreEnv + "=" + composeGitHubCore}},
		{"unbound source credential", "    instanceId: dws-adapter", "    instanceId: unregistered", "triggers.dws preflight: source, accountId and instanceId", nil},
		{"DWS Outbox role", "outboxConsumer: true}", "outboxConsumer: false}\n    - {source: dws, accountId: fixture-dws, instanceId: another-adapter, tokenFile: another-token, outboxConsumer: true}", "triggers.dws preflight: the reply-enabled Trigger", nil},
		{"credential shared across scopes", "", "", "triggers.dws preflight: credential references must hold distinct tokens", []string{composeGitHubEnv + "=" + composeDWSToken}},
		{"remote plaintext", "127.0.0.1:0", "192.0.2.1:0", "remote coreEndpoint requires HTTPS", nil},
		{"state under MASS", "./dws.sqlite", "./massd-data/dws.sqlite", "triggers.dws databasePath conflicts with core.massd.embedded dataDir", nil},
		{"state under Core", "./dws.sqlite", "./core-state/dws.sqlite", "triggers.dws databasePath conflicts with core dataDir", nil},
		{"shared state", "./dws.sqlite", "./github.sqlite-wal", "triggers.dws databasePath conflicts with triggers.github databasePath", nil},
		{"invalid status port", "    databasePath: ./dws.sqlite", "    statusListen: 127.0.0.1:invalid\n    databasePath: ./dws.sqlite", "triggers.dws statusListen requires a numeric port", nil},
		{"shared status port", "    databasePath:", "    statusListen: 127.0.0.1:15443\n    databasePath:", "triggers.dws statusListen conflicts with triggers.github", nil},
		{"DWS CLI local contract", "", "", "triggers.dws preflight: DWS executable/version check failed", []string{"TEST_COMPOSE_DWS_BAD_CLI=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := composeFixtureConfig(t, dir, "127.0.0.1:0", true, true)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "another-token"), []byte(strings.Repeat("x", 40)), 0o600))
			if tc.old != "" {
				require.Contains(t, config, tc.old)
				config = strings.ReplaceAll(config, tc.old, tc.new)
			}
			p := startComposeFixture(t, dir, config, tc.environment...)
			require.Error(t, p.wait(t))
			require.Empty(t, readComposeFile(t, p.stdout))
			logs := readComposeFile(t, p.logs)
			require.Contains(t, logs, tc.errorText)
			require.NotContains(t, logs, "PRIVATE-FIXTURE")
			for _, event := range composeEvents(dir) {
				require.False(t, strings.HasSuffix(event.Name, ":start"), event.Name)
			}
		})
	}
}

func TestComposeProcessSecondTriggerStartupFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dws.sqlite"), []byte("invalid SQLite fixture"), 0o600))
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true))
	require.Error(t, p.wait(t))
	require.Empty(t, readComposeFile(t, p.stdout))
	require.Contains(t, readComposeFile(t, p.logs), "triggers.dws")
	assertComposeOrder(t, dir, "core:start", "github:start", "dws:serve", "dws:serve:stop", "github:stop", "core:stop")
}

func TestComposeProcessTriggerFailureIsolated(t *testing.T) {
	dir := t.TempDir()
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, "127.0.0.1:0", true, true))
	startup := p.startup(t)
	github := awaitComposeEvent(t, dir, "github:start")
	require.NoError(t, syscall.Kill(github.PID, syscall.SIGTERM))
	require.Eventually(t, func() bool {
		logs, _ := os.ReadFile(p.logs)
		return strings.Contains(string(logs), `"component":"triggers.github"`)
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, p.cmd.Process.Signal(syscall.Signal(0)))
	require.Contains(t, composeHTTPBody(t, startup.TriggerEndpoints["dws"]+"/status"), `"connection":"connecting"`)
	composeHTTPBody(t, startup.Endpoint+"/v1/observer/health")
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
