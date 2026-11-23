package core_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const dropIssueQueryIndexes = `DROP INDEX issues_topic_status;
DROP INDEX issues_unfinished;`

func assertIssueQueryIndexes(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema
 WHERE type='index' AND name IN ('issues_topic_status','issues_unfinished')`).Scan(&count))
	require.Equal(t, want, count)
}

func TestTopicIssueListAcrossIndexInstall(t *testing.T) {
	f := fixtureAt(t)
	f.execution(newRouteBoundary(t))
	p := f.start()
	binding, _ := workBoundaryTopic(t, f, p)
	topic := startRoleMCP(t, f, binding)
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	other := input("issue-list-other-topic")
	other.Message.ThreadID, other.ReplyTarget = fixtureThreadID("issue-list-other"), nil
	message := submit(t, p, f, other)
	decision := decide(t, p, router.token, newRoute("issue-list-other", []string{message.MessageID}, f.dir))
	foreignBinding := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	foreign := startRoleMCP(t, f, foreignBinding)

	create := func(c *roleMCPClient, op string) core.Issue {
		t.Helper()
		return mcpWork[core.WorkReceipt](t, c, "create_issue", issueInput(f.dir, op)).Issue
	}
	first := create(topic, "first-pending")
	active := create(topic, "active")
	active = mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("start-active", active)).Issue
	blocked := blockers(t, topic, create(topic, "blocked"), "block", human("Await approval"))
	completed := create(topic, "completed")
	completed = mcpWork[core.WorkReceipt](t, topic, "issue_complete", core.CompleteIssueRequest{
		IssueID: completed.ID, CompleteRequest: acceptance("complete", completed.Revision, "verified.txt"),
	}).Issue
	canceled := create(topic, "canceled")
	canceled = mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("cancel", canceled)).Issue
	second := create(topic, "second-pending")
	foreignIssue := create(foreign, "foreign-pending")

	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assertIssueQueryIndexes(t, db, 2)
	const tied = "2026-01-01T00:00:00.000000000Z"
	setCreatedAt := func(id string) {
		t.Helper()
		_, err := db.ExecContext(t.Context(), `UPDATE issues SET created_at=?,body=json_set(body,'$.createdAt',?) WHERE id=?`, tied, tied, id)
		require.NoError(t, err)
	}
	setCreatedAt(first.ID)
	setCreatedAt(second.ID)
	_, err = db.ExecContext(t.Context(), dropIssueQueryIndexes)
	require.NoError(t, err)
	assertIssueQueryIndexes(t, db, 0)

	cases := []struct {
		status string
		ids    []string
	}{
		{"", []string{first.ID, second.ID, active.ID, blocked.ID, completed.ID, canceled.ID}},
		{"pending", []string{first.ID, second.ID}},
		{"active", []string{active.ID}},
		{"blocked", []string{blocked.ID}},
		{"completed", []string{completed.ID}},
		{"canceled", []string{canceled.ID}},
	}
	list := func(c *roleMCPClient, status string, limit int, cursor string) core.Page {
		t.Helper()
		return mcpWork[core.Page](t, c, "list_issues", map[string]any{"status": status, "limit": limit, "cursor": cursor})
	}
	assertItems := func(page core.Page, ids ...string) {
		t.Helper()
		require.Len(t, page.Items, len(ids))
		for i, id := range ids {
			var row map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(page.Items[i], &row))
			require.JSONEq(t, `"`+id+`"`, string(row["id"]))
			require.NotContains(t, row, "reports")
			require.NotContains(t, row, "pullRequests")
		}
	}
	read := func(c *roleMCPClient) map[string]json.RawMessage {
		t.Helper()
		return mcpWork[map[string]json.RawMessage](t, c, "get_issue", map[string]any{
			"issueId": first.ID, "include": []string{"reports", "pullRequests"},
		})
	}
	baseline := map[string]core.Page{}
	for _, c := range cases {
		baseline[c.status] = list(topic, c.status, 50, "")
		assertItems(baseline[c.status], c.ids...)
	}
	schemas := topic.call("tools/list", struct{}{})["result"]
	detail := read(topic)
	require.JSONEq(t, `[]`, string(detail["reports"]))
	delete(detail, "reports")
	delete(detail, "pullRequests")
	full, err := json.Marshal(detail)
	require.NoError(t, err)
	require.JSONEq(t, string(baseline["pending"].Items[0]), string(full))
	filtered := list(topic, "pending", 1, "")
	all := list(topic, "", 1, "")
	assertItems(filtered, first.ID)
	assertItems(all, first.ID)
	require.NotEmpty(t, filtered.NextCursor)
	require.NotEmpty(t, all.NextCursor)
	newIssue := create(topic, "new-but-backdated")
	setCreatedAt(newIssue.ID)
	p.stop(t, false)
	var version int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 27, version)

	f.start()
	assertIssueQueryIndexes(t, db, 2)
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 27, version)
	topic = startRoleMCP(t, f, binding)
	foreign = startRoleMCP(t, f, foreignBinding)
	router = startRoleMCP(t, f, routerBinding)
	listed := topic.call("tools/list", struct{}{})["result"]
	require.JSONEq(t, string(schemas), string(listed))
	var tools struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(listed, &tools))
	inputs := map[string]json.RawMessage{}
	for _, tool := range tools.Tools {
		inputs[tool.Name] = tool.InputSchema
	}
	require.JSONEq(t, `{"type":"object","required":[],"additionalProperties":false,"properties":{
 "status":{"type":"string","enum":["pending","active","blocked","completed","canceled"]},
 "cursor":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":200}}}`, string(inputs["list_issues"]))
	require.JSONEq(t, `{"type":"object","required":["issueId"],"additionalProperties":false,"properties":{
 "issueId":{"type":"string","minLength":1},"include":{"type":"array","items":{"type":"string","enum":["reports","pullRequests"]},"uniqueItems":true}}}`, string(inputs["get_issue"]))

	for _, c := range cases[2:] {
		require.Equal(t, baseline[c.status], list(topic, c.status, 50, ""))
	}
	for status, previous := range map[string]core.Page{"": all, "pending": filtered} {
		next := list(topic, status, 50, previous.NextCursor)
		require.Equal(t, baseline[status].Items[1:], next.Items)
		require.Empty(t, next.NextCursor)
	}
	assertItems(list(topic, "pending", 50, ""), first.ID, second.ID, newIssue.ID)
	assertItems(list(foreign, "pending", 50, ""), foreignIssue.ID)
	assertItems(list(foreign, "active", 50, ""))
	mcpWorkError(t, foreign, "list_issues", "invalid_argument", map[string]string{"status": "pending", "cursor": filtered.NextCursor})
	mcpWorkError(t, topic, "list_issues", "invalid_argument", map[string]string{"cursor": filtered.NextCursor})
	mcpWorkError(t, topic, "list_issues", "invalid_argument", map[string]string{"status": "Active"})
	mcpWorkError(t, router, "list_issues", "forbidden", struct{}{})
	mcpWorkError(t, router, "get_issue", "forbidden", map[string]string{"issueId": first.ID})
	mcpWorkError(t, foreign, "get_issue", "not_found", map[string]string{"issueId": first.ID})
	require.JSONEq(t, string(full), string(mcpWork[json.RawMessage](t, topic, "get_issue", map[string]string{"issueId": first.ID})))
	require.JSONEq(t, `[]`, string(read(topic)["reports"]))
}
