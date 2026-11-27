package cua_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureTokenPrefix = "fixture-only-access."
	relayURL           = "https://relay.invalid:5943"
)

func TestShortCommandStdio(t *testing.T) {
	binDir := buildBridge(t)

	t.Run("single target and upstream results", func(t *testing.T) {
		ctx, f := startBridge(t, binDir, "first-mac")
		tools, err := f.session.ListTools(ctx, nil)
		require.NoError(t, err)
		tool := findSpaceBash(t, tools.Tools)
		assert.Equal(t, []any{"command"}, tool.InputSchema.(map[string]any)["required"])

		for _, args := range []map[string]any{
			{"command": "pwd", "computer_id": "unknown-mac"},
			{"command": "pwd", "computer_id": ""},
			{"command": "pwd", "computer_id": nil},
			{"command": "pwd", "address": "https://another.invalid"},
			{"command": "pwd", "space": "another-space"},
			{"command": "pwd", "timeout": -1},
			{"command": "pwd", "timeout": nil},
			{"command": nil},
			{},
		} {
			result, err := f.call(ctx, args)
			require.NoError(t, err)
			require.True(t, result.IsError, "%v", args)
		}
		assert.Empty(t, f.stateDirs(t))

		command := "printf '%s\\n' 'preserve whitespace'"
		first := f.run(t, ctx, map[string]any{"command": command, "timeout": uint64(1<<64 - 1)})
		assert.False(t, first.IsError)
		require.Len(t, first.Content, 1)
		assert.Equal(t, "stdout\n[stderr]\nstderr\n[exit 7]", first.Content[0].(*mcp.TextContent).Text)
		assert.Equal(t, mcp.Meta{"fixture": true}, first.Meta)
		firstData := first.StructuredContent.(map[string]any)
		assert.Equal(t, command, firstData["cmd"])
		assert.Equal(t, "18446744073709551615", firstData["timeout"])
		assert.Equal(t, relayURL+"/m/first-mac", firstData["url"])
		assert.InDelta(t, 1, firstData["call"], 0)

		repeated := f.run(t, ctx, map[string]any{"computer_id": "first-mac", "command": "pwd"})
		repeatedData := repeated.StructuredContent.(map[string]any)
		assert.Equal(t, firstData["pid"], repeatedData["pid"])
		assert.InDelta(t, 2, repeatedData["call"], 0)
		assert.NotContains(t, repeatedData, "timeout")

		toolFailure := f.run(t, ctx, map[string]any{"command": "tool-error"})
		assert.True(t, toolFailure.IsError)
		assert.Equal(t, "upstream target unavailable", toolFailure.Content[0].(*mcp.TextContent).Text)
		_, err = f.call(ctx, map[string]any{"command": "rpc-error"})
		var wireErr *jsonrpc.Error
		require.ErrorAs(t, err, &wireErr)
		assert.EqualValues(t, -32002, wireErr.Code)
		assert.Equal(t, "upstream failure", wireErr.Message)
		assert.JSONEq(t, `{"retryable": true}`, string(wireErr.Data))
		for _, command := range []string{"credential-in-tool-result", "credential-in-rpc-data"} {
			result := f.run(t, ctx, map[string]any{"command": command})
			assert.True(t, result.IsError)
			assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, "unsafe")
			assert.Nil(t, result.StructuredContent)
		}
		require.Len(t, f.stateDirs(t), 1)
		f.closeAndCheck(t, int(firstData["pid"].(float64)))
	})

	t.Run("multiple targets and failed registration", func(t *testing.T) {
		ctx, f := startBridge(t, binDir, "first-mac", "second-mac", "unavailable-mac", "broken-mcp")
		tools, err := f.session.ListTools(ctx, nil)
		require.NoError(t, err)
		tool := findSpaceBash(t, tools.Tools)
		assert.Equal(t, []any{"command", "computer_id"}, tool.InputSchema.(map[string]any)["required"])

		missing := f.run(t, ctx, map[string]any{"command": "pwd"})
		require.True(t, missing.IsError)
		assert.Contains(t, missing.Content[0].(*mcp.TextContent).Text, "computer_id is required")
		unavailable := f.run(t, ctx, map[string]any{"computer_id": "unavailable-mac", "command": "pwd"})
		require.True(t, unavailable.IsError)
		assert.Contains(t, unavailable.Content[0].(*mcp.TextContent).Text, "cannot register")
		broken := f.run(t, ctx, map[string]any{"computer_id": "broken-mcp", "command": "pwd"})
		require.True(t, broken.IsError)
		assert.Contains(t, broken.Content[0].(*mcp.TextContent).Text, "cannot initialize")
		assert.Empty(t, f.stateDirs(t))

		// Two simultaneous first calls for the same computer initialize one
		// child. A third call uses the other computer's own child and registry.
		type callOutcome struct {
			result *mcp.CallToolResult
			err    error
		}
		replies := make(chan callOutcome, 2)
		for range 2 {
			go func() {
				result, err := f.call(ctx, map[string]any{"computer_id": "first-mac", "command": "pwd"})
				replies <- callOutcome{result, err}
			}()
		}
		second := f.run(t, ctx, map[string]any{"computer_id": "second-mac", "command": "pwd"})
		left, right := <-replies, <-replies
		require.NoError(t, left.err)
		require.NoError(t, right.err)
		require.False(t, left.result.IsError)
		require.False(t, right.result.IsError)
		require.False(t, second.IsError)
		firstData := left.result.StructuredContent.(map[string]any)
		repeatedData := right.result.StructuredContent.(map[string]any)
		secondData := second.StructuredContent.(map[string]any)
		assert.Equal(t, relayURL+"/m/first-mac", firstData["url"])
		assert.Equal(t, relayURL+"/m/first-mac", repeatedData["url"])
		assert.Equal(t, relayURL+"/m/second-mac", secondData["url"])
		assert.Equal(t, firstData["pid"], repeatedData["pid"])
		assert.NotEqual(t, firstData["pid"], secondData["pid"])
		assert.ElementsMatch(t, []any{float64(1), float64(2)}, []any{firstData["call"], repeatedData["call"]})
		require.Len(t, f.stateDirs(t), 2)
		f.closeAndCheck(t, int(firstData["pid"].(float64)), int(secondData["pid"].(float64)))
	})
}

type bridgeFixture struct {
	session *mcp.ClientSession
	tempDir string
	stderr  bytes.Buffer
}

func startBridge(t *testing.T, binDir string, ids ...string) (context.Context, *bridgeFixture) {
	t.Helper()
	return startBridgeWithDrivers(t, binDir, nil, ids...)
}

func buildBridge(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binDir+string(os.PathSeparator),
		"../../cmd/cua-mcp", "./testdata/cua")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return binDir
}

func startBridgeWithDrivers(t *testing.T, binDir string, drivers []string, ids ...string) (context.Context, *bridgeFixture) {
	t.Helper()
	dir := t.TempDir()
	f := &bridgeFixture{tempDir: filepath.Join(dir, "state")}
	require.NoError(t, os.Mkdir(f.tempDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test-ca.pem"), nil, 0o600))
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "cuaCLI: %q\nrelayURL: %q\ncaFile: test-ca.pem\ncomputers:\n", filepath.Join(binDir, "cua"), relayURL)
	for _, id := range ids {
		tokenFile := id + ".token"
		require.NoError(t, os.WriteFile(filepath.Join(dir, tokenFile), []byte(fixtureTokenPrefix+id+"\n"), 0o600))
		fmt.Fprintf(&cfg, "  - id: %q\n    tokenFile: %q\n", id, tokenFile)
		if slices.Contains(drivers, id) {
			fmt.Fprint(&cfg, "    driver:\n      cli: /target/Cua Driver/bin/cua-driver\n      socket: /target/Library/Caches/driver.sock\n")
		}
	}
	configPath := filepath.Join(dir, "cua-mcp.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(cfg.String()), 0o600))

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "cua-mcp"), "--config", configPath)
	cmd.Stderr = &f.stderr
	cmd.Env = append(os.Environ(), "TMPDIR="+f.tempDir, "CUA_DAEMON=ambient-socket", "CUA_ENV_TOKEN=ambient-token")
	client := mcp.NewClient(&mcp.Implementation{Name: "cua-mcp-test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.NoError(t, err)
	f.session = session
	t.Cleanup(func() { _ = f.session.Close() })
	return ctx, f
}

func (f *bridgeFixture) stateDirs(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(f.tempDir)
	require.NoError(t, err)
	return entries
}

func (f *bridgeFixture) closeAndCheck(t *testing.T, pids ...int) {
	t.Helper()
	require.NoError(t, f.session.Close())
	assert.Empty(t, f.stateDirs(t))
	assert.NotContains(t, f.stderr.String(), fixtureTokenPrefix)
	for _, pid := range pids {
		assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
	}
}

func (f *bridgeFixture) call(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	return f.session.CallTool(ctx, &mcp.CallToolParams{Name: "space_bash", Arguments: args})
}

func (f *bridgeFixture) run(t *testing.T, ctx context.Context, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := f.call(ctx, args)
	require.NoError(t, err)
	return result
}

func findSpaceBash(t *testing.T, tools []*mcp.Tool) *mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "space_bash" {
			return tool
		}
	}
	require.FailNow(t, "space_bash was not advertised")
	return nil
}
