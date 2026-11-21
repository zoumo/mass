package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": append([]string{}, required...), "additionalProperties": false}
}
func stringSchema() map[string]any { return map[string]any{"type": "string", "minLength": 1} }

const (
	mcpPath            = "/mcp"
	mcpProtocolVersion = "2025-11-25"
	mcpBindingMeta     = "mass.dev/binding"
)

type mcpHandler struct {
	capability  capability
	http        *http.Client
	initialized atomic.Bool
}
type readWriteCloser struct {
	io.Reader
	io.Writer
}

func (readWriteCloser) Close() error { return nil }

// RunMCPStdio captures one private capability at startup; request text cannot select a role or owner.
func RunMCPStdio(ctx context.Context, input io.Reader, output io.Writer, path string) error {
	data, err := readPrivate(path)
	if err != nil {
		return err
	}
	var startup capability
	if err := decodeStrict(data, &startup); err != nil {
		return err
	}
	u, err := url.Parse(startup.Endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil || startup.Token == "" || startup.BindingID == "" || startup.Generation < 1 || (startup.Role != "router" && startup.Role != "topic" && startup.Role != "issue") {
		return problem("invalid_argument", "invalid startup capability")
	}
	h := &mcpHandler{capability: startup, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	startupRequest := rpc2.Request{Method: "initialize"}
	if err := startupRequest.SetParams(map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "superagent-stdio", "version": "v1"},
	}); err != nil {
		return err
	}
	raw, err := h.forward(ctx, &startupRequest)
	if err != nil {
		return err
	}
	var initialized struct {
		Meta map[string]Binding `json:"_meta"`
	}
	if json.Unmarshal(raw, &initialized) != nil || !capabilityMatches(startup, initialized.Meta[mcpBindingMeta]) {
		return problem("stale_binding", "startup capability does not match authenticated binding")
	}
	conn := rpc2.NewConn(ctx, rpc2.NewPlainObjectStream(readWriteCloser{Reader: input, Writer: output}), rpc2.AsyncHandler(h))
	defer func() { _ = conn.Close() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-conn.DisconnectNotify():
		return nil
	}
}

func (h *mcpHandler) Handle(ctx context.Context, conn *rpc2.Conn, r *rpc2.Request) {
	if r.Notif {
		return
	}
	var result any
	var err error
	switch {
	case r.Method == "initialize":
		result, err = mcpInitialize(r.Params)
		if err == nil {
			h.initialized.Store(true)
		}
	case r.Method == "ping":
		result = struct{}{}
	case !h.initialized.Load():
		err = problem("failed_precondition", "initialize is required")
	default:
		result, err = h.forward(ctx, r)
	}
	if err != nil {
		_ = conn.ReplyWithError(ctx, r.ID, mcpRPCError(err))
		return
	}
	_ = conn.Reply(ctx, r.ID, result)
}

type mcpInitializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]map[string]bool `json:"capabilities"`
	ServerInfo      map[string]string          `json:"serverInfo"`
	Meta            map[string]any             `json:"_meta,omitempty"`
}

func supportedMCPVersion(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", mcpProtocolVersion:
		return true
	default:
		return false
	}
}

func mcpInitialize(params *json.RawMessage) (mcpInitializeResult, error) {
	var p struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      json.RawMessage `json:"clientInfo"`
		Meta            json.RawMessage `json:"_meta,omitempty"`
	}
	if params == nil || decodeStrict(*params, &p) != nil || p.ProtocolVersion == "" {
		return mcpInitializeResult{}, mcpInvalidParams()
	}
	version := p.ProtocolVersion
	if !supportedMCPVersion(version) {
		version = mcpProtocolVersion
	}
	return mcpInitializeResult{
		ProtocolVersion: version,
		Capabilities:    map[string]map[string]bool{"tools": {"listChanged": false}},
		ServerInfo:      map[string]string{"name": "superagent", "version": "v1"},
	}, nil
}

func mcpInvalidParams() *rpc2.Error {
	return &rpc2.Error{Code: rpc2.CodeInvalidParams, Message: "Invalid params"}
}

func mcpRPCError(err error) *rpc2.Error {
	var rpcErr *rpc2.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	var e *Error
	if !errors.As(err, &e) {
		e = problem("unavailable", "MCP operation failed")
	}
	rpcErr = &rpc2.Error{Code: -32000, Message: e.Message}
	rpcErr.SetError(e)
	return rpcErr
}

func (h *mcpHandler) forward(ctx context.Context, request *rpc2.Request) (json.RawMessage, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, mcpInvalidParams()
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, h.capability.Endpoint+mcpPath, bytes.NewReader(payload))
	if err != nil {
		return nil, problem("invalid_argument", "invalid Core endpoint")
	}
	r.Header.Set("Authorization", "Bearer "+h.capability.Token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if request.Method != "initialize" {
		r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	}
	response, err := h.http.Do(r)
	if err != nil {
		return nil, problem("unavailable", "Core MCP is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error *Error `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&failure) == nil && failure.Error != nil {
			return nil, failure.Error
		}
		return nil, problem("unavailable", "Core MCP request failed")
	}
	var envelope rpc2.Response
	if json.NewDecoder(response.Body).Decode(&envelope) != nil || envelope.ID != request.ID {
		return nil, problem("unavailable", "invalid Core MCP response")
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}
	if envelope.Result == nil {
		return nil, problem("unavailable", "missing Core MCP result")
	}
	return *envelope.Result, nil
}
