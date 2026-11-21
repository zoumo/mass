package core_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/superagent/pkg/core"
)

// Translate older test DTOs to the current public mutation arguments. This keeps
// lifecycle crash and recovery fixtures on the real MCP endpoint.
func mutationFixture(tool string, args any) (string, any) {
	switch tool {
	case "issue_update_requirements", "issue_update_blockers", "issue_session_input", "issue_complete", "issue_cancel", "issue_reopen", "topic_complete", "topic_cancel", "topic_reopen", "timer_update", "timer_cancel":
	default:
		return tool, args
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return tool, args
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return tool, args
	}
	move := func(names ...string) map[string]json.RawMessage {
		moved := map[string]json.RawMessage{}
		for _, name := range names {
			if value, ok := fields[name]; ok {
				moved[name] = value
				delete(fields, name)
			}
		}
		return moved
	}
	put := func(key string, value any) { fields[key], _ = json.Marshal(value) }
	switch tool {
	case "timer_update", "timer_cancel":
		if value, ok := fields["timerId"]; ok {
			fields["followupId"] = value
			delete(fields, "timerId")
		}
		if tool == "timer_update" {
			return "update_followup", fields
		}
		return "cancel_followup", fields
	case "issue_update_requirements", "issue_update_blockers":
		if string(fields["notifyIssueSession"]) == "true" {
			put("executionInput", map[string]string{"mode": "continue"})
		}
		delete(fields, "notifyIssueSession")
		if tool == "issue_update_blockers" {
			put("blockers", move("add", "resolve", "unblockedStatus"))
		}
	case "issue_session_input":
		put("executionInput", move("mode", "content"))
		if materials, ok := fields["materials"]; ok {
			put("materials", map[string]json.RawMessage{"add": materials})
		}
	default:
		trans := move("reason", "evidence")
		put("action", tool[6:])
		trans["action"] = fields["action"]
		delete(fields, "action")
		put("transition", trans)
		if tool[:6] == "topic_" {
			return "update_topic", fields
		}
	}
	return "update_issue", fields
}

func mcpListTopicsFixture(t *testing.T, c *stdioClient, id string) core.Topic {
	t.Helper()
	listed := mcpWork[core.Page](t, c, "list_topics", map[string]string{"query": id})
	for _, raw := range listed.Items {
		var item core.Topic
		require.NoError(t, json.Unmarshal(raw, &item))
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("Topic %s not listed", id)
	return core.Topic{}
}
