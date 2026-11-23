package subcommand

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func checkStackStatus(t *testing.T, endpoint, overall, massd, github, dws string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/status?endpoint=http://example.invalid", http.NoBody)
	require.NoError(t, err)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	for _, private := range []string{"fixture-private-github-status", "fixture/example", endpoint, composeGitHubCore, composeGitHubToken, composeDWSToken} {
		require.NotContains(t, string(body), private)
	}
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
	require.Equal(t, massd, got.Massd.Status)
	require.Equal(t, github, got.Triggers.GitHub.Status)
	require.Equal(t, dws, got.Triggers.DWS.Status)
}

func TestComposeProcessStatusObservesStartupAndFailures(t *testing.T) {
	dir := t.TempDir()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listen := listener.Addr().String()
	require.NoError(t, listener.Close())
	p := startComposeFixture(t, dir, composeFixtureConfig(t, dir, listen, true, false), "TEST_COMPOSE_HOLD_GITHUB_STARTUP=1")
	awaitComposeEvent(t, dir, "github:start")
	coreChild := awaitComposeEvent(t, dir, "core:start")
	index := slices.Index(coreChild.Args, "--compose-status-socket")
	require.GreaterOrEqual(t, index, 0)
	require.Less(t, index+1, len(coreChild.Args))
	socket := coreChild.Args[index+1]
	info, err := os.Stat(filepath.Dir(socket))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	checkStackStatus(t, "http://"+listen, "degraded", "ready", "starting", "disabled")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "github-startup-release"), nil, 0o600))
	endpoint := p.startup(t).Endpoint
	checkStackStatus(t, endpoint, "degraded", "ready", "degraded", "disabled")
	for _, tc := range []struct{ mode, overall, github string }{
		{"ready", "ready", "ready"},
		{"malformed", "degraded", "unavailable"},
		{"wait", "degraded", "unavailable"},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "github-status-mode"), []byte(tc.mode), 0o600))
		checkStackStatus(t, endpoint, tc.overall, "ready", tc.github, "disabled")
		require.JSONEq(t, `{"status":"ready"}`, composeHTTPBody(t, endpoint+"/readyz"))
	}
	// Remove the socket name while the parent process is still running.
	require.NoError(t, os.Remove(socket))
	checkStackStatus(t, endpoint, "degraded", "ready", "unavailable", "disabled")
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, p.wait(t), readComposeFile(t, p.logs))
	require.NoDirExists(t, filepath.Dir(socket))
}
