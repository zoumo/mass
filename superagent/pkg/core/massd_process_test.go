package core_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/internal/testpeer"
	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/transport"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestEmbeddedMASSProcessAndStandaloneHandoff(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	_ = build(t)
	massdBin, relay := dispatchBinaries(t)
	f, _ := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	const external = "massd:\n  endpoint: ws://127.0.0.1:1\n"
	data := filepath.Join(f.data, "massd")
	write(t, f.config, strings.Replace(string(saved), external, "", 1))
	seedEmbeddedRuntime(t, data, relay, peer.listener.Addr().String())

	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=internal_event_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	router := waitBinding(t, p, "router", "", "ready")
	liveCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	preview := "/apis/core/v1/session-bindings/" + router.ID
	req, err := http.NewRequestWithContext(liveCtx, http.MethodGet, p.endpoint+preview+"/live", http.NoBody)
	require.NoError(t, err)
	httpClient := &http.Client{Timeout: 8 * time.Second}
	response, err := httpClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	frames := bufio.NewScanner(response.Body)
	require.True(t, frames.Scan(), "%v", frames.Err())
	require.JSONEq(t, `{"kind":"connected"}`, frames.Text())
	for _, view := range []string{"history", "live"} {
		status, head := request(t, http.MethodHead, p.endpoint, preview+"/"+view, "", nil)
		require.Equal(t, http.StatusOK, status)
		require.Empty(t, head)
	}

	receipt := submit(t, p, f, input("embedded-mass-router"))
	first := peer.next(t, "turn/start")
	require.Equal(t, router.SessionID, first.session)
	decodeInternalBatch(t, first, receipt.EventID)
	first.answer <- nativeSubmitAnswer{workID: "embedded-mass-first", complete: true, final: "embedded persisted message"}
	waitWorkGate(t, gate, "internal_event_before_settlement")
	require.Empty(t, waitInternalEvent(t, f, receipt.EventID, "dispatching").SentAt)
	require.True(t, frames.Scan(), "%v", frames.Err())
	var frame struct{ Kind string }
	require.NoError(t, json.Unmarshal(frames.Bytes(), &frame))
	require.Contains(t, []string{"preview", "history_changed"}, frame.Kind)
	cancel()
	require.False(t, frames.Scan())
	require.ErrorIs(t, frames.Err(), context.Canceled)
	require.Eventually(t, func() bool {
		status, history := request(t, http.MethodGet, p.endpoint, preview+"/history", "", nil)
		return status == http.StatusOK && bytes.Contains(history, []byte("embedded persisted message"))
	}, 5*time.Second, 25*time.Millisecond)

	_, err = massd.Start(t.Context(), massd.Options{DataDir: data, Transport: transport.Options{WriteTimeout: time.Second}})
	var conflict *runtime.OperationError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "data_directory_in_use", conflict.Reason)

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	waitCoreListenerClosed(t, p)
	write(t, filepath.Join(gate, "release"), "released")
	p.waitStopped(t, false)
	waitInternalEvent(t, f, receipt.EventID, "sent")
	endpoint, c, externalToken := startHandoffMassd(t, massdBin, data)
	ref := resource.SessionRef{SessionID: router.SessionID}
	var found resource.Session
	require.NoError(t, c.GetSession(t.Context(), ref, &found))
	require.Equal(t, router.SessionID, found.Status.SessionID)
	require.Equal(t, router.SessionUID, found.Metadata.UID)
	write(t, filepath.Join(f.dir, "external.token"), externalToken)
	write(t, f.config, strings.Replace(string(saved), external, "massd:\n  endpoint: "+endpoint+"\n  tokenFile: ./external.token\n", 1))
	p = f.start()
	rebound := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, router.ID, rebound.ID)
	require.Equal(t, router.SessionID, rebound.SessionID)
	require.Equal(t, router.SessionUID, rebound.SessionUID)
	require.Equal(t, router.ExecutionProfile, rebound.ExecutionProfile)
	peer.quiet(t, 300*time.Millisecond)
	status, history := request(t, http.MethodGet, p.endpoint, preview+"/history", "", nil)
	require.Equal(t, http.StatusOK, status, string(history))
	require.Contains(t, string(history), "embedded persisted message")

	receipt = submit(t, p, f, input("standalone-mass-router"))
	second := peer.next(t, "turn/start")
	require.Equal(t, router.SessionID, second.session)
	decodeInternalBatch(t, second, receipt.EventID)
	second.answer <- nativeSubmitAnswer{workID: "embedded-mass-second", complete: true}
	waitRouteStatus(t, p, receipt.EventID, "sent")

	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(1000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `INSERT INTO session_bindings(id,role,owner_id,generation,created_at,token,spec,state,body)
 SELECT 'stale-binding',role,owner_id,generation+10,created_at,'fixture-stale-binding-token',spec,'revoked',
 json_set(body,'$.id','stale-binding','$.state','revoked','$.sessionUid','different-session-uid')
 FROM session_bindings WHERE id=?`, router.ID)
	require.NoError(t, err)
	for _, view := range []string{"history", "live"} {
		path := "/apis/core/v1/session-bindings/stale-binding/" + view
		status, body := request(t, http.MethodGet, p.endpoint, path, "", nil)
		errorCode(t, status, body, http.StatusServiceUnavailable, "unavailable")
		status, body = request(t, http.MethodHead, p.endpoint, path, "", nil)
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.Empty(t, body)
	}
}

func TestEmbeddedMASSDispatchDuringShutdown(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	f, config, adminFile := embeddedProcessFixture(t, peer)
	token, err := core.ReadOperatorToken(adminFile)
	require.NoError(t, err)
	write(t, f.config, config)
	gate := t.TempDir()
	const stage = "after_session_input_admission"
	p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "released") })
	router := waitBinding(t, p, "router", "", "ready")
	mount := "ws" + strings.TrimPrefix(p.endpoint, "http") + "/apis/massd"
	external, err := client.New(mount, token, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, external.Close()) })
	require.NoError(t, external.Initialize(t.Context(), nil, nil))

	receipt := submit(t, p, f, input("embedded-mass-shutdown"))
	waitWorkGate(t, gate, stage)
	require.Empty(t, waitInternalEvent(t, f, receipt.EventID, "dispatching").SentAt)
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	waitCoreListenerClosed(t, p)
	var runtimes []resource.Runtime
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var admission *runtime.OperationError
	require.ErrorAs(t, external.ListRuntimes(ctx, resource.RuntimeQuery{}, &runtimes), &admission)
	require.Equal(t, "not_ready", admission.Reason)
	write(t, filepath.Join(gate, "release"), "released")
	const final = "native output from the dispatch admitted before shutdown"
	select {
	case call := <-peer.calls:
		require.Equal(t, "turn/start", call.method)
		require.Equal(t, router.SessionID, call.session)
		decodeInternalBatch(t, call, receipt.EventID)
		call.answer <- nativeSubmitAnswer{workID: "shutdown-admitted", complete: true, final: final}
	case err := <-p.done:
		p.stopped = true
		t.Fatalf("Core exited with %v before native submission; event: %+v", err, internalRows(t, f, "event_id=?", receipt.EventID))
	case <-time.After(5 * time.Second):
		t.Fatal("native submission did not reach the peer during shutdown")
	}
	p.waitStopped(t, false)
	require.NotEmpty(t, waitInternalEvent(t, f, receipt.EventID, "sent").SentAt)
	p = f.start()
	rebound := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, router.SessionID, rebound.SessionID)
	require.Equal(t, router.SessionUID, rebound.SessionUID)
	requireEmbeddedHistory(t, p, rebound.ID, final)
	peer.quiet(t, 300*time.Millisecond)
}

func TestEmbeddedMASSRestartAfterLostSettlement(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	f, config, operatorFile := embeddedProcessFixture(t, peer)
	write(t, f.config, config)
	gate := t.TempDir()
	const stage = "internal_event_before_settlement"
	p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "released") })
	router := waitBinding(t, p, "router", "", "ready")
	receipt := submit(t, p, f, input("embedded-mass-crash"))
	first := peer.next(t, "turn/start")
	require.Equal(t, router.SessionID, first.session)
	decodeInternalBatch(t, first, receipt.EventID)
	const original = "native output before Core settlement"
	first.answer <- nativeSubmitAnswer{workID: "before-crash", complete: true, final: original}
	waitWorkGate(t, gate, stage)
	require.Empty(t, waitInternalEvent(t, f, receipt.EventID, "dispatching").SentAt)
	requireEmbeddedHistory(t, p, router.ID, original)
	peer.quiet(t, 300*time.Millisecond)
	p.stop(t, true)
	require.Empty(t, waitInternalEvent(t, f, receipt.EventID, "dispatching").SentAt)

	p = f.start()
	blocked := waitBinding(t, p, "router", "", "blocked")
	require.Equal(t, router.ID, blocked.ID)
	require.Equal(t, router.SessionID, blocked.SessionID)
	require.Equal(t, router.SessionUID, blocked.SessionUID)
	require.Equal(t, "session_native_owner_unknown", blocked.LastError)
	requireEmbeddedHistory(t, p, blocked.ID, original)
	require.Empty(t, waitInternalEvent(t, f, receipt.EventID, "dispatching").SentAt)
	peer.quiet(t, 400*time.Millisecond)

	recovery := recoverRoleBinding(t, p, operatorFile, recoveryFor("retry-embedded-crash", "retry", router.ID))
	require.Equal(t, router.SessionID, recovery.SessionID)
	require.Equal(t, router.SessionUID, recovery.SessionUID)
	require.NotEmpty(t, recovery.ResumedResource)
	require.Empty(t, recovery.ClosedResource)
	rebound := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, router.ID, rebound.ID)
	require.True(t, rebound.PreviousOwnerUnknown)
	retry := peer.next(t, "turn/start")
	require.Equal(t, first.session, retry.session)
	require.Equal(t, first.text, retry.text)
	const recovered = "native output from the recovered dispatch"
	retry.answer <- nativeSubmitAnswer{workID: "after-crash", complete: true, final: recovered}
	require.NotEmpty(t, waitInternalEvent(t, f, receipt.EventID, "sent").SentAt)
	requireEmbeddedHistory(t, p, rebound.ID, original, recovered)
	peer.quiet(t, 300*time.Millisecond)
	p.stop(t, false)

	p = f.start()
	rebound = waitBinding(t, p, "router", "", "ready")
	require.Equal(t, router.SessionID, rebound.SessionID)
	require.Equal(t, router.SessionUID, rebound.SessionUID)
	require.NotEmpty(t, waitInternalEvent(t, f, receipt.EventID, "sent").SentAt)
	requireEmbeddedHistory(t, p, rebound.ID, original, recovered)
	peer.quiet(t, 400*time.Millisecond)
	t.Log("Observed two native submissions of the same event on one Session, including one operator-authorized retry after the crash")
}

func embeddedProcessFixture(t *testing.T, peer *nativeSubmitPeer) (*fixture, string, string) {
	t.Helper()
	_, relay := dispatchBinaries(t)
	f, operatorFile := eventFixture(t, "ws://127.0.0.1:1")
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	seedEmbeddedRuntime(t, filepath.Join(f.data, "massd"), relay, peer.listener.Addr().String())
	return f, strings.Replace(string(config), "massd:\n  endpoint: ws://127.0.0.1:1\n", "", 1), operatorFile
}

func requireEmbeddedHistory(t *testing.T, p *process, bindingID string, messages ...string) {
	t.Helper()
	path := "/apis/core/v1/session-bindings/" + bindingID + "/history"
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		status, history := request(t, http.MethodGet, p.endpoint, path, "", nil)
		assert.Equal(collect, http.StatusOK, status, string(history))
		for _, message := range messages {
			assert.Contains(collect, string(history), message)
		}
	}, 5*time.Second, 25*time.Millisecond)
}

func waitCoreListenerClosed(t *testing.T, p *process) {
	t.Helper()
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	require.Eventually(t, func() bool {
		conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(p.endpoint, "http://"))
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 5*time.Second, 25*time.Millisecond)
}

func seedEmbeddedRuntime(t *testing.T, data, relay, addr string) {
	t.Helper()
	instance, err := massd.Start(t.Context(), massd.Options{DataDir: data, Transport: transport.Options{WriteTimeout: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })
	c, err := client.NewInProcess(instance.Handler().Connect, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	var runtimes []resource.Runtime
	require.NoError(t, c.ListRuntimes(t.Context(), resource.RuntimeQuery{}, &runtimes))
	require.Len(t, runtimes, 2)
	require.NoError(t, c.CreateRuntime(t.Context(), resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{
			"executable": relay, "envPolicy": "replace", "env": testpeer.RuntimeEnv(map[string]string{"CONFIG_PEER_ADDR": addr}),
		}},
	}, nil))
	require.NoError(t, c.Close())
	require.NoError(t, instance.Shutdown(t.Context()))
}

func startHandoffMassd(t *testing.T, binary, data string) (string, *client.Client, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "massd.yaml")
	token := strings.Repeat("E", 48)
	tokenFile := filepath.Join(dir, "massd.token")
	write(t, tokenFile, token)
	write(t, cfg, "{}")
	command := exec.CommandContext(context.Background(), binary, "--config", cfg, "--data", data, "--listen", "127.0.0.1:0", "--token-file", tokenFile)
	command.Env = []string{"HOME=" + dir, "PATH=" + dir}
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			require.NoError(t, err, "%s", stderr.String())
		case <-time.After(20 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("standalone MASS did not settle")
		}
	})
	startup := make(chan string, 1)
	go func() {
		var result struct{ URL string }
		_ = json.NewDecoder(stdout).Decode(&result)
		startup <- result.URL
	}()
	var endpoint string
	select {
	case endpoint = <-startup:
		require.NotEmpty(t, endpoint)
	case <-time.After(10 * time.Second):
		t.Fatal("standalone MASS did not report readiness")
	}
	c, err := client.New(endpoint, token, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return endpoint, c, token
}

func TestEmbeddedMASSConfigAndStartupFailure(t *testing.T) {
	f, _ := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	const external = "massd:\n  endpoint: ws://127.0.0.1:1\n"
	embedded := strings.Replace(string(saved), external, "", 1)
	for _, config := range []string{"null", "{}", "{tokenFile: ./external.token}", "{endpoint: http://127.0.0.1}", "{embedded: {dataDir: ./other}}"} {
		write(t, f.config, embedded+"massd: "+config+"\n")
		_, err := core.LoadConfig(f.config)
		require.Error(t, err, "%q", config)
	}
	write(t, f.config, embedded)
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Nil(t, cfg.Massd)

	// Fail after MASS prepared but before Core became ready, then reuse both stores.
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	cfg.Listen = occupied.Addr().String()
	_, err = core.StartWithContext(t.Context(), cfg, core.TelemetryOptions{})
	require.ErrorContains(t, err, "cannot bind listen address")
	require.NoError(t, occupied.Close())
	s, err := core.StartWithContext(t.Context(), cfg, core.TelemetryOptions{})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Shutdown(deadline))
	require.NoError(t, <-done)
}

func TestCoreRejectsDirectoryAliases(t *testing.T) {
	for _, name := range []string{"dataDir", "massd", "credentials"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			alias := data
			if name != "dataDir" {
				require.NoError(t, os.Mkdir(data, 0o700))
				alias = filepath.Join(data, name)
			}
			target := t.TempDir()
			require.NoError(t, os.Symlink(target, alias))
			cfg := core.Config{DataDir: data, Listen: "127.0.0.1:0"}
			_, err := core.StartWithContext(t.Context(), cfg, core.TelemetryOptions{})
			require.ErrorContains(t, err, name)
			entries, err := os.ReadDir(target)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
