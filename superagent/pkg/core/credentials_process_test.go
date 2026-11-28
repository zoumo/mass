package core_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestCoreInstanceCredentialsProcess(t *testing.T) {
	f := fixtureAt(t)
	dir := filepath.Join(f.data, "credentials")
	adminPath := filepath.Join(dir, "admin.token")
	triggerPath := filepath.Join(dir, "triggers.token")
	require.NoDirExists(t, dir)
	raw, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, fixedListen(t, strings.Replace(string(raw), f.data, "./data", 1)))

	p := f.startConfigured()
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	admin := instanceToken(t, adminPath)
	trigger := instanceToken(t, triggerPath)
	require.NotEqual(t, admin, trigger)
	command := exec.CommandContext(t.Context(), build(t), "scheduled-tasks", "--config", f.config, "list")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.JSONEq(t, `{"items":[]}`, string(output))

	for _, tc := range []struct {
		token string
		state int
	}{
		{admin, http.StatusOK},
		{trigger, http.StatusUnauthorized},
		{"", http.StatusUnauthorized},
	} {
		status, _ := request(t, http.MethodGet, p.endpoint, "/apis/core/v1/scheduled-tasks", tc.token, nil)
		require.Equal(t, tc.state, status)
	}
	status, _ := request(t, http.MethodPost, p.endpoint, "/apis/core/v1/trigger-messages", admin, input("instance-token"))
	require.Equal(t, http.StatusUnauthorized, status)
	f.token = trigger
	receipt := submit(t, p, f, input("instance-token"))
	mount := "ws" + strings.TrimPrefix(p.endpoint, "http") + "/apis/massd"
	c := mountedMASSClient(t, mount, admin, 8)
	var runtime resource.Runtime
	require.NoError(t, c.GetRuntime(t.Context(), "codex", &runtime))
	require.NotEmpty(t, runtime.Metadata.UID)
	require.NoError(t, c.Close())
	p.stop(t, false)
	for _, token := range []string{admin, trigger} {
		require.NotContains(t, p.stderr.String(), token)
	}

	p = f.startConfigured()
	require.Equal(t, admin, instanceToken(t, adminPath))
	require.Equal(t, trigger, instanceToken(t, triggerPath))
	require.Equal(t, receipt.MessageID, submit(t, p, f, input("instance-token")).MessageID)
	c = mountedMASSClient(t, "ws"+strings.TrimPrefix(p.endpoint, "http")+"/apis/massd", admin, 8)
	var restored resource.Runtime
	require.NoError(t, c.GetRuntime(t.Context(), "codex", &restored))
	require.Equal(t, runtime.Metadata.UID, restored.Metadata.UID)
}

func TestCoreConcurrentCredentialInitialization(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, existing := range []string{"fresh", "admin.token", "triggers.token"} {
		t.Run(existing, func(t *testing.T) {
			f := fixtureAt(t)
			const provisioned = "fixture-credential-provisioned-before-startup"
			var existingPath string
			if existing != "fresh" {
				existingPath = f.seedToken(existing, provisioned)
			}
			cfg, err := core.LoadConfig(f.config)
			require.NoError(t, err)
			type outcome struct {
				server *core.Server
				err    error
			}
			start := make(chan struct{})
			results := make(chan outcome, 2)
			for range 2 {
				go func() {
					<-start
					server, err := core.StartWithContext(t.Context(), cfg, core.TelemetryOptions{})
					results <- outcome{server, err}
				}()
			}
			close(start)
			owners := 0
			var failures []error
			for range 2 {
				result := <-results
				if result.err != nil {
					failures = append(failures, result.err)
					continue
				}
				owners++
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- result.server.Serve() }()
					require.NoError(t, result.server.Shutdown(ctx))
					require.NoError(t, <-done)
				})
			}
			require.Equal(t, 1, owners)
			require.Len(t, failures, 1)
			require.ErrorContains(t, failures[0], "already owned by another Core")
			dir := filepath.Join(cfg.DataDir, "credentials")
			require.NotEqual(t, instanceToken(t, cfg.AdminTokenPath()), instanceToken(t, filepath.Join(dir, "triggers.token")))
			if existingPath != "" {
				require.Equal(t, provisioned, instanceToken(t, existingPath))
			}
		})
	}
}

func TestCorePreservesInvalidCredentialFiles(t *testing.T) {
	const valid = "private-fixture-token-owned-by-core"
	for _, tc := range []struct {
		name, token, failure string
		mode                 os.FileMode
	}{
		{"malformed", "short secret", "at least 32 bytes", 0o600},
		{"public", valid, "mode 0600", 0o644},
		{"role scope", "cap_" + valid, "distinct from role MCP", 0o600},
		{"same scope", valid, "contain distinct tokens", 0o600},
		{"external scope", valid + "-external", "distinct from role MCP and external MASS", 0o600},
		{"symlink", valid, "mode 0600", 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixtureAt(t)
			admin := f.seedToken("admin.token", valid)
			trigger := f.seedToken("triggers.token", tc.token)
			require.NoError(t, os.Chmod(trigger, tc.mode))
			if tc.name == "external scope" {
				write(t, filepath.Join(f.dir, "external.token"), tc.token)
				data, err := os.ReadFile(f.config)
				require.NoError(t, err)
				write(t, f.config, string(data)+"massd: {endpoint: 'ws://127.0.0.1:1/jsonrpc', tokenFile: ./external.token}\n")
			}
			source := trigger
			if tc.name == "symlink" {
				source = filepath.Join(f.dir, "token")
				require.NoError(t, os.Rename(trigger, source))
				require.NoError(t, os.Symlink(source, trigger))
			}
			f.fails(tc.failure)
			raw, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, tc.token, string(raw))
			info, err := os.Stat(source)
			require.NoError(t, err)
			require.Equal(t, tc.mode, info.Mode().Perm())
			require.Equal(t, valid, instanceToken(t, admin))
			if tc.name == "symlink" {
				target, err := os.Readlink(trigger)
				require.NoError(t, err)
				require.Equal(t, source, target)
			}
		})
	}
}

func instanceToken(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	token := strings.TrimSpace(string(data))
	require.GreaterOrEqual(t, len(token), 32)
	return token
}
