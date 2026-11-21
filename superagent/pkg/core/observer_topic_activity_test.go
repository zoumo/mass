package core_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type topicEvent struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	At        string `json:"at"`
	IssueID   string `json:"issueId"`
	MessageID string `json:"messageId"`
	Actor     string `json:"actor"`
	Title     string `json:"title"`
	Status    string `json:"status"`
}

func topicEvents(t *testing.T, p *process, query string) ([]topicEvent, string) {
	t.Helper()
	got := page(t, p, "topic-activity?"+query)
	var rows []topicEvent
	for _, raw := range got.Items {
		var item topicEvent
		require.NoError(t, json.Unmarshal(raw, &item))
		rows = append(rows, item)
	}
	return rows, got.NextCursor
}

func TestObserverTopicActivityWatermark(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	receipt := submit(t, p, f, input("timeline"))
	const at = "2024-05-05T05:05:05.000000000Z"
	const updated = "2024-05-06T05:05:05.000000000Z"
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	insert := func(query string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(t.Context(), query, args...)
		require.NoError(t, err)
	}
	for _, topic := range []string{"mass-t1", "mass-t2"} {
		insert(`INSERT INTO topics(created_at,body,creation_key,creation_digest)
 VALUES(?2,json_object('id',?1,'status','active','title',?1,'createdAt',?2),?1,'')`, topic, at)
	}
	insert(`INSERT INTO issues(topic_id,number,created_at,body,creation_key,creation_digest) VALUES('mass-t1',1,?1,
 json_object('id','mass-t1-i1','topicId','mass-t1','title','Older snapshot','status','completed','createdAt',?1,'updatedAt',?2,'goalRevision',4),'a','')`, at, updated)
	insert(`INSERT INTO issues(topic_id,number,created_at,body,creation_key,creation_digest) VALUES('mass-t1',2,?1,
 json_object('id','mass-t1-i2','topicId','mass-t1','title','Issue B','status','pending','createdAt',?1,'updatedAt',?1),'b','')`, at)
	addEvent := func(id string) {
		insert(`INSERT INTO internal_events(event_id,kind,created_at,sender,recipient,refs,status)
 VALUES(?1,'routed_message',?2,'{"kind":"router"}','{"kind":"topic","topic_id":"mass-t1"}',
 json_array(json_object('kind','trigger_message','id',?3)),'sent')`, id, at, receipt.MessageID)
	}
	addEvent("receive")
	query := "topicId=mass-t1&limit=1"
	first, token := topicEvents(t, p, query)
	require.Len(t, first, 1)
	require.Equal(t, "issue_snapshot", first[0].Kind)
	require.Empty(t, first[0].Actor)
	require.NotEmpty(t, token)
	for _, wrong := range []string{"topicId=mass-t2", "topicId=mass-t1&filter=message", "topicId=mass-t1&order=oldest"} {
		status, _ := request(t, "GET", p.endpoint, "/v1/observer/topic-activity?"+wrong+"&cursor="+url.QueryEscape(token), "", nil)
		require.Equal(t, 400, status)
	}
	// A late insertion even with the exact same saved timestamp must not enter page two.
	addEvent("late")
	all := first
	for token != "" {
		next, cursor := topicEvents(t, p, query+"&cursor="+url.QueryEscape(token))
		require.Len(t, next, 1)
		all = append(all, next...)
		token = cursor
	}
	require.Len(t, all, 4)
	require.Equal(t, []string{"routed_message", "issue_created", "issue_created"}, []string{all[1].Kind, all[2].Kind, all[3].Kind})
	require.Equal(t, "mass-t1-i2", all[2].IssueID)
	require.Equal(t, "mass-t1-i1", all[3].IssueID)
	require.Empty(t, all[2].MessageID)
	require.Equal(t, "router", all[1].Actor)
	require.Equal(t, "sent", all[1].Status)
	msg, _ := topicEvents(t, p, "topicId=mass-t1&filter=message")
	require.Len(t, msg, 2)
	issues, _ := topicEvents(t, p, "topicId=mass-t1&filter=issue")
	require.Len(t, issues, 3)
	for _, row := range issues {
		require.NotEqual(t, "completion", row.Kind)
		require.NotEqual(t, "goal_notification", row.Kind)
		require.Empty(t, row.MessageID)
	}
	empty, cursor := topicEvents(t, p, "topicId=mass-t2")
	require.Empty(t, empty)
	require.Empty(t, cursor)
	for _, test := range []struct {
		method, query string
		status        int
	}{
		{"GET", "topicId=missing", 404}, {"GET", "topicId=mass-t1&filter=unknown", 400}, {"GET", "", 400}, {"GET", "topicId=mass-t1&limit=201", 400}, {"GET", "topicId=mass-t1&cursor=broken", 400}, {"HEAD", "topicId=mass-t1", 200}, {"POST", "topicId=mass-t1", 405},
	} {
		t.Run(fmt.Sprintf("%s_%s", test.method, test.query), func(t *testing.T) {
			status, _ := request(t, test.method, p.endpoint, "/v1/observer/topic-activity?"+test.query, "", nil)
			require.Equal(t, test.status, status)
		})
	}
}

func TestObserverTopicActivitySavedActions(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	m := submit(t, p, f, input("activity-saved"))
	d := decide(t, p, roleToken(t, f, router), newRoute("activity-saved", []string{m.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", d.Targets[0].TopicID, "ready")
	token := roleToken(t, f, topic)
	created := roleWork[core.WorkReceipt](t, p, token, "create_issue", issueInput(f.dir, "activity-saved"))
	b := roleWork[core.WorkReceipt](t, p, token, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "hold", IssueID: created.Issue.ID, ExpectedRevision: created.Issue.Revision, Add: []core.BlockerInput{human("review needed")},
	})
	resolved := roleWork[core.WorkReceipt](t, p, token, "issue_update_blockers", core.UpdateBlockersRequest{OperationID: "resolve", IssueID: b.Issue.ID, ExpectedRevision: b.Issue.Revision, UnblockedStatus: "pending", Resolve: []core.BlockerResolution{{BlockerID: b.Issue.Blockers[0].ID, Reason: "review recorded"}}})
	canceled := roleWork[core.WorkReceipt](t, p, token, "issue_cancel", issueLifecycle("cancel", resolved.Issue))
	roleWork[core.WorkReceipt](t, p, token, "issue_reopen", issueLifecycle("reopen", canceled.Issue))
	got, _ := topicEvents(t, p, "topicId="+topic.OwnerID+"&filter=issue")
	kinds := []string{}
	for _, event := range got {
		kinds = append(kinds, event.Kind)
		require.Equal(t, created.Issue.ID, event.IssueID)
		require.Equal(t, "topic", event.Actor)
		require.Empty(t, event.MessageID)
	}
	require.Equal(t, []string{"reopening", "cancellation", "blocker_resolved", "issue_created"}, kinds)
	cli := exec.CommandContext(t.Context(), build(t), "observe", "topic-activity", "--topic-id", topic.OwnerID, "--filter", "issue", "--endpoint", p.endpoint)
	body, err := cli.Output()
	require.NoError(t, err)
	var actual struct{ Items []topicEvent }
	require.NoError(t, json.Unmarshal(body, &actual))
	require.Equal(t, got, actual.Items)
}
