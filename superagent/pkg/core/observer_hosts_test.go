package core_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestReadAllowedHostsConfig(t *testing.T) {
	f := fixtureAt(t)
	base, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, string(base)+"readAllowedHosts: [observer.example.com:5970, other.example.com]\n")
	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, []string{"observer.example.com:5970", "other.example.com"}, config.ReadAllowedHosts)

	for _, invalid := range []string{
		"observer.example.com",
		"[true]",
		"[123]",
		"['']",
		"['*.example.com:5970']",
		"[http://observer.example.com:5970]",
		"[observer.example.com/observer/]",
		"[user@observer.example.com]",
		"[observer.example.com:]",
		"[observer.example.com:0]",
		"[observer.example.com:65536]",
		"[observer.example.com:abc]",
		"[observer..example.com]",
		"[-observer.example.com]",
		"[observer_example.com]",
		"[127.0.0.1:5970]",
	} {
		t.Run(invalid, func(t *testing.T) {
			write(t, f.config, string(base)+"readAllowedHosts: "+invalid+"\n")
			_, err := core.LoadConfig(f.config)
			require.ErrorContains(t, err, "readAllowedHosts")
		})
	}
}

func TestReadAllowedHostsHTTP(t *testing.T) {
	const allowed = "observer.example.com:5970"
	const health = "/v1/observer/health"
	f := fixtureAt(t)
	base, err := os.ReadFile(f.config)
	require.NoError(t, err)
	c := &http.Client{Timeout: 3 * time.Second}
	requestStatus := func(t *testing.T, p *process, method, path, host, origin, fetchSite string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, p.endpoint+path, http.NoBody)
		require.NoError(t, err)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		res, err := c.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}

	defaultServer := f.start()
	require.Equal(t, http.StatusOK, requestStatus(t, defaultServer, http.MethodGet, health, "", "", ""))
	require.Equal(t, http.StatusForbidden, requestStatus(t, defaultServer, http.MethodGet, health, allowed, "", ""))
	defaultServer.stop(t, false)

	write(t, f.config, string(base)+"readAllowedHosts: ["+allowed+"]\n")
	p := f.start()
	for _, tc := range []struct {
		name, method, path, host, origin, fetchSite string
		want                                        int
	}{
		{name: "allowed GET", method: http.MethodGet, path: health, host: allowed, origin: "http://" + allowed, fetchSite: "same-origin", want: http.StatusOK},
		{name: "allowed HEAD", method: http.MethodHead, path: health, host: allowed, want: http.StatusOK},
		{name: "allowed Core read", method: http.MethodGet, path: "/v1/core/topics", host: allowed, origin: "http://" + allowed, fetchSite: "same-origin", want: http.StatusOK},
		{name: "operator evidence requires the direct Core host", method: http.MethodGet, path: "/v1/core/session-bindings/example/runtime-evidence", host: allowed, want: http.StatusForbidden},
		{name: "numeric GET", method: http.MethodGet, path: health, want: http.StatusOK},
		{name: "numeric POST", method: http.MethodPost, path: health, want: http.StatusMethodNotAllowed},
		{name: "allowed host POST", method: http.MethodPost, path: health, host: allowed, want: http.StatusForbidden},
		{name: "allowed host MCP", method: http.MethodPost, path: "/v1/mcp", host: allowed, want: http.StatusForbidden},
		{name: "allowed host channel", method: http.MethodPost, path: "/v1/channel/messages", host: allowed, want: http.StatusForbidden},
		{name: "allowed host telemetry", method: http.MethodPost, path: "/v1/telemetry/traces", host: allowed, want: http.StatusForbidden},
		{name: "allowed host unrelated GET", method: http.MethodGet, path: "/", host: allowed, want: http.StatusForbidden},
		{name: "host prefix", method: http.MethodGet, path: health, host: "other." + allowed, want: http.StatusForbidden},
		{name: "missing port", method: http.MethodGet, path: health, host: strings.TrimSuffix(allowed, ":5970"), want: http.StatusForbidden},
		{name: "foreign Origin", method: http.MethodGet, path: health, host: allowed, origin: p.endpoint, want: http.StatusForbidden},
		{name: "cross-site", method: http.MethodGet, path: health, host: allowed, fetchSite: "cross-site", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, requestStatus(t, p, tc.method, tc.path, tc.host, tc.origin, tc.fetchSite))
		})
	}
	for _, path := range []string{"/observer", "/observer/", "/observer/assets/missing.js"} {
		numericStatus := requestStatus(t, p, http.MethodGet, path, "", "", "")
		require.Equal(t, http.StatusNotFound, numericStatus)
		require.Equal(t, http.StatusForbidden, requestStatus(t, p, http.MethodGet, path, allowed, "", ""))
	}
}
