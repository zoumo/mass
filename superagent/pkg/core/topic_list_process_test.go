package core_test

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestRouterTopicListPaginationAcrossIndexInstall(t *testing.T) {
	f := fixtureAt(t)
	f.execution(newRouteBoundary(t))
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	r := startRoleMCP(t, f, router)
	createTopic := func(op, title string) string {
		t.Helper()
		in := input(op)
		in.Message.ThreadKey = fixtureThreadKey(op)
		in.ReplyTarget = nil
		message := submit(t, p, f, in)
		request := newRoute(op, []string{message.MessageID}, f.dir)
		request.Targets[0].Create.Title = title
		return decide(t, p, r.token, request).Targets[0].TopicID
	}
	first := createTopic("topic-list-first", "Project Deployment")
	second := createTopic("topic-list-second", "Deployment Checklist")
	third := createTopic("topic-list-third", "Separate work")
	secondBinding := waitBinding(t, p, "topic", second, "ready")
	topic := startRoleMCP(t, f, secondBinding)
	completed := mcpWork[core.Topic](t, topic, "topic_complete", acceptance("topic-list-complete", 1, "Reviewed"))
	require.Equal(t, "completed", completed.Status)
	mcpWorkError(t, topic, "list_topics", "forbidden", struct{}{})

	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(r.call("tools/list", struct{}{})["result"], &listed))
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Name == "list_topics" {
			require.JSONEq(t, `{"type":"object","required":[],"additionalProperties":false,"properties":{
 "query":{"type":"string","minLength":1},"cursor":{"type":"string","minLength":1},
 "limit":{"type":"integer","minimum":1,"maximum":200}}}`, string(tool.InputSchema))
		}
	}
	require.ElementsMatch(t, []string{"get_internal_event", "get_message", "list_messages", "send_reply", "list_topics", "route_messages"}, names)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	setCreatedAt := func(id, at string) {
		t.Helper()
		_, err := db.ExecContext(t.Context(), `UPDATE topics SET created_at=?,body=json_set(body,'$.createdAt',?) WHERE id=?`, at, at, id)
		require.NoError(t, err)
	}
	const tied = "2026-01-01T00:00:00.000000000Z"
	const later = "2026-01-02T00:00:00.000000000Z"
	setCreatedAt(first, tied)
	setCreatedAt(second, tied)
	setCreatedAt(third, later)
	_, err = db.ExecContext(t.Context(), "DROP INDEX topics_created_at")
	require.NoError(t, err)

	checkPage := func(result core.Page, ids ...string) []core.Topic {
		t.Helper()
		require.Len(t, result.Items, len(ids))
		topics := make([]core.Topic, len(ids))
		for i, id := range ids {
			require.NoError(t, json.Unmarshal(result.Items[i], &topics[i]))
			require.Equal(t, id, topics[i].ID)
		}
		return topics
	}
	// Match the cursor bytes produced by schema 25 before the index was added.
	legacy := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"s":%q,"k":"topics","u":3,"a":%q,"i":%q}`, router.ID, tied, first))
	oldest := mcpWork[core.Page](t, r, "list_topics", map[string]any{"limit": 1})
	checkPage(oldest, first)
	require.Equal(t, legacy, oldest.NextCursor)
	newest := page(t, p, "topics?order=newest&limit=1")
	checkPage(newest, third)
	require.NotEmpty(t, newest.NextCursor)

	fourth := createTopic("topic-list-fourth", "New Deployment")
	setCreatedAt(fourth, tied) // The new row sorts within both cursor ranges.
	p.stop(t, false)
	var version int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 26, version)

	p = f.start()
	var indexSQL string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name='topics_created_at'").Scan(&indexSQL))
	require.Contains(t, indexSQL, "ON topics(created_at,id)")
	r = startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	continued := mcpWork[core.Page](t, r, "list_topics", map[string]string{"cursor": legacy})
	checkPage(continued, second, third)
	require.Empty(t, continued.NextCursor)
	mcpWorkError(t, r, "list_topics", "invalid_argument", map[string]string{"cursor": legacy, "query": "deploy"})

	continued = page(t, p, "topics?order=newest&limit=1&cursor="+url.QueryEscape(newest.NextCursor))
	checkPage(continued, second)
	require.NotEmpty(t, continued.NextCursor)
	end := page(t, p, "topics?order=newest&cursor="+url.QueryEscape(continued.NextCursor))
	checkPage(end, first)
	require.Empty(t, end.NextCursor)

	matches := mcpWork[core.Page](t, r, "list_topics", map[string]string{"query": "pLoYmEnT"})
	summaries := checkPage(matches, first, second, fourth)
	require.Equal(t, "completed", summaries[1].Status)
	idMatch := mcpWork[core.Page](t, r, "list_topics", map[string]string{"query": "SS-T2"})
	checkPage(idMatch, second)
}
