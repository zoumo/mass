package subcommand

import (
	"context"
	"encoding/json"
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
		{[]string{"compose", "--massd-config", "massd.yaml"}, "unknown flag: --massd-config"},
		{[]string{"compose", "--core-config", "core.yaml"}, "unknown flag: --core-config"},
	} {
		command := New()
		command.SetArgs(tc.args)
		require.ErrorContains(t, command.Execute(), tc.failure)
	}
}

func TestComposeConfigurationDocument(t *testing.T) {
	valid := "massd: {}\ncore: {listen: 127.0.0.1:0}\n"
	c, err := decodeComposeConfig(strings.NewReader(valid))
	require.NoError(t, err)
	require.YAMLEq(t, "{}", string(c.massd))
	require.YAMLEq(t, "listen: 127.0.0.1:0", string(c.core))
	require.Empty(t, c.triggers)
	c, err = decodeComposeConfig(strings.NewReader(valid + "triggers: {github: {accountId: fake-account}, dws: {source: dws}}\n"))
	require.NoError(t, err)
	require.YAMLEq(t, "accountId: fake-account", string(c.triggers["github"]))
	require.YAMLEq(t, "source: dws", string(c.triggers["dws"]))
	const secret = "PRIVATE-FIXTURE-CONFIG-VALUE"
	for _, tc := range []struct {
		name, input, failure string
	}{
		{"missing massd", "core: {}", "missing massd configuration"},
		{"missing core", "massd: {}", "missing core configuration"},
		{"unknown", valid + secret + ": {}", "unknown Compose configuration section at line 3"},
		{"duplicate section", valid + "core: {}\n", "duplicate key at line 3"},
		{"duplicate component key", "massd: {listen: " + secret + ", Listen: ''}\ncore: {}", "massd configuration contains a duplicate key at line 1"},
		{"duplicate item key", "massd: {}\ncore: {sources: [{name: " + secret + ", name: duplicate}]}", "core configuration contains a duplicate key at line 2"},
		{"nonmapping root", "- " + secret, "compose configuration must be a mapping"},
		{"null section", "massd: {}\ncore: null", "core configuration must be a mapping"},
		{"nonmapping section", "massd: " + secret + "\ncore: {}", "massd configuration must be a mapping"},
		{"nonstring key", "massd: {0: " + secret + "}\ncore: {}", "massd configuration requires string keys at line 1"},
		{"trigger section scalar", valid + "triggers: " + secret, "triggers configuration must be a mapping"},
		{"unknown trigger", valid + "triggers: {" + secret + ": {}}", "unknown triggers configuration section at line 3"},
		{"duplicate trigger", valid + "triggers: {dws: {}, DWS: {}}", "triggers configuration contains a duplicate key at line 3"},
		{"trigger component null", valid + "triggers: {github: null}", "triggers.github configuration must be a mapping"},
		{"trigger duplicate nested field", valid + "triggers: {dws: {direct: {prompt: " + secret + ", Prompt: ''}}}", "triggers.dws configuration contains a duplicate key at line 3"},
		{"second document", valid + "---\n" + secret, "one YAML document"},
		{"malformed YAML", "massd: [" + secret + "\n", "cannot read Compose configuration as YAML"},
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
		data := "massd: {}\ncore: {listen: '127.0.0.1:0', unused: " + value + "}\n"
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		command := New()
		command.SetArgs([]string{"compose", "--config", path, "--massd-bin", "unused"})
		err := command.Execute()
		require.ErrorContains(t, err, "core configuration: invalid_argument: configuration contains unknown or invalid fields")
		require.NotContains(t, err.Error(), "PRIVATE-FIXTURE-CONFIG-VALUE")
	}
}

func TestMassdStartupAndReadiness(t *testing.T) {
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

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	daemon := startStubMassd(t, fmt.Sprintf("printf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	actual, err := daemon.waitReady(ctx, massdStartupEndpoint, "/readyz")
	require.NoError(t, err)
	require.Equal(t, endpoint, actual)
	require.GreaterOrEqual(t, requests.Load(), int32(2))
	require.NoError(t, daemon.stop(time.Second))
	require.Equal(t, syscall.SIGTERM, daemon.cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
}

func TestMassdStartupEndpoint(t *testing.T) {
	for _, test := range []struct {
		url  string
		want string
	}{
		{url: "ws://127.0.0.1:24368/jsonrpc", want: "ws://127.0.0.1:24368/jsonrpc"},
		{url: "wss://[::]:24368/jsonrpc", want: "wss://[::1]:24368/jsonrpc"},
		{url: "ws://127.0.0.1:24368/jsonrpc#fragment"},
		{url: "ws://127.0.0.1:24368"},
		{url: "ws://127.0.0.1:24368/massd/jsonrpc"},
		{url: "ws://127.0.0.1:24368/jsonrpc/"},
		{url: "ws://127.0.0.1:24368/jsonrpc#"},
	} {
		t.Run(test.url, func(t *testing.T) {
			record, err := json.Marshal(map[string]string{"url": test.url})
			require.NoError(t, err)
			endpoint, err := massdStartupEndpoint(record)
			if test.want == "" {
				require.ErrorContains(t, err, "invalid massd startup RPC endpoint")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, endpoint.String())
		})
	}
}

func TestMassdStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, failure string
		deadline              time.Duration
		exitCode              int
	}{
		{name: "early exit", script: "exit 42", failure: "massd", deadline: 5 * time.Second, exitCode: 42},
		{name: "invalid startup output", script: "printf 'invalid\\n'; exec /bin/sleep 10", failure: "invalid massd startup record", deadline: 5 * time.Second},
		{name: "startup timeout", script: "exec /bin/sleep 10", failure: "waiting for massd startup: context deadline exceeded", deadline: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon := startStubMassd(t, tc.script)
			ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
			defer cancel()
			_, err := daemon.waitReady(ctx, massdStartupEndpoint, "/readyz")
			require.ErrorContains(t, err, tc.failure)
			if tc.exitCode != 0 {
				select {
				case <-daemon.done:
					require.Equal(t, tc.exitCode, daemon.cmd.ProcessState.ExitCode())
				case <-ctx.Done():
					t.Fatal("massd process was not reaped")
				}
			}
		})
	}
}

func TestMassdReadinessTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	daemon := startStubMassd(t, fmt.Sprintf("printf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 30\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := daemon.waitReady(ctx, massdStartupEndpoint, "/readyz")
	require.ErrorContains(t, err, "waiting for massd readiness: context deadline exceeded")
}

func TestMassdForcedShutdownWaitsForExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/jsonrpc"
	daemon := startStubMassd(t, fmt.Sprintf("trap '' TERM\nprintf '%%s\\n' '{\"url\":%q}'\nexec /bin/sleep 10\n", endpoint))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := daemon.waitReady(ctx, massdStartupEndpoint, "/readyz")
	require.NoError(t, err)
	require.ErrorContains(t, daemon.stop(10*time.Millisecond), "massd exceeded shutdown timeout")
	require.NotNil(t, daemon.cmd.ProcessState)
}

func TestCoreStartupDependencyExit(t *testing.T) {
	daemon := startStubMassd(t, "exit 42")
	server := startStubChild(t, "core", "exec /bin/sleep 30")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := server.waitReady(ctx, func(line []byte) (*url.URL, error) {
		return coreStartupEndpoint(line, "127.0.0.1:0")
	}, "/v1/observer/health", daemon)
	require.ErrorContains(t, err, "massd exited unexpectedly: exit status 42")
}

func TestComposeStartupWildcardEndpoints(t *testing.T) {
	for _, tc := range []struct {
		parse          func([]byte) (*url.URL, error)
		record, expect string
	}{
		{massdStartupEndpoint, `{"url":"ws://0.0.0.0:12111/jsonrpc"}`, "ws://127.0.0.1:12111/jsonrpc"},
		{massdStartupEndpoint, `{"url":"ws://[::]:12111/jsonrpc"}`, "ws://[::1]:12111/jsonrpc"},
	} {
		actual, err := tc.parse([]byte(tc.record))
		require.NoError(t, err)
		require.Equal(t, tc.expect, actual.String())
	}
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

func startStubMassd(t *testing.T, script string) *childProcess {
	t.Helper()
	return startStubChild(t, "massd", script)
}

func startStubChild(t *testing.T, name, script string) *childProcess {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o700))
	cmd := exec.CommandContext(context.Background(), binary)
	cmd.Stderr = io.Discard
	child, err := startChild(name, cmd)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, child.stop(time.Second))
		require.NotNil(t, child.cmd.ProcessState)
	})
	return child
}
