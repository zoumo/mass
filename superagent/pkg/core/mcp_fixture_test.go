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
	case "issue_update_requirements", "issue_update_blockers", "issue_session_input", "issue_complete", "issue_cancel", "issue_reopen", "topic_complete", "topic_cancel", "topic_reopen":
	default:
		return tool, args
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return tool, args
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(raw, &decoded) != nil {
		return tool, args
	}
	fields := make(map[string]any, len(decoded))
	for name, value := range decoded {
		fields[name] = value
	}
	move := func(names ...string) map[string]any {
		moved := map[string]any{}
		for _, name := range names {
			if value, ok := fields[name]; ok {
				moved[name] = value
				delete(fields, name)
			}
		}
		return moved
	}
	switch tool {
	case "issue_update_requirements", "issue_update_blockers":
		if string(decoded["notifyIssueSession"]) == "true" {
			fields["sessionInput"] = map[string]string{"mode": "continue"}
		}
		delete(fields, "notifyIssueSession")
		if tool == "issue_update_blockers" {
			fields["blockers"] = move("add", "resolve", "unblockedStatus")
		}
	case "issue_session_input":
		fields["sessionInput"] = move("mode", "content")
		if materials, ok := fields["materials"]; ok {
			fields["materials"] = map[string]any{"add": materials}
		}
	default:
		trans := move("reason", "evidence")
		trans["action"] = tool[6:]
		fields["transition"] = trans
		if tool[:6] == "topic_" {
			return "update_topic", fields
		}
	}
	return "update_issue", fields
}

func mcpListTopicsFixture(t *testing.T, c *roleMCPClient, id string) core.Topic {
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
