package integration_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
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
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type WebSocketAuthSuite struct{ suite.Suite }

// Exercise actual daemon/client entrypoints with test-owned credentials and TLS;
// this does not call native Agents or use personal accounts.
func (webSocketAuthSuite *WebSocketAuthSuite) TestAuthenticatedDaemon() {
	t := webSocketAuthSuite.T()
	owned := t.TempDir()
	tokenPath := filepath.Join(owned, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("synthetic-http-token\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MASS HTTP acceptance"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPath, keyPath := filepath.Join(owned, "cert.pem"), filepath.Join(owned, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, app, "--data", filepath.Join(owned, "data"), "--listen", "127.0.0.1:0", "--token-file", tokenPath, "--tls-cert", certPath, "--tls-key", keyPath, "--session-idle-timeout", "5s")
	// Isolate daemon default configuration without relying on a particular CLI flag.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+owned)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var ready struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		waited = true
		require.NoError(t, err, stderr.String())
	}
	require.True(t, strings.HasPrefix(ready.URL, "wss://"))
	require.True(t, strings.HasSuffix(ready.URL, "/rpc"))
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(certPEM))
	wire := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer wire.CloseIdleConnections()
	httpClient := &http.Client{Transport: wire, Timeout: 5 * time.Second}
	httpsURL := "https://" + strings.TrimPrefix(strings.TrimSuffix(ready.URL, "/rpc"), "wss://")
	for _, path := range []string{"/rpc"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpsURL+path, http.NoBody)
		require.NoError(t, err)
		response, err := httpClient.Do(request)
		require.NoError(t, err)
		require.Equal(t, 401, response.StatusCode)
		require.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
		response.Body.Close()
	}
	probe, err := http.NewRequestWithContext(ctx, http.MethodGet, httpsURL+"/readyz", http.NoBody)
	require.NoError(t, err)
	response, err := httpClient.Do(probe)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	response.Body.Close()
	var rows []resource.Runtime
	c := newHTTPClient(t, ready.URL, "synthetic-http-token")
	c.SetTLSClientConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(t, c.ListRuntimes(ctx, resource.RuntimeQuery{}, &rows))
	require.NotNil(t, rows)
	require.Empty(t, rows)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, cmd.Wait())
	waited = true
	conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(t.Context(), "tcp", strings.TrimPrefix(httpsURL, "https://"))
	if conn != nil {
		conn.Close()
	}
	require.Error(t, err, "owned TLS listener must be released")
	for _, args := range [][]string{
		{"--data", filepath.Join(owned, "denied"), "--listen", "127.0.0.1:0"},
		{"--data", filepath.Join(owned, "denied"), "--listen", "0.0.0.0:0", "--local"},
		{"--data", filepath.Join(owned, "denied"), "--listen", "0.0.0.0:0", "--token-file", tokenPath},
		{"--data", filepath.Join(owned, "denied"), "--listen", "127.0.0.1:0", "--local", "--session-idle-timeout=-1s"},
	} {
		invalid := exec.CommandContext(ctx, app, args...)
		invalid.Env = cmd.Env
		require.Error(t, invalid.Run(), "unsafe or unspecified access mode must fail startup")
	}
	t.Log("PASS authenticated TLS WebSocket daemon/client with 5s idle configuration, unauthenticated RPC rejected, probes, explicit access-mode and negative-timeout rejection, and listener cleanup")
}

func TestWebSocketAuthSuite(t *testing.T) { suite.Run(t, new(WebSocketAuthSuite)) }
