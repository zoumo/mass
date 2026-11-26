package cua_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesktopStdio(t *testing.T) {
	binDir := buildBridge(t)

	t.Run("snapshot action and readback on a single target", func(t *testing.T) {
		ctx, f := startBridgeWithDrivers(t, binDir, []string{"first-mac"}, "first-mac")
		tools := desktopToolList(t, ctx, f)
		assert.Equal(t, []any{"pid", "window_id"}, tools["get_window_state"]["required"])
		assert.Equal(t, []any{"text"}, tools["type_text"]["required"])
		assert.Equal(t, []any{"key"}, tools["press_key"]["required"])
		assert.Empty(t, f.stateDirs(t))

		windows := callDesktop(t, ctx, f, "list_windows", nil)
		childPID := int(windows["child_pid"].(float64))
		assert.Equal(t, relayURL+"/m/first-mac", windows["url"])
		window := map[string]any{"pid": windows["pid"], "window_id": windows["window_id"], "tree_format": "elements"}
		state, err := f.session.CallTool(ctx, &mcp.CallToolParams{Name: "get_window_state", Arguments: window})
		require.NoError(t, err)
		require.False(t, state.IsError)
		require.Len(t, state.Content, 2)
		assert.Equal(t, "get_window_state", state.Content[0].(*mcp.TextContent).Text)
		require.IsType(t, (*mcp.ImageContent)(nil), state.Content[1])
		image := state.Content[1].(*mcp.ImageContent)
		assert.Equal(t, "image/png", image.MIMEType)
		assert.Equal(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC", base64.StdEncoding.EncodeToString(image.Data))
		assert.Equal(t, mcp.Meta{"fixture": "driver"}, state.Meta)
		first := state.StructuredContent.(map[string]any)
		assert.Equal(t, windows["child_pid"], first["child_pid"])
		assert.Equal(t, window, first["arguments"])
		element := first["elements"].([]any)[0].(map[string]any)
		require.Equal(t, false, element["checked"])

		for _, action := range []struct {
			name string
			args map[string]any
		}{
			{"click", map[string]any{"action": "press", "ui_tree_revision": uint64(1<<64 - 1)}},
			{"type_text", map[string]any{"text": "literal text\n第二行"}},
			{"press_key", map[string]any{"key": "return", "modifiers": []any{"shift"}}},
		} {
			action.args["element_token"] = element["element_token"]
			action.args["computer_id"] = "first-mac"
			result := callDesktop(t, ctx, f, action.name, action.args)
			assert.Equal(t, "confirmed", result["effect"])
			assert.Equal(t, windows["child_pid"], result["child_pid"])
			forwarded := result["arguments"].(map[string]any)
			assert.NotContains(t, forwarded, "computer_id")
			assert.Equal(t, element["element_token"], forwarded["element_token"])
			switch action.name {
			case "click":
				assert.Equal(t, "press", forwarded["action"])
				assert.Equal(t, "18446744073709551615", result["revision_json"])
			case "type_text":
				assert.Equal(t, action.args["text"], forwarded["text"])
			case "press_key":
				assert.Equal(t, action.args["key"], forwarded["key"])
				assert.Equal(t, action.args["modifiers"], forwarded["modifiers"])
			}
		}

		window["since"] = first["snapshot_id"]
		updated := callDesktop(t, ctx, f, "get_window_state", window)
		assert.Equal(t, windows["child_pid"], updated["child_pid"])
		assert.Equal(t, window, updated["arguments"])
		assert.NotEqual(t, first["snapshot_id"], updated["snapshot_id"])
		assert.Equal(t, true, updated["elements"].([]any)[0].(map[string]any)["checked"])
		require.Len(t, f.stateDirs(t), 1)
		f.closeAndCheck(t, childPID)
	})

	t.Run("target isolation and shell availability", func(t *testing.T) {
		ctx, f := startBridgeWithDrivers(t, binDir, []string{"first-mac", "second-mac", "broken-driver"},
			"first-mac", "second-mac", "broken-driver", "shell-only")
		tools := desktopToolList(t, ctx, f)
		for _, input := range tools {
			assert.Contains(t, input["required"], "computer_id")
			computerID := input["properties"].(map[string]any)["computer_id"].(map[string]any)
			assert.Equal(t, []any{"broken-driver", "first-mac", "second-mac"}, computerID["enum"])
		}

		for _, tc := range []struct {
			args map[string]any
			want string
		}{
			{nil, "arguments to Cua Driver must be an object"},
			{map[string]any{}, "computer_id is required"},
			{map[string]any{"computer_id": nil}, "computer_id must be a string"},
			{map[string]any{"computer_id": "unknown-mac"}, "computer_id is not configured"},
			{map[string]any{"computer_id": "first-mac", "address": "https://another.invalid"}, "unknown argument"},
			{map[string]any{"computer_id": "first-mac", "socket": "/tmp/override.sock"}, "unknown argument"},
			{map[string]any{"computer_id": "shell-only"}, "no Cua Driver is configured"},
			{map[string]any{"computer_id": "broken-driver"}, "cannot initialize"},
		} {
			result, err := f.session.CallTool(ctx, &mcp.CallToolParams{Name: "list_windows", Arguments: tc.args})
			require.NoError(t, err)
			require.True(t, result.IsError)
			assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, tc.want)
		}
		assert.Empty(t, f.stateDirs(t))

		first := callDesktop(t, ctx, f, "get_window_state", map[string]any{"computer_id": "first-mac", "pid": 501, "window_id": 724})
		second := callDesktop(t, ctx, f, "get_window_state", map[string]any{"computer_id": "second-mac", "pid": 501, "window_id": 724})
		assert.Equal(t, relayURL+"/m/first-mac", first["url"])
		assert.Equal(t, relayURL+"/m/second-mac", second["url"])
		assert.NotEqual(t, first["child_pid"], second["child_pid"])
		token := first["elements"].([]any)[0].(map[string]any)["element_token"]
		wrongTarget, err := f.session.CallTool(ctx, &mcp.CallToolParams{Name: "click", Arguments: map[string]any{
			"computer_id": "second-mac", "element_token": token,
		}})
		require.NoError(t, err)
		require.True(t, wrongTarget.IsError)
		assert.Equal(t, "stale_element_token", wrongTarget.StructuredContent.(map[string]any)["code"])
		assert.Equal(t, "Element token belongs to another snapshot or session", wrongTarget.Content[0].(*mcp.TextContent).Text)
		click := callDesktop(t, ctx, f, "click", map[string]any{"computer_id": "first-mac", "element_token": token})
		assert.Equal(t, first["child_pid"], click["child_pid"])

		_, err = f.session.CallTool(ctx, &mcp.CallToolParams{Name: "list_windows", Arguments: map[string]any{
			"computer_id": "first-mac", "app": "rpc-error",
		}})
		var rpcErr *jsonrpc.Error
		require.ErrorAs(t, err, &rpcErr)
		assert.EqualValues(t, -32010, rpcErr.Code)
		assert.Equal(t, "Driver RPC failed", rpcErr.Message)
		assert.JSONEq(t, `{"retryable": true}`, string(rpcErr.Data))
		for _, app := range []string{"tool-error", "credential-in-tool-result"} {
			result, err := f.session.CallTool(ctx, &mcp.CallToolParams{Name: "list_windows", Arguments: map[string]any{
				"computer_id": "first-mac", "app": app,
			}})
			require.NoError(t, err)
			require.True(t, result.IsError)
			if app == "tool-error" {
				assert.Equal(t, "desktop_unavailable", result.StructuredContent.(map[string]any)["code"])
				assert.Equal(t, "Driver cannot list windows", result.Content[0].(*mcp.TextContent).Text)
			} else {
				assert.Nil(t, result.StructuredContent)
				assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, "unsafe")
			}
		}

		pids := []int{int(first["child_pid"].(float64)), int(second["child_pid"].(float64))}
		for _, id := range []string{"first-mac", "broken-driver", "shell-only"} {
			shell := f.run(t, ctx, map[string]any{"computer_id": id, "command": "pwd"})
			require.False(t, shell.IsError)
			data := shell.StructuredContent.(map[string]any)
			assert.Equal(t, relayURL+"/m/"+id, data["url"])
			pids = append(pids, int(data["pid"].(float64)))
		}
		require.Len(t, f.stateDirs(t), 5)
		f.closeAndCheck(t, pids...)
	})
}

func desktopToolList(t *testing.T, ctx context.Context, f *bridgeFixture) map[string]map[string]any {
	t.Helper()
	list, err := f.session.ListTools(ctx, nil)
	require.NoError(t, err)
	inputs := make(map[string]map[string]any)
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Name != "space_bash" {
			inputs[tool.Name] = tool.InputSchema.(map[string]any)
		}
	}
	require.ElementsMatch(t, []string{"space_bash", "list_windows", "get_window_state", "click", "type_text", "press_key"}, names)
	return inputs
}

func callDesktop(t *testing.T, ctx context.Context, f *bridgeFixture, name string, args map[string]any) map[string]any {
	t.Helper()
	params := &mcp.CallToolParams{Name: name}
	if args != nil {
		params.Arguments = args
	}
	result, err := f.session.CallTool(ctx, params)
	require.NoError(t, err)
	require.False(t, result.IsError, "%+v", result.GetError())
	return result.StructuredContent.(map[string]any)
}
