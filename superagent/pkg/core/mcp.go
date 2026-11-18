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
	"sync"
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
func roleTools(role string) []mcpTool {
	str := stringSchema()
	ids := map[string]any{"type": "array", "items": str, "minItems": 1, "uniqueItems": true}
	query := objectSchema(map[string]any{"collection": map[string]any{"type": "string", "enum": []string{"messages", "threads", "outbox", "routing-deliveries"}}, "id": str, "threadKey": str, "messageId": str, "cursor": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, "collection")
	tools := []mcpTool{
		{Name: "delivery_get", Description: "Read the full content and references of an internal delivery directed to your role using its delivery_id.", InputSchema: objectSchema(map[string]any{"delivery_id": str}, "delivery_id")},
	}
	if role == "worker" {
		return append(tools, workerTools()...)
	}
	tools = append(tools,
		mcpTool{Name: "binding_get", Description: "Read your startup-bound role, owner, generation, workspace and preparation state. No credentials or Session transcript.", InputSchema: objectSchema(nil)},
		mcpTool{Name: "source_query", Description: "Read source Messages, Threads, routing-deliveries and your own Outbox replies. Filter routing deliveries by messageId. Router sees source history. Topic sees only associated Messages and subscribed Thread metadata. Filter Outbox by messageId or threadKey. A Topic Message can include quotedReply with the matched Outbox and blockerRef. Lists use cursor/limit; id selects one detail.", InputSchema: query},
		mcpTool{Name: "topic_query", Description: "Router reads Topic summaries only. Topic reads only its own Topic; an out-of-scope id is denied. No Issue, Report or Worker Session expansion.", InputSchema: objectSchema(map[string]any{"collection": map[string]any{"type": "string", "enum": []string{"topics"}}, "id": str, "cursor": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, "collection")},
	)
	replyProps := map[string]any{"operationId": str, "text": str, "messageId": str}
	replyDescription := "Queue a reply to an associated Message. With no messageId, Core uses your oldest associated Message. Reuse operationId for a retry. The receipt confirms the reply was stored."
	if role == "topic" {
		replyProps["blockerRef"] = objectSchema(map[string]any{"issueId": str, "blockerId": str}, "issueId", "blockerId")
		replyDescription += " For a question that blocks work, attach blockerRef for one open human blocker in your Topic."
	}
	if role == "router" {
		replyProps["messageIds"] = ids
		replyDescription = "Queue a clarification or reply to an explicit messageId or the oldest Message among your current routing messageIds. Reuse operationId for a retry. The receipt confirms the reply was stored."
	}
	tools = append(tools, mcpTool{Name: "message_reply", Description: replyDescription, InputSchema: objectSchema(replyProps, "operationId", "text")})
	switch role {
	case "router":
		target := objectSchema(map[string]any{"topicId": str, "create": objectSchema(map[string]any{"title": str, "description": map[string]any{"type": "string"}, "goal": str, "workspace": str}, "title", "workspace"), "messageIds": ids}, "messageIds")
		tools = append(tools, mcpTool{Name: "route_decide", Description: "Atomically route an explicit Message range at its current routing rounds, or ignore with a reason. Each target has topicId XOR create and explicit messageIds. New Topics automatically prepare Sessions and receive source input. Reuse operationId only with identical parameters. You can explicitly select a previously returning Topic again. Native subagents provide suggestions; Router submits the final scoped decision.", InputSchema: objectSchema(map[string]any{"operationId": str, "messageIds": ids, "rounds": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}}, "kind": map[string]any{"type": "string", "enum": []string{"route", "ignore"}}, "reason": str, "targets": map[string]any{"type": "array", "items": target}}, "operationId", "messageIds", "rounds", "kind", "reason")})
	case "topic":
		tools = append(tools, mcpTool{Name: "message_return", Description: "Return a routed_message addressed to your Topic using its delivery_id as deliveryId. Give a reason. A retry returns the same request. The Thread stays subscribed.", InputSchema: objectSchema(map[string]any{"deliveryId": str, "reason": str}, "deliveryId", "reason")})
		tools = append(tools, topicWorkTools()...)
		tools = append(tools, topicTimerTools()...)
		tools = append(tools, registerPRTool(true))
	}
	return tools
}

func topicWorkTools() []mcpTool {
	str := stringSchema()
	revision := map[string]any{"type": "integer", "minimum": 1}
	evidence := map[string]any{"type": "array", "items": str, "minItems": 1}
	material := objectSchema(map[string]any{"label": str, "uri": str, "messageId": str})
	resolve := map[string]any{"type": "array", "items": objectSchema(map[string]any{"blockerId": str, "reason": str, "messageId": str}, "blockerId", "reason")}
	blocker := objectSchema(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": []string{"human", "dependency"}},
		"reason": str, "dependencyIssueId": str,
	}, "kind", "reason")
	return []mcpTool{
		{
			Name: "issue_create", Description: "Create an Issue in your Topic with a goal that states the deliverable, constraints and acceptance criteria. Use an existing absolute workspace. Each material names one URI/path or one associated Message. Defaults to pending; start=true also schedules a Worker atomically.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "title": str, "goal": str, "requirements": str, "workspace": str, "materials": map[string]any{"type": "array", "items": material}, "start": map[string]any{"type": "boolean"}}, "operationId", "title", "workspace"),
		},
		{
			Name: "issue_start", Description: "Explicitly schedule the only current Execution for your Issue. Resolve each blocker with its ID and basis before starting, or provide all resolutions in the same call. Read execution preparation and startup evidence with work_query.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "issueId": str, "expectedRevision": revision, "resolve": resolve}, "operationId", "issueId", "expectedRevision"),
		},
		{
			Name: "issue_update_requirements", Description: "Save your Issue goal using its current Issue revision. Set notifyWorker=true to continue its current active Execution in the same transaction.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "issueId": str, "expectedRevision": revision, "goal": str, "requirements": str, "notifyWorker": map[string]any{"type": "boolean"}}, "operationId", "issueId", "expectedRevision"),
		},
		{
			Name: "issue_update_blockers", Description: "Add human blockers or same-Topic Issue dependencies. Resolve each blocker by ID with a reason and optional scoped Message. A Message with a matched quotedReply can serve as the basis only for its referenced blocker. On the final resolution, choose pending or active. Use notifyWorker=true to continue the current active Execution in the same operation.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "issueId": str, "expectedRevision": revision, "add": map[string]any{"type": "array", "items": blocker}, "resolve": resolve, "notifyWorker": map[string]any{"type": "boolean"}, "unblockedStatus": map[string]any{"type": "string", "enum": []string{"pending", "active"}}}, "operationId", "issueId", "expectedRevision"),
		},
		{
			Name: "issue_worker_update", Description: "Send context_supplement to add background or materials without opening a turn. Use continue to advance a current active Execution after all blockers are resolved. Neither mode changes the goal.",
			InputSchema: objectSchema(map[string]any{
				"operationId": str, "issueId": str, "expectedRevision": revision,
				"mode": map[string]any{"type": "string", "enum": []string{"continue", contextSupplement}}, "note": str,
				"materials": map[string]any{"type": "array", "items": material},
			}, "operationId", "issueId", "expectedRevision", "mode"),
		},
		{
			Name: "issue_complete", Description: "Explicitly accept your Issue after verifying the result. Supply reason and evidence references. Retires current execution authority and requests any remaining native work to stop.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "issueId": str, "expectedRevision": revision, "reason": str, "evidence": evidence}, "operationId", "issueId", "expectedRevision", "reason", "evidence"),
		},
		{
			Name: "topic_complete", Description: "Explicitly accept your bound Topic with a reason and evidence after every Issue is terminal.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "expectedRevision": revision, "reason": str, "evidence": evidence}, "operationId", "expectedRevision", "reason", "evidence"),
		},
		{Name: "issue_cancel", Description: "Cancel your Issue and revoke Worker authority. Read separate native stop and Session release evidence from work_query.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "issueId": str, "expectedRevision": revision, "reason": str,
		}, "operationId", "issueId", "expectedRevision", "reason")},
		{Name: "issue_reopen", Description: "Reopen a terminal Issue to pending. Its prior outcomes and blockers remain available. To execute, use a separate explicit issue_start after prior Session reconciliation.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "issueId": str, "expectedRevision": revision, "reason": str,
		}, "operationId", "issueId", "expectedRevision", "reason")},
		{Name: "topic_cancel", Description: "Cancel this Topic and all its unfinished Issues. Requests native stops and retires this Topic capability. Completed results remain available.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "expectedRevision": revision, "reason": str,
		}, "operationId", "expectedRevision", "reason")},
		{Name: "topic_reopen", Description: "Explicitly reopen only this Topic. Its Issues and existing outcomes remain as they are.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "expectedRevision": revision, "reason": str,
		}, "operationId", "expectedRevision", "reason")},
		{
			Name: "work_query", Description: "Read your Topic's Issues, Executions, Reports, Timers or PR associations. Use id for detail or issueId to filter Executions, Reports or PR associations. Timers include current revision and followup delivery evidence.",
			InputSchema: objectSchema(map[string]any{"collection": map[string]any{"type": "string", "enum": []string{"issues", "executions", "reports", "timers", "pull-requests"}}, "id": str, "issueId": str, "cursor": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, "collection"),
		},
	}
}

func topicTimerTools() []mcpTool {
	str := stringSchema()
	dueAt := map[string]any{"type": "string", "format": "date-time"}
	revision := map[string]any{"type": "integer", "minimum": 1}
	materials := map[string]any{"type": "array", "items": objectSchema(map[string]any{"label": str, "uri": str, "messageId": str})}
	return []mcpTool{
		{Name: "timer_set", Description: "Create a one-time followup for your active Topic. Use a stable operationId and an RFC3339 dueAt with a time zone. A past dueAt is processed on the next scan. When a routed Message has meta.type=timer.followup, read the task and current Topic state before deciding what to do.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "dueAt": dueAt, "content": str, "materials": materials,
		}, "operationId", "dueAt", "content")},
		{Name: "timer_update", Description: "Replace the dueAt, content and materials of your scheduled Timer using its current revision. Omit materials to clear them. A fired Timer needs a new timer_set for another followup.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "timerId": str, "expectedRevision": revision, "dueAt": dueAt, "content": str, "materials": materials,
		}, "operationId", "timerId", "expectedRevision", "dueAt", "content")},
		{Name: "timer_cancel", Description: "Cancel your scheduled Timer using its current revision. Use work_query to check whether a Timer has already fired.", InputSchema: objectSchema(map[string]any{
			"operationId": str, "timerId": str, "expectedRevision": revision,
		}, "operationId", "timerId", "expectedRevision")},
	}
}

func workerTools() []mcpTool {
	str := stringSchema()
	refs := map[string]any{"type": "array", "items": str}
	return []mcpTool{
		registerPRTool(false),
		{Name: "issue_get_current", Description: "Read the startup-bound Issue goal and goalRevision, blockers, material references, workspace and current Execution. Takes no identifiers.", InputSchema: objectSchema(nil)},
		{Name: "issue_material_get", Description: "Read a source Message explicitly listed as material of your bound Issue.", InputSchema: objectSchema(map[string]any{"messageId": str}, "messageId")},
		{
			Name: "report_submit", Description: "Append a Report for the startup-bound Execution and queue a Topic notification in the same transaction. Supply the goalRevision of the goal you worked from. Reuse the same operationId for an identical retry. A retired binding can append only a historical Report.",
			InputSchema: objectSchema(map[string]any{"operationId": str, "kind": map[string]any{"type": "string", "enum": []string{"progress", "ready", "blocked", "failed"}}, "summary": str, "details": str, "evidence": refs, "questions": refs, "goalRevision": map[string]any{"type": "integer", "minimum": 1}, "requirementsRevision": map[string]any{"type": "integer", "minimum": 1}}, "operationId", "kind", "summary"),
		},
	}
}

func registerPRTool(topic bool) mcpTool {
	props := map[string]any{"operationId": stringSchema(), "url": stringSchema()}
	scope := "The PR link is saved on your current Issue and its parent Topic."
	if topic {
		props["issueId"] = stringSchema()
		scope = "Omit issueId for a Topic-level association or specify an Issue within your Topic."
	}
	return mcpTool{
		Name:        "pr_register",
		Description: "Record an explicit absolute PR resource URL and subscribe the bound Topic to its Thread. " + scope,
		InputSchema: objectSchema(props, "operationId", "url"),
	}
}

type mcpHandler struct {
	capability  capability
	http        *http.Client
	mu          sync.Mutex
	initialized bool
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
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil || startup.Token == "" || startup.BindingID == "" || startup.Generation < 1 || (startup.Role != "router" && startup.Role != "topic" && startup.Role != "worker") {
		return problem("invalid_argument", "invalid startup capability")
	}
	h := &mcpHandler{capability: startup, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	raw, err := h.call(ctx, "binding_get", json.RawMessage(`{}`))
	if err != nil {
		return err
	}
	var binding Binding
	if json.Unmarshal(raw, &binding) != nil || !capabilityMatches(startup, binding) {
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
	result, err := h.dispatch(ctx, r)
	if r.Notif {
		return
	}
	if err != nil {
		var rpcErr *rpc2.Error
		if !errors.As(err, &rpcErr) {
			var e *Error
			if !errors.As(err, &e) {
				e = problem("unavailable", "MCP operation failed")
			}
			rpcErr = &rpc2.Error{Code: -32000, Message: e.Message}
			rpcErr.SetError(e)
		}
		_ = conn.ReplyWithError(ctx, r.ID, rpcErr)
		return
	}
	_ = conn.Reply(ctx, r.ID, result)
}

func (h *mcpHandler) dispatch(ctx context.Context, r *rpc2.Request) (any, error) {
	invalid := func() error { return &rpc2.Error{Code: rpc2.CodeInvalidParams, Message: "Invalid params"} }
	if r.Method == "initialize" {
		var p struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    json.RawMessage `json:"capabilities"`
			ClientInfo      json.RawMessage `json:"clientInfo"`
		}
		if r.Params == nil || decodeStrict(*r.Params, &p) != nil || p.ProtocolVersion == "" {
			return nil, invalid()
		}
		version := p.ProtocolVersion
		switch version {
		case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		default:
			version = "2024-11-05"
		}
		h.mu.Lock()
		h.initialized = true
		h.mu.Unlock()
		return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "superagent", "version": "v1"}}, nil
	}
	if r.Method == "notifications/initialized" || r.Method == "ping" {
		return map[string]any{}, nil
	}
	h.mu.Lock()
	ready := h.initialized
	h.mu.Unlock()
	if !ready {
		return nil, problem("failed_precondition", "initialize is required")
	}
	switch r.Method {
	case "tools/list":
		var p struct {
			Cursor string          `json:"cursor,omitempty"`
			Meta   json.RawMessage `json:"_meta,omitempty"`
		}
		if r.Params != nil && (decodeStrict(*r.Params, &p) != nil || p.Cursor != "") {
			return nil, invalid()
		}
		return map[string]any{"tools": roleTools(h.capability.Role)}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta,omitempty"`
		}
		if r.Params == nil || decodeStrict(*r.Params, &p) != nil {
			return nil, invalid()
		}
		allowed := false
		for _, tool := range roleTools(h.capability.Role) {
			if tool.Name == p.Name {
				allowed = true
			}
		}
		if !allowed {
			return nil, problem("forbidden", "tool is outside startup role capability")
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage(`{}`)
		}
		raw, err := h.call(ctx, p.Name, p.Arguments)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": raw, "isError": false}, nil
	default:
		return nil, &rpc2.Error{Code: rpc2.CodeMethodNotFound, Message: "Method not found"}
	}
}

func (h *mcpHandler) call(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, h.capability.Endpoint+"/v1/role/"+tool, bytes.NewReader(args))
	if err != nil {
		return nil, problem("invalid_argument", "invalid Core endpoint")
	}
	r.Header.Set("Authorization", "Bearer "+h.capability.Token)
	r.Header.Set("Content-Type", "application/json")
	response, err := h.http.Do(r)
	if err != nil {
		return nil, problem("unavailable", "Core role API is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, problem("unavailable", "invalid Core role response")
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}
	if response.StatusCode != http.StatusOK || len(envelope.Result) == 0 {
		return nil, problem("unavailable", "Core role request failed")
	}
	return envelope.Result, nil
}
