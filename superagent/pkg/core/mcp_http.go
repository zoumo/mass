package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel/trace"
)

const mcpMaxRequestBytes = 1 << 20

// The Core MCP endpoint uses stateless Streamable HTTP with JSON responses.
// Each request authenticates independently using the capability bearer token.
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var parent trace.SpanContext
	var binding Binding
	var observed *telemetryResponse
	observe := func() {
		if s.store.telemetry.enabled {
			observed, r = s.store.telemetry.startHTTPWithParent(w, r, start, parent)
			w = observed
		}
	}
	defer func() {
		if observed != nil {
			observed.finishRequest()
		}
	}()

	if r.Method != http.MethodPost {
		observe()
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	version := r.Header.Get("MCP-Protocol-Version")
	if version != "" && !supportedMCPVersion(version) {
		observe()
		s.fail(w, problem("invalid_argument", "unsupported MCP-Protocol-Version"))
		return
	}
	binding, err := s.store.authenticateRole(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		observe()
		s.fail(w, err)
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		observe()
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxRequestBytes))
	if err != nil {
		observe()
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			s.fail(w, problem("invalid_argument", "cannot read MCP request"))
		}
		return
	}
	var request mcpHTTPRequest
	if !json.Valid(body) {
		observe()
		s.write(w, http.StatusBadRequest, mcpHTTPError(nil, rpc2.CodeParseError, "Parse error"))
		return
	}
	if err := decodeStrict(body, &request); err != nil || !request.valid() {
		observe()
		s.write(w, http.StatusBadRequest, mcpHTTPError(nil, rpc2.CodeInvalidRequest, "Invalid Request"))
		return
	}
	if request.ID == nil {
		observe()
		if request.Method != "notifications/initialized" && request.Method != "notifications/cancelled" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	if request.Method == "tools/call" {
		call, protocolErr := prepareMCPTool(request.Params, binding)
		usage := newMCPUsage(binding)
		var toolErr error
		if protocolErr == nil && traceRoleTool(call.name) {
			call.arguments, parent, toolErr = s.store.roleTraceInput(context.WithValue(r.Context(), mcpUsageContextKey{}, usage), binding, call.arguments, call.name)
		}
		observe()
		err = protocolErr
		if err == nil {
			var value any
			if toolErr == nil {
				value, toolErr = s.callRoleTool(context.WithValue(r.Context(), mcpUsageContextKey{}, usage), binding, call.name, call.arguments)
			}
			result, err = makeMCPToolResult(value, toolErr)
		}
		s.store.recordMCPTool(r.Context(), start, usage, call.metricName, errors.Join(toolErr, err))
	} else {
		observe()
		result, err = s.mcpRequest(request, binding)
	}
	response := mcpHTTPResponse{JSONRPC: "2.0", ID: request.ID}
	if err != nil {
		response.Error = mcpRPCError(err)
	} else {
		response.Result = result
	}
	s.write(w, http.StatusOK, response)
}

// Retain the wire ID so signed numbers and their original precision round-trip.
type mcpHTTPRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage  `json:"id,omitempty"`
}

func (r mcpHTTPRequest) valid() bool {
	if r.JSONRPC != "2.0" || r.Method == "" {
		return false
	}
	if r.ID == nil {
		return true
	}
	first := r.ID[0]
	if first == '"' {
		return true
	}
	return (first == '-' || (first >= '0' && first <= '9')) && !bytes.ContainsAny(r.ID, ".eE")
}

type mcpHTTPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpc2.Error     `json:"error,omitempty"`
}

func mcpHTTPError(id json.RawMessage, code int64, message string) mcpHTTPResponse {
	return mcpHTTPResponse{JSONRPC: "2.0", ID: id, Error: &rpc2.Error{Code: code, Message: message}}
}

func (s *Server) mcpRequest(request mcpHTTPRequest, b Binding) (any, error) {
	switch request.Method {
	case "initialize":
		if b.State == "revoked" && b.Role != "issue" {
			return nil, problem("stale_binding", "binding was revoked")
		}
		result, err := mcpInitialize(request.Params)
		if err != nil {
			return nil, err
		}
		identity := map[string]any{"id": b.ID, "role": b.Role, "ownerId": b.OwnerID, "generation": b.Generation}
		if b.TopicID != "" {
			identity["topicId"] = b.TopicID
		}
		if b.IssueID != "" {
			identity["issueId"] = b.IssueID
		}
		result.Meta = map[string]any{mcpBindingMeta: identity}
		return result, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		var p struct {
			Cursor string          `json:"cursor,omitempty"`
			Meta   json.RawMessage `json:"_meta,omitempty"`
		}
		if request.Params != nil && (decodeStrict(*request.Params, &p) != nil || p.Cursor != "") {
			return nil, mcpInvalidParams()
		}
		tools := mcpToolsByRole[b.Role]
		if b.State == "revoked" {
			if b.Role != "issue" {
				return nil, problem("stale_binding", "binding was revoked")
			}
			tools = slices.DeleteFunc(slices.Clone(tools), func(tool mcpTool) bool {
				return tool.Name != "submit_report"
			})
		}
		return map[string]any{"tools": tools}, nil
	default:
		return nil, &rpc2.Error{Code: rpc2.CodeMethodNotFound, Message: "Method not found"}
	}
}

var mcpToolsByRole = map[string][]mcpTool{
	"router": roleTools("router"),
	"topic":  roleTools("topic"),
	"issue": roleTools("issue"),
}

var knownMCPTools = func() map[string]bool {
	names := map[string]bool{}
	for _, tools := range mcpToolsByRole {
		for _, tool := range tools {
			names[tool.Name] = true
		}
	}
	return names
}()

type mcpToolCall struct {
	name       string
	arguments  json.RawMessage
	metricName string
}

func prepareMCPTool(params *json.RawMessage, b Binding) (mcpToolCall, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	call := mcpToolCall{metricName: "_unknown"}
	if params == nil || decodeStrict(*params, &p) != nil || p.Name == "" {
		return call, mcpInvalidParams()
	}
	call.name = p.Name
	if knownMCPTools[p.Name] {
		call.metricName = p.Name
	}
	if !slices.ContainsFunc(mcpToolsByRole[b.Role], func(tool mcpTool) bool { return tool.Name == p.Name }) {
		return call, problem("forbidden", "tool is outside current role capability")
	}
	if b.State == "revoked" && (b.Role != "issue" || p.Name != "submit_report") {
		return call, problem("stale_binding", "binding was revoked")
	}
	call.arguments = bytes.TrimSpace(p.Arguments)
	if len(call.arguments) == 0 {
		call.arguments = json.RawMessage(`{}`)
	} else if call.arguments[0] != '{' {
		return call, mcpInvalidParams()
	}
	return call, nil
}

func makeMCPToolResult(value any, toolErr error) (any, error) {
	if toolErr != nil {
		var e *Error
		if !errors.As(toolErr, &e) {
			return nil, toolErr
		}
		value = map[string]*Error{"error": e}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(raw)}},
		"structuredContent": json.RawMessage(raw),
		"isError":           toolErr != nil,
	}, nil
}
