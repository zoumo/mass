package core_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/transport"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestStandaloneStatusProbes(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	for path, outcome := range map[string]string{"/livez": "alive", "/readyz": "ready"} {
		status, body := request(t, http.MethodGet, p.endpoint, path, "", nil)
		require.Equal(t, http.StatusOK, status)
		require.JSONEq(t, `{"status":"`+outcome+`"}`, string(body))
	}
	for _, path := range []string{"/livez", "/readyz", "/status"} {
		status, _ := request(t, http.MethodPost, p.endpoint, path, "", nil)
		require.Equal(t, http.StatusMethodNotAllowed, status)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, p.endpoint+path, http.NoBody)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://foreign.example.test")
		response, err := (&http.Client{Timeout: time.Second}).Do(req)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusForbidden, response.StatusCode)
	}
	require.Equal(t, "not_started", checkStandaloneStackStatus(t, p, "ready", "disabled").Router)
	for _, path := range []string{"/livez", "/readyz", "/jsonrpc"} {
		status, _ := request(t, http.MethodGet, p.endpoint, "/massd"+path, "", nil)
		require.Equal(t, http.StatusNotFound, status)
	}
}

func TestStandaloneStatusWithExternalMassd(t *testing.T) {
	token := strings.Repeat("e", 48)
	instance, err := massd.Start(t.Context(), massd.Options{
		DataDir: t.TempDir(), Transport: transport.Options{Token: token, WriteTimeout: time.Second},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, instance.Shutdown(ctx))
	})
	external := httptest.NewServer(instance.Handler())
	t.Cleanup(external.Close)
	endpoint := "ws" + strings.TrimPrefix(external.URL, "http")
	f, _ := eventFixture(t, endpoint)
	tokenFile := filepath.Join(f.dir, "massd.token")
	write(t, tokenFile, token)
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(config), "  endpoint: "+endpoint+"\n", "  endpoint: "+endpoint+"\n  tokenFile: "+tokenFile+"\n", 1))

	p := f.start()
	checkStandaloneStackStatus(t, p, "ready", "ready", token, endpoint, external.URL)
	status, _ := request(t, http.MethodGet, p.endpoint, "/massd/jsonrpc", token, nil)
	require.Equal(t, http.StatusNotFound, status)

	external.Close()
	checkStandaloneStackStatus(t, p, "degraded", "unavailable", token, endpoint, external.URL)
	status, body := request(t, http.MethodGet, p.endpoint, "/readyz", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"status":"ready"}`, string(body))
}

func checkStandaloneStackStatus(t *testing.T, p *process, overall, massState string, private ...string) core.CoreStatus {
	t.Helper()
	status, body := request(t, http.MethodGet, p.endpoint, "/status", "", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var got struct {
		Status   string               `json:"status"`
		Core     core.CoreStatus      `json:"core"`
		Massd    core.ComponentStatus `json:"massd"`
		Triggers core.TriggerStatuses `json:"triggers"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, overall, got.Status)
	require.Equal(t, "ready", got.Core.Status)
	require.NotNil(t, got.Core.TimerStates)
	require.Equal(t, massState, got.Massd.Status)
	require.Equal(t, massState == "ready", got.Massd.ObservedAt != nil)
	require.Equal(t, "not_managed", got.Triggers.GitHub.Status)
	require.Equal(t, "not_managed", got.Triggers.DWS.Status)
	for _, text := range private {
		require.NotContains(t, string(body), text)
	}
	return got.Core
}
