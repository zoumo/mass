package core_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/transport"
	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	mountedMASSOrigin = "https://ops.example.test"
	mountedMASSConfig = "massd:\n  embedded:\n    dataDir: ./massd\n    sessionIdleTimeout: 0s\n    writeTimeout: 2s"
	mountedMASSExpose = "\n    expose:\n      tokenFile: ./embedded-massd.token\n      allowedOrigins: [" + mountedMASSOrigin + "]"
)

func TestEmbeddedMASSMount(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	_, relay := dispatchBinaries(t)
	f, operator := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config := strings.Replace(string(saved), "massd:\n  endpoint: ws://127.0.0.1:1", mountedMASSConfig, 1)
	data := filepath.Join(f.dir, "massd")
	seedEmbeddedRuntime(t, data, relay, peer.listener.Addr().String())
	write(t, f.config, config)
	p := f.start()
	status, _ := request(t, http.MethodGet, p.endpoint, "/massd/jsonrpc", "", nil)
	require.Equal(t, http.StatusNotFound, status)
	disabledDefaults := waitBinding(t, p, "router", "", "ready")
	p.stop(t, false)

	token := strings.Repeat("M", 48)
	write(t, filepath.Join(f.dir, "embedded-massd.token"), token+"\n")
	write(t, f.config, strings.Replace(config, mountedMASSConfig, mountedMASSConfig+mountedMASSExpose, 1))
	gate := t.TempDir()
	p = f.start("SUPERAGENT_TEST_GATE_STAGE=internal_event_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "released") })
	router := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, disabledDefaults.SessionID, router.SessionID)
	require.Equal(t, disabledDefaults.SessionUID, router.SessionUID)
	for path, state := range map[string]string{"/massd/livez": "alive", "/massd/readyz": "ready"} {
		probeStatus, body := request(t, http.MethodGet, p.endpoint, path, "", nil)
		require.Equal(t, http.StatusOK, probeStatus, string(body))
		require.JSONEq(t, `{"status":"`+state+`"}`, string(body))
	}

	mount := "ws" + strings.TrimPrefix(p.endpoint, "http") + "/massd"
	operatorToken, err := os.ReadFile(operator)
	require.NoError(t, err)
	dial := &websocket.Dialer{HandshakeTimeout: time.Second}
	for _, credential := range []string{"", f.token, string(operatorToken), roleToken(t, f, router)} {
		checkMassdUpgrade(t, dial, mount, credential, "", "", http.StatusUnauthorized)
	}
	checkMassdUpgrade(t, dial, mount, token, "", "foreign.example.test", http.StatusForbidden)
	checkMassdUpgrade(t, dial, mount, token, p.endpoint, "", http.StatusForbidden)
	status, _ = request(t, http.MethodGet, p.endpoint, "/massd/rpc", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	checkMassctlMountedSession(t, mount, token, router)

	wss, tlsConfig := startMASSProxy(t, p.endpoint)
	tlsDial := &websocket.Dialer{HandshakeTimeout: time.Second, TLSClientConfig: tlsConfig}
	checkMassdUpgrade(t, tlsDial, wss, token, mountedMASSOrigin, "", http.StatusSwitchingProtocols)
	checkMassdUpgrade(t, tlsDial, wss, token, "https://foreign.example.test", "", http.StatusForbidden)

	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	ref := resource.SessionRef{SessionID: router.SessionID}
	healthy := mountedMASSClient(t, wss, token, tlsConfig, 32)
	slow := mountedMASSClient(t, wss, token, tlsConfig, 1)
	healthySubscription, err := healthy.Subscribe(ctx, ref)
	require.NoError(t, err)
	slowSubscription, err := slow.Subscribe(ctx, ref)
	require.NoError(t, err)

	preview := "/v1/observer/session-preview/" + router.ID
	previewCtx, cancelPreview := context.WithCancel(ctx)
	defer cancelPreview()
	req, err := http.NewRequestWithContext(previewCtx, http.MethodGet, p.endpoint+preview+"/live", http.NoBody)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	frames := bufio.NewScanner(response.Body)
	require.True(t, frames.Scan(), "%v", frames.Err())
	require.JSONEq(t, `{"kind":"connected"}`, frames.Text())

	submitted := input("mounted-mass-router")
	submitted.Message.Content[0].Text.Text += " " + token
	receipt := submit(t, p, f, submitted)
	status, observed := request(t, http.MethodGet, p.endpoint, "/v1/observer/messages", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, string(observed), token)
	require.Contains(t, string(observed), "[redacted]")
	call := peer.next(t, "turn/start")
	require.Equal(t, router.SessionID, call.session)
	decodeInternalBatch(t, call, receipt.EventID)
	const final = "message through the Core mount"
	call.answer <- nativeSubmitAnswer{workID: "mounted-mass", complete: true, final: final}
	waitWorkGate(t, gate, "internal_event_before_settlement")
	require.Eventually(t, func() bool { return slowSubscription.Err() != nil }, 5*time.Second, 10*time.Millisecond)
	requireMASSSubscriptionError(t, ctx, slowSubscription, "resource_exhausted", "slow_subscriber")
	for {
		event, err := healthySubscription.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, router.SessionID, event.SessionID)
		if message, ok := event.Payload().(events.AgentMessage); ok && len(message.Content) > 0 {
			require.Equal(t, final, events.TextOf(message.Content[0]))
			break
		}
	}
	var found resource.Session
	require.NoError(t, slow.GetSession(ctx, ref, &found))
	require.Equal(t, router.SessionUID, found.Metadata.UID)
	require.True(t, frames.Scan(), "%v", frames.Err())
	var frame struct{ Kind string }
	require.NoError(t, json.Unmarshal(frames.Bytes(), &frame))
	require.Contains(t, []string{"preview", "history_changed"}, frame.Kind)
	cancelPreview()

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	require.Eventually(t, func() bool {
		conn, err := dialer.DialContext(ctx, "tcp", strings.TrimPrefix(p.endpoint, "http://"))
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 5*time.Second, 25*time.Millisecond)
	checkMassdUpgrade(t, tlsDial, wss, token, "", "", http.StatusBadGateway)
	write(t, filepath.Join(gate, "release"), "released")
	requireMASSSubscriptionError(t, ctx, healthySubscription, "unavailable", "server_shutdown")
	p.waitStopped(t, false)
	waitInternalEvent(t, f, receipt.EventID, "sent")
	instance, err := massd.Start(ctx, massd.Options{DataDir: data, Transport: transport.Options{WriteTimeout: time.Second}})
	require.NoError(t, err)
	require.NoError(t, instance.Shutdown(ctx))
}

func TestEmbeddedMASSExposureConfig(t *testing.T) {
	f, operator := eventFixture(t, "ws://127.0.0.1:1")
	saved, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config := strings.Replace(string(saved), "massd:\n  endpoint: ws://127.0.0.1:1", mountedMASSConfig+mountedMASSExpose, 1)
	path := filepath.Join(f.dir, "embedded-massd.token")
	write(t, f.config, config)
	_, err = core.LoadConfig(f.config)
	require.ErrorContains(t, err, "readable private regular file")
	operatorToken, err := os.ReadFile(operator)
	require.NoError(t, err)
	for _, credential := range []string{f.token, string(operatorToken), "cap_" + strings.Repeat("m", 48)} {
		write(t, path, credential)
		_, err := core.LoadConfig(f.config)
		require.ErrorContains(t, err, "distinct from Core credentials")
	}
	write(t, path, strings.Repeat("x", 32)+"\u2003"+strings.Repeat("x", 32))
	_, err = core.LoadConfig(f.config)
	require.ErrorContains(t, err, "without whitespace or control characters")
	write(t, path, strings.Repeat("m", 48))
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = core.LoadConfig(f.config)
	require.ErrorContains(t, err, "readable private regular file")
	require.NoError(t, os.Chmod(path, 0o600))
	cfg, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, path, cfg.Massd.Embedded.Expose.TokenFile)
	cfg.Listen = "0.0.0.0:0"
	_, err = core.Start(cfg)
	require.ErrorContains(t, err, "loopback Core listener")
	cfg.Listen = "127.0.0.1:0"
	cfg.Massd.Embedded.Expose.TokenFile = ""
	_, err = core.Start(cfg)
	require.ErrorContains(t, err, "expose.tokenFile is required")
}

func checkMassctlMountedSession(t *testing.T, mount, token string, binding core.SessionBinding) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "massctl")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/massctl")
	build.Dir = "../../.."
	build.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	log, err := build.CombinedOutput()
	require.NoError(t, err, "%s", log)
	for _, base := range []string{mount, mount + "/"} {
		command := exec.CommandContext(t.Context(), bin, "--timeout", "5s", "--url", base, "-o", "json", "session", "get", binding.SessionID)
		command.Env = []string{"MASS_TOKEN=" + token}
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		var found resource.Session
		require.NoError(t, json.Unmarshal(output, &found))
		require.Equal(t, binding.SessionID, found.Status.SessionID)
		require.Equal(t, binding.SessionUID, found.Metadata.UID)
	}
}

func checkMassdUpgrade(t *testing.T, dial *websocket.Dialer, base, token, origin, host string, status int) {
	t.Helper()
	header := make(http.Header)
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if host != "" {
		header.Set("Host", host)
	}
	conn, response, err := dial.DialContext(t.Context(), base+"/jsonrpc", header)
	if conn != nil {
		defer conn.Close()
	}
	if status == http.StatusSwitchingProtocols {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, websocket.ErrBadHandshake)
	}
	require.NotNil(t, response)
	defer response.Body.Close()
	require.Equal(t, status, response.StatusCode)
}

func startMASSProxy(t *testing.T, endpoint string) (string, *tls.Config) {
	t.Helper()
	target, err := url.Parse(endpoint)
	require.NoError(t, err)
	routes := http.NewServeMux()
	routes.Handle("/massd/", &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = target.Host
	}})
	proxy := httptest.NewTLSServer(routes)
	t.Cleanup(proxy.Close)
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	return "wss" + strings.TrimPrefix(proxy.URL, "https") + "/massd", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func mountedMASSClient(t *testing.T, endpoint, token string, tlsConfig *tls.Config, queue int) *client.Client {
	t.Helper()
	c, err := client.New(endpoint, token, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: queue})
	require.NoError(t, err)
	c.SetTLSClientConfig(tlsConfig)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func requireMASSSubscriptionError(t *testing.T, ctx context.Context, subscription *client.Subscription, code, reason string) {
	t.Helper()
	for {
		_, err := subscription.Next(ctx)
		if err != nil {
			var terminal *runtime.OperationError
			require.ErrorAs(t, err, &terminal)
			require.Equal(t, code, terminal.Code)
			require.Equal(t, reason, terminal.Reason)
			return
		}
	}
}
