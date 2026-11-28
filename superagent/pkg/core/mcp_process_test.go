package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const mcpTestProtocolVersion = "2025-11-25"

type mcpTestResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int        `json:"code"`
		Message string     `json:"message"`
		Data    core.Error `json:"data"`
	} `json:"error"`
}

type mcpTestToolResult struct {
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

func decodeMCPToolError(t *testing.T, result mcpTestToolResult) core.Error {
	t.Helper()
	require.True(t, result.IsError)
	var failure struct {
		Error core.Error `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result.StructuredContent, &failure))
	return failure.Error
}

func newMCPRequest(ctx context.Context, endpoint, token, method string, id, params any) (*http.Request, error) {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		message["id"] = id
	}
	if params != nil {
		message["params"] = params
	}
	body, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/apis/core/v1/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method != "initialize" {
		req.Header.Set("MCP-Protocol-Version", mcpTestProtocolVersion)
	}
	return req, nil
}

func newMCPToolRequest(ctx context.Context, endpoint, token, tool string, args any) (*http.Request, error) {
	tool, args = mutationFixture(tool, args)
	return newMCPRequest(ctx, endpoint, token, "tools/call", 1, map[string]any{"name": tool, "arguments": args})
}

func sendMCPRequest(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	result := doMCPRequest(req)
	require.NoError(t, result.err)
	return result.status, result.body
}

type mcpHTTPResult struct {
	status int
	body   []byte
	err    error
}

func doMCPRequest(req *http.Request) mcpHTTPResult {
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return mcpHTTPResult{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return mcpHTTPResult{status: response.StatusCode, body: body, err: err}
}

func decodeMCPResponse(t *testing.T, body []byte, id string) mcpTestResponse {
	t.Helper()
	var response mcpTestResponse
	require.NoError(t, json.Unmarshal(body, &response), string(body))
	require.Equal(t, "2.0", response.JSONRPC)
	require.JSONEq(t, id, string(response.ID))
	return response
}

// Map MCP domain errors to the status/code conventions used by the existing
// business assertions. The raw MCP response is verified before normalization.
func requestMCPTool(t *testing.T, endpoint, tool, token string, args any) (int, []byte) {
	t.Helper()
	req, err := newMCPToolRequest(t.Context(), endpoint, token, tool, args)
	require.NoError(t, err)
	status, body := sendMCPRequest(t, req)
	return normalizeMCPToolResult(t, status, body)
}

func normalizeMCPToolResult(t *testing.T, status int, body []byte) (int, []byte) {
	t.Helper()
	if status != http.StatusOK {
		return status, body
	}
	response := decodeMCPResponse(t, body, "1")
	var e core.Error
	if response.Error == nil {
		var toolResult mcpTestToolResult
		require.NoError(t, json.Unmarshal(response.Result, &toolResult))
		require.NotEmpty(t, toolResult.StructuredContent)
		if !toolResult.IsError {
			result, err := json.Marshal(map[string]json.RawMessage{"result": toolResult.StructuredContent})
			require.NoError(t, err)
			return status, result
		}
		e = decodeMCPToolError(t, toolResult)
	} else {
		require.Empty(t, response.Result)
		require.Equal(t, -32000, response.Error.Code)
		e = response.Error.Data
		require.Equal(t, e.Message, response.Error.Message)
	}
	switch e.Code {
	case "invalid_argument":
		status = http.StatusBadRequest
	case "unauthorized":
		status = http.StatusUnauthorized
	case "forbidden":
		status = http.StatusForbidden
	case "not_found":
		status = http.StatusNotFound
	case "conflict", "failed_precondition", "unsupported", "stale_binding":
		status = http.StatusConflict
	case "unavailable":
		status = http.StatusServiceUnavailable
	default:
		t.Fatalf("unknown MCP domain error: %s", body)
	}
	failure, err := json.Marshal(map[string]core.Error{"error": e})
	require.NoError(t, err)
	return status, failure
}

func initializeMCP(t *testing.T, endpoint, token string) json.RawMessage {
	t.Helper()
	req, err := newMCPRequest(t.Context(), endpoint, token, "initialize", "initialize", map[string]any{
		"protocolVersion": mcpTestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "core-test", "version": "1"},
	})
	require.NoError(t, err)
	status, body := sendMCPRequest(t, req)
	require.Equal(t, http.StatusOK, status, string(body))
	response := decodeMCPResponse(t, body, `"initialize"`)
	require.Nil(t, response.Error)
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		Meta            map[string]json.RawMessage `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Equal(t, mcpTestProtocolVersion, result.ProtocolVersion)
	require.Contains(t, result.Capabilities, "tools")
	require.NotContains(t, string(response.Result), token)
	require.Contains(t, result.Meta, "mass.dev/session-binding")
	return result.Meta["mass.dev/session-binding"]
}
