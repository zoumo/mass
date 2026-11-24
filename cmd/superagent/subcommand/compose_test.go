package subcommand

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComposeConfigFlag(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		failure string
	}{
		{[]string{"compose"}, "required flag(s) \"config\""},
		{[]string{"compose", "--massd-bin", "massd"}, "unknown flag: --massd-bin"},
		{[]string{"compose", "--core-config", "core.yaml"}, "unknown flag: --core-config"},
	} {
		command := New()
		command.SetArgs(tc.args)
		require.ErrorContains(t, command.Execute(), tc.failure)
	}
}

func TestComposeConfigurationDocument(t *testing.T) {
	valid := "core: {listen: 127.0.0.1:0}\n"
	c, err := decodeComposeConfig(strings.NewReader(valid))
	require.NoError(t, err)
	require.YAMLEq(t, "listen: 127.0.0.1:0", string(c.core))
	require.Empty(t, c.triggers)
	require.Zero(t, c.watchdogInterval)
	c, err = decodeComposeConfig(strings.NewReader(valid + "triggers: {github: {accountId: fake-account}, dws: {source: dws}}\nwatchdog: {interval: 10m}\n"))
	require.NoError(t, err)
	require.YAMLEq(t, "accountId: fake-account", string(c.triggers["github"]))
	require.YAMLEq(t, "source: dws", string(c.triggers["dws"]))
	require.Equal(t, 10*time.Minute, c.watchdogInterval)
	const secret = "PRIVATE-FIXTURE-CONFIG-VALUE"
	for _, tc := range []struct {
		name, input, failure string
	}{
		{"removed massd section", valid + "massd: {}", "unknown Compose configuration section at line 2"},
		{"missing core", "triggers: {}", "missing core configuration"},
		{"unknown", valid + secret + ": {}", "unknown Compose configuration section at line 2"},
		{"duplicate section", valid + "core: {}\n", "duplicate key at line 2"},
		{"duplicate component key", "core: {listen: " + secret + ", Listen: ''}", "core configuration contains a duplicate key at line 1"},
		{"duplicate item key", "core: {sources: [{name: " + secret + ", name: duplicate}]}", "core configuration contains a duplicate key at line 1"},
		{"nonmapping root", "- " + secret, "compose configuration must be a mapping"},
		{"null section", "core: null", "core configuration must be a mapping"},
		{"nonmapping section", "core: " + secret, "core configuration must be a mapping"},
		{"nonstring key", "core: {0: " + secret + "}", "core configuration requires string keys at line 1"},
		{"trigger section scalar", valid + "triggers: " + secret, "triggers configuration must be a mapping"},
		{"unknown trigger", valid + "triggers: {" + secret + ": {}}", "unknown triggers configuration section at line 2"},
		{"duplicate trigger", valid + "triggers: {dws: {}, DWS: {}}", "triggers configuration contains a duplicate key at line 2"},
		{"trigger component null", valid + "triggers: {github: null}", "triggers.github configuration must be a mapping"},
		{"trigger duplicate nested field", valid + "triggers: {dws: {direct: {prompt: " + secret + ", Prompt: ''}}}", "triggers.dws configuration contains a duplicate key at line 2"},
		{"watchdog missing interval", valid + "watchdog: {}", "watchdog.interval must be a positive duration string"},
		{"watchdog null", valid + "watchdog: null", "watchdog configuration must be a mapping"},
		{"watchdog duplicate interval", valid + "watchdog: {interval: 10m, interval: 1h}", "watchdog configuration contains a duplicate key"},
		{"watchdog unknown field", valid + "watchdog: {interval: 1m, unused: " + secret + "}", "unknown watchdog configuration field"},
		{"watchdog invalid duration", valid + "watchdog: {interval: " + secret + "}", "watchdog.interval must be a positive duration string"},
		{"watchdog numeric duration", valid + "watchdog: {interval: 10}", "watchdog.interval must be a positive duration string"},
		{"watchdog zero duration", valid + "watchdog: {interval: 0s}", "watchdog.interval must be a positive duration string"},
		{"watchdog negative duration", valid + "watchdog: {interval: -1s}", "watchdog.interval must be a positive duration string"},
		{"second document", valid + "---\n" + secret, "one YAML document"},
		{"malformed YAML", "core: [" + secret + "\n", "cannot read Compose configuration as YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeComposeConfig(strings.NewReader(tc.input))
			require.ErrorContains(t, err, tc.failure)
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestComposeRejectsUnknownCoreFields(t *testing.T) {
	for _, value := range []string{"null", "{}", "PRIVATE-FIXTURE-CONFIG-VALUE"} {
		path := filepath.Join(t.TempDir(), "stack.yaml")
		data := "core: {listen: '127.0.0.1:0', unused: " + value + "}\n"
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		command := New()
		command.SetArgs([]string{"compose", "--config", path})
		err := command.Execute()
		require.ErrorContains(t, err, "core configuration: invalid_argument: configuration contains unknown or invalid fields")
		require.NotContains(t, err.Error(), "PRIVATE-FIXTURE-CONFIG-VALUE")
	}
}

func TestComposeRejectsInvalidEmbeddedMASS(t *testing.T) {
	for _, tc := range []struct{ config, errorText string }{
		{"{endpoint: ws://127.0.0.1:24368}", "Compose uses core.massd.embedded for MASS settings"},
		{"{tokenFile: ./source.token}", "Compose uses core.massd.embedded for MASS settings"},
		{"{embedded: {writeTimeout: 10}}", "massd.embedded durations must be strings"},
		{"{embedded: {writeTimeout: 0s}}", "massd.embedded.writeTimeout must be positive"},
		{"{embedded: {expose: {}}}", "massd.embedded.expose.tokenFile is required"},
		{"{embedded: {unused: null}}", "configuration contains unknown or invalid fields"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "stack.yaml")
			data := composeFixtureConfig(t, dir, "127.0.0.1:0", false, false) + "  massd: " + tc.config + "\n"
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			command := New()
			command.SetArgs([]string{"compose", "--config", path})
			require.ErrorContains(t, command.Execute(), tc.errorText)
			require.NoDirExists(t, filepath.Join(dir, "massd-data"))
		})
	}
}

func TestCoreStartupAndReadiness(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	endpoint := server.URL
	child := startStubCore(t, fmt.Sprintf("printf '%%s\\n' '{\"status\":\"ready\",\"endpoint\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	actual, err := child.waitReady(ctx, parseFixtureCoreStartup, "/readyz")
	require.NoError(t, err)
	require.Equal(t, endpoint, actual)
	require.GreaterOrEqual(t, requests.Load(), int32(2))
	require.NoError(t, child.stop(time.Second))
	require.Equal(t, syscall.SIGTERM, child.cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
}

func TestCoreStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, failure string
		deadline              time.Duration
		exitCode              int
	}{
		{name: "early exit", script: "exit 42", failure: "core", deadline: 5 * time.Second, exitCode: 42},
		{name: "invalid startup output", script: "printf 'invalid\\n'; exec /bin/sleep 10", failure: "invalid core startup record", deadline: 5 * time.Second},
		{name: "startup timeout", script: "exec /bin/sleep 10", failure: "waiting for core startup: context deadline exceeded", deadline: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := startStubCore(t, tc.script)
			ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
			defer cancel()
			_, err := child.waitReady(ctx, parseFixtureCoreStartup, "/readyz")
			require.ErrorContains(t, err, tc.failure)
			if tc.exitCode != 0 {
				select {
				case <-child.done:
					require.Equal(t, tc.exitCode, child.cmd.ProcessState.ExitCode())
				case <-ctx.Done():
					t.Fatal("core process was not reaped")
				}
			}
		})
	}
}

func TestCoreReadinessTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	endpoint := server.URL
	child := startStubCore(t, fmt.Sprintf("printf '%%s\\n' '{\"status\":\"ready\",\"endpoint\":%q}'\nexec /bin/sleep 30\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := child.waitReady(ctx, parseFixtureCoreStartup, "/readyz")
	require.ErrorContains(t, err, "waiting for core readiness: context deadline exceeded")
}

func TestCoreForcedShutdownWaitsForExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := server.URL
	child := startStubCore(t, fmt.Sprintf("trap '' TERM\nprintf '%%s\\n' '{\"status\":\"ready\",\"endpoint\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := child.waitReady(ctx, parseFixtureCoreStartup, "/readyz")
	require.NoError(t, err)
	require.ErrorContains(t, child.stop(10*time.Millisecond), "core exceeded shutdown timeout")
	require.NotNil(t, child.cmd.ProcessState)
}

func TestComposeStartupWildcardEndpoints(t *testing.T) {
	for _, tc := range []struct {
		listen, reported, expected string
	}{
		{"0.0.0.0:0", "http://0.0.0.0:12111", "http://127.0.0.1:12111"},
		{"0.0.0.0:0", "http://[::]:12111", "http://127.0.0.1:12111"},
		{"[::]:0", "http://[::]:12111", "http://[::1]:12111"},
	} {
		record := fmt.Appendf(nil, `{"status":"ready","endpoint":%q}`, tc.reported)
		actual, err := coreStartupEndpoint(record, tc.listen)
		require.NoError(t, err)
		require.Equal(t, tc.expected, actual.String())
	}
}

func parseFixtureCoreStartup(line []byte) (*url.URL, error) {
	return coreStartupEndpoint(line, "127.0.0.1:0")
}

func startStubCore(t *testing.T, script string) *childProcess {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "core")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o700))
	cmd := exec.CommandContext(context.Background(), binary)
	cmd.Stderr = io.Discard
	child, err := startChild("core", cmd)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, child.stop(time.Second))
		require.NotNil(t, child.cmd.ProcessState)
	})
	return child
}
