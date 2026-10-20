//go:build proxy

package service_test

import (
	"bufio"
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicit integration target: requires the actual Caddy CLI, never substitutes
// a fake proxy. All listeners, config, storage and processes belong to this test.
func TestSSEThroughCaddyDeliversFramesWhileOpen(t *testing.T) {
	caddy, err := exec.LookPath("caddy")
	require.NoError(t, err)
	fixture := &SessionWorkSuite{}
	fixture.SetT(t)
	fixture.SetupTest()
	handler, err := transport.New(fixture.svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	defer handler.Shutdown()
	origin := httptest.NewServer(handler)
	defer origin.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	dir := t.TempDir()
	config := filepath.Join(dir, "Caddyfile")
	require.NoError(t, os.WriteFile(config, []byte(fmt.Sprintf(`{
 admin off
 persist_config off
 auto_https off
}
http://%s {
 reverse_proxy %s {
  flush_interval -1
  stream_timeout 1m
 }
}
`, address, strings.TrimPrefix(origin.URL, "http://"))), 0600))
	log, err := os.Create(filepath.Join(dir, "caddy.log"))
	require.NoError(t, err)
	defer log.Close()
	cmd := exec.Command(caddy, "run", "--config", config, "--adapter", "caddyfile")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir, "XDG_DATA_HOME="+dir)
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		require.NoError(t, cmd.Process.Signal(os.Interrupt))
		select {
		case err := <-exited:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Error("proxy did not shut down gracefully")
		}
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		require.Error(t, err, "proxy listener must be released")
	}()
	client := &http.Client{Timeout: 25 * time.Second}
	require.Eventually(t, func() bool {
		res, err := client.Get("http://" + address + "/livez")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == 200
	}, 5*time.Second, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/apis/v1/sessions/original/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Accept", "text/event-stream")
	started := time.Now()
	res, err := client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	require.Equal(t, "no-cache, no-store, no-transform", res.Header.Get("Cache-Control"))
	require.Empty(t, res.Header.Get("Content-Encoding"))
	reader := bufio.NewReader(res.Body)
	frame, err := readFrame(reader)
	require.NoError(t, err)
	require.Equal(t, "event: subscription.ready\ndata: {}\n\n", frame)
	require.Less(t, time.Since(started), 2*time.Second)
	t.Logf("ready received after %s", time.Since(started))
	for i := 0; i < 2; i++ {
		e := fixture.fact(fmt.Sprint(i), events.AgentMessage{MessageID: fmt.Sprint(i), Content: []events.ContentBlock{events.Text("proxy frame")}})
		sent := time.Now()
		require.NoError(t, fixture.driver.pipe.Send(ctx, e))
		frame, err = readFrame(reader)
		require.NoError(t, err)
		require.Contains(t, frame, e.EventID)
		require.NotContains(t, frame, "\nid:")
		require.NotContains(t, frame, "\nretry:")
		require.Less(t, time.Since(sent), 2*time.Second)
		t.Logf("business frame %d received after %s", i, time.Since(sent))
	}
	idle := time.Now()
	frame, err = readFrame(reader)
	require.NoError(t, err)
	require.Equal(t, ": heartbeat\n\n", frame)
	require.Less(t, time.Since(idle), 15*time.Second)
	t.Logf("heartbeat received after %s while stream remains open", time.Since(idle))
	handler.Shutdown()
	frame, err = readFrame(reader)
	require.NoError(t, err)
	require.Contains(t, frame, "subscription.error")
	require.Contains(t, frame, "server_shutdown")
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF)
}
