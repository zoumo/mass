package core

func roleTools(role string) []mcpTool {
	text := stringSchema()
	revision := map[string]any{"type": "integer", "minimum": 1}
	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	include := func(values ...string) map[string]any {
		return map[string]any{"type": "array", "items": enum(values...), "uniqueItems": true}
	}
	tool := func(name, description string, props map[string]any, required ...string) mcpTool {
		result := mcpTool{Name: name, Description: description, InputSchema: objectSchema(props, required...)}
		if traceRoleTool(name) {
			props[triggerEventArg] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": triggerEventDescription}
		}
		return result
	}
	material := objectSchema(map[string]any{"label": text, "messageId": text, "uri": text})
	material["oneOf"] = []map[string]any{{"required": []string{"messageId"}}, {"required": []string{"uri"}}}
	materials := array(material)
	prs := objectSchema(map[string]any{"add": map[string]any{"type": "array", "items": text, "minItems": 1}}, "add")
	transition := objectSchema(map[string]any{"action": enum("complete", "cancel", "reopen"), "reason": text, "evidence": map[string]any{"type": "array", "items": text, "minItems": 1}}, "action", "reason")
	transition["if"] = map[string]any{"properties": map[string]any{"action": enum("complete")}}
	transition["then"] = map[string]any{"required": []string{"evidence"}}
	transition["else"] = map[string]any{"not": map[string]any{"required": []string{"evidence"}}}
	issueUpdate := map[string]any{"operationId": text, "expectedRevision": revision, "title": text, "materials": objectSchema(map[string]any{"add": map[string]any{"type": "array", "items": material, "minItems": 1}}, "add"), "pullRequests": prs}
	tools := []mcpTool{tool("get_internal_event", "Read the stored event addressed to you. Use when truncated input omits a fact you need.", map[string]any{"event_id": text}, "event_id")}
	if role == "issue" {
		return append(tools,
			tool("get_issue", "Read your bound Issue, current Execution, goal revision, materials, and blockers.", nil),
			tool("get_message", "Read a Message referenced in your Issue materials.", map[string]any{"messageId": text}, "messageId"),
			tool("update_issue", "Update the title, append materials, or register PR URLs for your bound Issue. Use its current revision. Supply at least one change.", issueUpdate, "operationId", "expectedRevision"),
			tool("submit_report", "Report the result of your bound Execution against the goalRevision you used. A retired Execution can submit a historical Report.", map[string]any{"operationId": text, "kind": enum("progress", "ready", "blocked", "failed"), "summary": text, "goalRevision": revision, "details": text, "evidence": array(text), "questions": array(text)}, "operationId", "kind", "summary", "goalRevision"),
		)
	}
	messageIncludes := []string{"thread", "replies"}
	if role == "router" {
		messageIncludes = append(messageIncludes, "routing")
	}
	replyArgs := map[string]any{"operationId": text, "messageId": text, "content": structuredContentInputSchema()}
	if role == "topic" {
		replyArgs["blockerRef"] = objectSchema(map[string]any{"issueId": text, "blockerId": text}, "issueId", "blockerId")
	}
	tools = append(tools,
		tool("get_message", "Read a scoped Message. Request only the expansions you need. Router can request routing round, state, and return reasons.", map[string]any{"messageId": text, "include": include(messageIncludes...)}, "messageId"),
		tool("list_messages", "Read visible Message history. Keep the same filters when using a cursor.", map[string]any{"threadKey": text, "cursor": text, "limit": limit}),
		tool("send_reply", "Queue a structured reply to a specific Message. Reuse the same operationId on retry.", replyArgs, "operationId", "messageId", "content"),
	)
	if role == "router" {
		ids := map[string]any{"type": "array", "items": text, "minItems": 1, "uniqueItems": true}
		target := objectSchema(map[string]any{"messageIds": ids, "topicId": text, "create": objectSchema(map[string]any{"title": text, "workspace": text, "description": map[string]any{"type": "string"}, "goal": text}, "title", "workspace")}, "messageIds")
		target["oneOf"] = []map[string]any{{"required": []string{"topicId"}}, {"required": []string{"create"}}}
		route := tool("route_messages", "Route or ignore an explicit set of Messages. Map each Message ID to its current routing round. To route, select targets and supply explicit messageIds for each target.", map[string]any{"operationId": text, "messageIds": ids, "rounds": map[string]any{"type": "object", "additionalProperties": revision}, "kind": enum("route", "ignore"), "reason": text, "targets": map[string]any{"type": "array", "items": target, "minItems": 1}}, "operationId", "messageIds", "rounds", "kind", "reason")
		route.InputSchema["if"] = map[string]any{"properties": map[string]any{"kind": enum("route")}}
		route.InputSchema["then"] = map[string]any{"required": []string{"targets"}}
		route.InputSchema["else"] = map[string]any{"not": map[string]any{"required": []string{"targets"}}}
		return append(tools, tool("list_topics", "Find candidate Topic summaries by title or ID.", map[string]any{"query": text, "cursor": text, "limit": limit}), route)
	}
	resolve := array(objectSchema(map[string]any{"blockerId": text, "reason": text, "messageId": text}, "blockerId", "reason"))
	blockers := objectSchema(map[string]any{"add": array(objectSchema(map[string]any{"kind": enum("human", "dependency"), "reason": text, "dependencyIssueId": text}, "kind", "reason")), "resolve": resolve, "unblockedStatus": enum("pending", "active")})
	issueUpdate["issueId"], issueUpdate["goal"], issueUpdate["blockers"] = text, text, blockers
	issueUpdate["executionInput"] = objectSchema(map[string]any{"mode": enum(contextSupplement, "continue"), "content": structuredContentInputSchema()}, "mode")
	issueUpdate["transition"] = transition
	dueAt := map[string]any{"type": "string", "format": "date-time"}
	return append(tools,
		tool("get_topic", "Read your bound Topic. Expand templates, followups, or PR associations when needed.", map[string]any{"include": include("executionTemplates", "followups", "pullRequests")}),
		tool("update_topic", "Edit your Topic, register PR URLs, or explicitly complete, cancel, or reopen it. Use its current revision. Completion requires evidence and terminal Issues.", map[string]any{"operationId": text, "expectedRevision": revision, "title": text, "description": map[string]any{"type": "string"}, "goal": text, "pullRequests": prs, "transition": transition}, "operationId", "expectedRevision"),
		tool("create_issue", "Create an Issue with a goal and acceptance criteria. Choose runtimeRef and model together. The Issue inherits the Topic workspace unless you supply one. Use start=true to schedule its Execution atomically.", map[string]any{"operationId": text, "title": text, "goal": text, "runtimeRef": text, "model": text, "workspace": text, "materials": materials, "start": map[string]any{"type": "boolean"}}, "operationId", "title", "goal", "runtimeRef", "model"),
		tool("get_issue", "Read an Issue in your Topic, with optional Executions, Reports, and PR associations.", map[string]any{"issueId": text, "include": include("executions", "reports", "pullRequests")}, "issueId"),
		tool("list_issues", "List Issues in your Topic, optionally filtered by status.", map[string]any{"status": enum("pending", "active", "blocked", "completed", "canceled"), "cursor": text, "limit": limit}),
		tool("start_issue", "Schedule an Execution for an Issue at its current revision. Resolve open blockers first or in this call. To change its execution profile, supply runtimeRef and model together.", map[string]any{"operationId": text, "issueId": text, "expectedRevision": revision, "resolve": resolve, "runtimeRef": text, "model": text}, "operationId", "issueId", "expectedRevision"),
		tool("update_issue", "Update an Issue in your Topic at its current revision. You may combine changes, materials, blockers, and PR associations. Use executionInput to supplement or continue a current Execution, or transition for an explicit lifecycle decision.", issueUpdate, "operationId", "issueId", "expectedRevision"),
		tool("get_report", "Read the full Report and its evidence for an Issue in your Topic.", map[string]any{"reportId": text}, "reportId"),
		tool("decline_message", "Decline a routed Message that belongs elsewhere. Copy event_id and give a reason so Router can reconsider it.", map[string]any{"event_id": text, "reason": text}, "event_id", "reason"),
		tool("schedule_followup", "Schedule a one-time followup at an RFC3339 timestamp with a time zone.", map[string]any{"operationId": text, "dueAt": dueAt, "content": text, "materials": materials}, "operationId", "dueAt", "content"),
		tool("update_followup", "Edit a scheduled followup at its current revision. Supply at least one change. An empty materials array clears materials.", map[string]any{"operationId": text, "followupId": text, "expectedRevision": revision, "dueAt": dueAt, "content": text, "materials": materials}, "operationId", "followupId", "expectedRevision"),
		tool("cancel_followup", "Cancel a scheduled followup at its current revision.", map[string]any{"operationId": text, "followupId": text, "expectedRevision": revision}, "operationId", "followupId", "expectedRevision"),
	)
}
