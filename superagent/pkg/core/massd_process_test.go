package core_test

import (
	"bufio"
	"bytes"
	"context"
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

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/massd"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestEmbeddedMASSProcessAndStandaloneHandoff(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	_ = build(t)
	massdBin, relay := dispatchBinaries(t)
	f, _ := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	const external = "massd:\n  endpoint: ws://127.0.0.1:1"
	const embedded = "massd:\n  embedded:\n    dataDir: ./massd\n    sessionIdleTimeout: 0s\n    writeTimeout: 2s"
	data := filepath.Join(f.dir, "massd")
	write(t, f.config, strings.Replace(string(saved), external, embedded, 1))
	seedEmbeddedRuntime(t, data, relay, peer.listener.Addr().String())

	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=internal_event_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	router := waitBinding(t, p, "router", "", "ready")
	liveCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	preview := "/v1/observer/session-preview/" + router.ID
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
	require.Eventually(t, func() bool {
		status, history := request(t, http.MethodGet, p.endpoint, preview+"/history", "", nil)
		return status == http.StatusOK && bytes.Contains(history, []byte("embedded persisted message"))
	}, 5*time.Second, 25*time.Millisecond)

	_, err = massd.Start(t.Context(), massd.Options{DataDir: data, Transport: transport.Options{WriteTimeout: time.Second}})
	var conflict *runtime.OperationError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "data_directory_in_use", conflict.Reason)

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	require.Eventually(t, func() bool {
		conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(p.endpoint, "http://"))
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 5*time.Second, 25*time.Millisecond)
	write(t, filepath.Join(gate, "release"), "released")
	p.waitStopped(t, false)
	waitInternalEvent(t, f, receipt.EventID, "sent")
	endpoint, c := startHandoffMassd(t, massdBin, data)
	ref := resource.SessionRef{SessionID: router.SessionID}
	var found resource.Session
	require.NoError(t, c.GetSession(t.Context(), ref, &found))
	require.Equal(t, router.SessionID, found.Status.SessionID)
	require.Equal(t, router.SessionUID, found.Metadata.UID)
	write(t, f.config, strings.Replace(string(saved), external, "massd:\n  endpoint: "+endpoint, 1))
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
			"executable": relay, "envPolicy": "replace", "env": map[string]any{"CONFIG_PEER_ADDR": addr},
		}},
	}, nil))
	require.NoError(t, c.Close())
	require.NoError(t, instance.Shutdown(t.Context()))
}

func startHandoffMassd(t *testing.T, binary, data string) (string, *client.Client) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "massd.yaml")
	write(t, cfg, "{}")
	command := exec.CommandContext(context.Background(), binary, "--config", cfg, "--data", data, "--listen", "127.0.0.1:0")
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
	c, err := client.New(endpoint, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return endpoint, c
}

func TestEmbeddedMASSConfigAndStartupFailure(t *testing.T) {
	f, _ := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	const external = "massd:\n  endpoint: ws://127.0.0.1:1"
	const embedded = "massd:\n  embedded:\n    dataDir: ./massd\n    sessionIdleTimeout: 0s"
	for _, extra := range []string{"\n  endpoint: ws://127.0.0.1:1", "\n  tokenFile: ./source.token", "\n    writeTimeout: 0s", "\n    writeTimeout: 2", "\n    unknown: value"} {
		write(t, f.config, strings.Replace(string(saved), external, embedded+extra, 1))
		_, err := core.LoadConfig(f.config)
		require.Error(t, err, "%q", extra)
	}
	write(t, f.config, strings.Replace(string(saved), external, embedded, 1))
	_, err = core.LoadComposeConfig(f.config)
	require.ErrorContains(t, err, "cannot be combined with compose")
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.NotNil(t, cfg.Massd.Embedded.SessionIdleTimeout)
	require.Zero(t, *cfg.Massd.Embedded.SessionIdleTimeout)

	original := cfg.Massd.Embedded.DataDir
	require.NoError(t, os.MkdirAll(cfg.DataDir, 0o700))
	alias := filepath.Join(f.dir, "core-alias")
	require.NoError(t, os.Symlink(cfg.DataDir, alias))
	cfg.Massd.Embedded.DataDir = alias
	_, err = core.Start(cfg)
	require.ErrorContains(t, err, "separate data directories")
	cfg.Massd.Embedded.DataDir = original

	// Fail after MASS prepared but before Core became ready, then reuse both stores.
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	cfg.Listen = occupied.Addr().String()
	_, err = core.Start(cfg)
	require.ErrorContains(t, err, "cannot bind listen address")
	require.NoError(t, occupied.Close())
	s, err := core.Start(cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Shutdown(deadline))
	require.NoError(t, <-done)
}
