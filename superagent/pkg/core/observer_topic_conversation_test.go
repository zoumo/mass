package core_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type conversationRow struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Kind             string `json:"kind"`
	TopicID          string `json:"topicId"`
	ThreadID         string `json:"threadId"`
	MessageID        string `json:"messageId"`
	EventID          string `json:"eventId"`
	Actor            string `json:"actor"`
	Recipient        string `json:"recipient"`
	LastMessageAt    string `json:"lastMessageAt"`
	DispatchKind     string `json:"dispatchKind"`
	SessionBindingID string `json:"sessionBindingId"`
	IssueID          string `json:"issueId"`
	At               string `json:"at"`
	ReportID         string `json:"reportId"`
	ReportKind       string `json:"reportKind"`
	Summary          string `json:"summary"`
}

func topicConversationPath(query url.Values) string {
	path := "/v1/core/topics"
	if id := query.Get("topicId"); id != "" {
		path += "/" + url.PathEscape(id) + "/activity"
		query.Del("topicId")
	}
	query.Set("view", "conversation")
	return path + "?" + query.Encode()
}

func conversationPage(t *testing.T, p *process, query url.Values) ([]conversationRow, string) {
	t.Helper()
	status, body := request(t, http.MethodGet, p.endpoint, topicConversationPath(query), "", nil)
	require.Equal(t, http.StatusOK, status, "%s", body)
	var result core.Page
	require.NoError(t, json.Unmarshal(body, &result))
	rows := make([]conversationRow, 0, len(result.Items))
	for _, raw := range result.Items {
		var row conversationRow
		require.NoError(t, json.Unmarshal(raw, &row))
		rows = append(rows, row)
	}
	return rows, result.NextCursor
}

func conversationInput(id, thread string) core.SubmitTriggerMessageRequest {
	msg := input(id)
	moveInput(&msg, thread)
	return msg
}

func TestObserverTopicConversationIngress(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	token := roleToken(t, f, router)

	shared := submit(t, p, f, conversationInput("shared", "shared:thread/%项目"))
	route := newRoute("first", []string{shared.MessageID}, f.dir)
	route.Targets = append(route.Targets, core.RouteTarget{
		Create: &core.NewTopic{Title: "Topic second", Workspace: f.dir}, MessageIDs: []string{shared.MessageID},
	})
	decision := decide(t, p, token, route)
	topicA, topicB := decision.Targets[0].TopicID, decision.Targets[1].TopicID

	unique := submit(t, p, f, conversationInput("unique", "unique"))
	routeUnique := newRoute("unique", []string{unique.MessageID}, f.dir)
	routeUnique.Targets[0].Create, routeUnique.Targets[0].TopicID = nil, topicB
	decide(t, p, token, routeUnique)

	first, cursor := conversationPage(t, p, url.Values{"limit": {"1"}})
	require.Len(t, first, 1)
	require.Equal(t, topicB, first[0].ID)
	require.NotEmpty(t, first[0].LastMessageAt)
	require.NotEmpty(t, cursor)
	rows, _ := conversationPage(t, p, url.Values{"limit": {"1"}, "search": {"FIRST"}})
	require.Equal(t, []string{topicA}, []string{rows[0].ID})

	// Promote the earlier Topic after the first page. Its older position
	// remains available across continuation, and a fresh reader sees the move.
	promote := submit(t, p, f, conversationInput("promote", "another"))
	routePromote := newRoute("promote", []string{promote.MessageID}, f.dir)
	routePromote.Targets[0].Create, routePromote.Targets[0].TopicID = nil, topicA
	decide(t, p, token, routePromote)
	older, end := conversationPage(t, p, url.Values{"limit": {"1"}, "cursor": {cursor}})
	require.Empty(t, end)
	require.Len(t, older, 1)
	require.Equal(t, topicA, older[0].ID)
	latest, _ := conversationPage(t, p, url.Values{"limit": {"1"}})
	require.Equal(t, topicA, latest[0].ID)

	msg := conversationInput("direct", "unique")
	direct := submit(t, p, f, msg)
	require.Equal(t, direct.EventID, submit(t, p, f, msg).EventID)
	both, next := conversationPage(t, p, url.Values{"topicId": {topicB}, "limit": {"1"}})
	require.Len(t, both, 1)
	require.NotEmpty(t, next)
	require.Equal(t, "thread_direct/"+direct.EventID, both[0].ID)
	require.Equal(t, "thread_direct", both[0].Kind)
	require.Equal(t, topicB, both[0].TopicID)
	require.Equal(t, direct.ThreadID, both[0].ThreadID)
	require.Equal(t, direct.MessageID, both[0].MessageID)
	require.Equal(t, direct.EventID, both[0].EventID)
	require.Equal(t, "core", both[0].Actor)
	require.Equal(t, "topic", both[0].Recipient)
	routed, _ := conversationPage(t, p, url.Values{"topicId": {topicB}, "filter": {"router_routed"}})
	require.Len(t, routed, 2)
	require.Equal(t, shared.ThreadID, routed[1].ThreadID)
	require.Equal(t, shared.MessageID, routed[1].MessageID)
	require.Equal(t, decision.Targets[1].EventIDs[shared.MessageID], routed[1].EventID)
	require.Equal(t, "router", routed[1].Actor)
	directRows, _ := conversationPage(t, p, url.Values{"topicId": {topicB}, "filter": {"thread_direct"}})
	require.Equal(t, both, directRows)

	for _, check := range []struct {
		method string
		query  url.Values
		status int
	}{
		{"GET", url.Values{"topicId": {topicB}, "filter": {"router_routed"}, "cursor": {next}}, 400},
		{"GET", url.Values{"topicId": {topicA}, "cursor": {next}}, 400},
		{"GET", url.Values{"search": {"second"}, "cursor": {cursor}}, 400},
		{"GET", url.Values{"topicId": {topicA}, "eventId": {both[0].ID}}, 404},
		{"GET", url.Values{"topicId": {topicB}, "eventId": {both[0].ID}}, 200},
		{"HEAD", url.Values{}, 200},
		{"POST", url.Values{}, 405},
	} {
		status, body := request(t, check.method, p.endpoint, topicConversationPath(check.query), "", nil)
		require.Equal(t, check.status, status, string(body))
	}
}

func TestObserverTopicConversationIssueDispatch(t *testing.T) {
	boundary := newRouteBoundary(t)
	f, _ := eventFixture(t, boundary.url)
	p := f.start()
	routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
	createTopic := func(label string) (core.SessionBinding, core.Receipt) {
		t.Helper()
		message := submit(t, p, f, conversationInput(label, label))
		route := decide(t, p, routerToken, newRoute(label, []string{message.MessageID}, f.dir))
		return waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready"), message
	}
	topic, ingress := createTopic("dispatch")
	other, _ := createTopic("other")
	token := roleToken(t, f, topic)
	issue := roleWork[core.WorkReceipt](t, p, token, "create_issue", issueInput(f.dir, "dispatch")).Issue

	rows, _ := conversationPage(t, p, url.Values{"topicId": {topic.OwnerID}, "filter": {"issue_session_dispatch"}})
	require.Empty(t, rows)
	rows, _ = conversationPage(t, p, url.Values{"topicId": {topic.OwnerID}, "filter": {"issue_created"}})
	require.Len(t, rows, 1)
	require.Equal(t, "issue_created/"+issue.ID, rows[0].ID)
	topics, _ := conversationPage(t, p, url.Values{})
	require.Equal(t, other.OwnerID, topics[0].ID, "creating an Issue alone does not promote its Topic")

	started := roleWork[core.WorkReceipt](t, p, token, "start_issue", startRequest("start", issue))
	waitBinding(t, p, "issue", started.SessionBinding.ID, "ready")
	before, cursor := conversationPage(t, p, url.Values{"topicId": {topic.OwnerID}, "limit": {"1"}})
	require.Len(t, before, 1)
	require.NotEmpty(t, cursor)
	require.Equal(t, "issue_session_dispatch/"+started.SessionBinding.StartEventID, before[0].ID)
	topics, _ = conversationPage(t, p, url.Values{})
	require.Equal(t, topic.OwnerID, topics[0].ID, "Issue Session startup is a conversation message")

	supplemented := roleWork[core.WorkReceipt](t, p, token, "issue_session_input", core.IssueSessionInputRequest{
		OperationID: "supplement", IssueID: issue.ID, ExpectedRevision: started.Issue.Revision, Mode: "context_supplement",
		Content:   &core.StructuredContent{Type: "markdown", Data: json.RawMessage(`{"text":"Context for Issue Session"}`)},
		Materials: []core.Material{{Label: "Ingress", MessageID: ingress.MessageID}},
	})
	continued := roleWork[core.WorkReceipt](t, p, token, "issue_session_input", core.IssueSessionInputRequest{
		OperationID: "continue", IssueID: issue.ID, ExpectedRevision: supplemented.Issue.Revision, Mode: "continue",
		Content: &core.StructuredContent{Type: "markdown", Data: json.RawMessage(`{"text":"Continue the current work"}`)},
	})
	updated := roleWork[core.WorkReceipt](t, p, token, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "goal", IssueID: issue.ID, ExpectedRevision: continued.Issue.Revision, Goal: "Updated goal", NotifyIssueSession: true,
	})
	remainder, end := conversationPage(t, p, url.Values{"topicId": {topic.OwnerID}, "cursor": {cursor}})
	require.Empty(t, end)
	require.Len(t, remainder, 2)
	require.Equal(t, "issue_created/"+issue.ID, remainder[0].ID)
	require.Equal(t, ingress.MessageID, remainder[1].MessageID)

	result := page(t, p, "topics/"+topic.OwnerID+"/activity?view=conversation&filter=issue_session_dispatch")
	want := []struct {
		kind, id, sessionBindingID string
	}{
		{"goal_notification", updated.NotificationEventID, started.SessionBinding.ID},
		{"issue_session_update", continued.NotificationEventID, started.SessionBinding.ID},
		{"context_supplement", supplemented.NotificationEventID, started.SessionBinding.ID},
		{"issue_session_started", started.SessionBinding.StartEventID, started.SessionBinding.ID},
	}
	require.Len(t, result.Items, len(want))
	var latestDispatchAt string
	for i, raw := range result.Items {
		var dispatch struct {
			ID               string `json:"id"`
			DispatchKind     string `json:"dispatchKind"`
			SessionBindingID string `json:"sessionBindingId"`
			IssueID          string `json:"issueId"`
			At               string `json:"at"`
			Status           string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(raw, &dispatch))
		require.Equal(t, "issue_session_dispatch/"+want[i].id, dispatch.ID)
		require.Equal(t, want[i].kind, dispatch.DispatchKind)
		require.Equal(t, want[i].sessionBindingID, dispatch.SessionBindingID)
		require.Equal(t, issue.ID, dispatch.IssueID)
		require.NotEmpty(t, dispatch.Status)
		if i == 0 {
			latestDispatchAt = dispatch.At
		}
	}
	topics, _ = conversationPage(t, p, url.Values{})
	require.Equal(t, latestDispatchAt, topics[0].LastMessageAt)
	rows, _ = conversationPage(t, p, url.Values{"topicId": {other.OwnerID}})
	require.Len(t, rows, 1)
	require.True(t, strings.HasPrefix(rows[0].ID, "router_routed/"))
}

type conversationFixture struct {
	process       *process
	issueToken    string
	topicID       string
	otherTopicID  string
	issue         core.Issue
	binding       core.SessionBinding
	routedEventID string
	direct        core.Receipt
	report        core.ReportRequest
}

func seedTopicConversation(t *testing.T, extraMessages int) conversationFixture {
	t.Helper()
	boundary := newRouteBoundary(t)
	f, _ := eventFixture(t, boundary.url)
	p := f.start()
	routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))

	routed := submit(t, p, f, conversationInput("conversation-routed", "conversation"))
	route := newRoute("conversation-main", []string{routed.MessageID}, f.dir)
	route.Targets[0].Create.Title = "Conversation pipeline"
	target := decide(t, p, routerToken, route).Targets[0]
	topic := waitBinding(t, p, "topic", target.TopicID, "ready")
	token := roleToken(t, f, topic)
	create := issueInput(f.dir, "conversation-issue")
	create.Title = "Verify conversation build"
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
	issue := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")

	direct := submit(t, p, f, conversationInput("conversation-direct", "conversation"))
	for i := range extraMessages {
		submit(t, p, f, conversationInput(fmt.Sprintf("conversation-extra-%02d", i), "conversation"))
	}
	other := submit(t, p, f, conversationInput("conversation-other", "conversation-other"))
	otherRoute := newRoute("conversation-other", []string{other.MessageID}, f.dir)
	otherRoute.Targets[0].Create.Title = "Other recent conversation"
	otherTopicID := decide(t, p, routerToken, otherRoute).Targets[0].TopicID
	return conversationFixture{
		process: p, issueToken: roleToken(t, f, issue), topicID: topic.OwnerID,
		otherTopicID: otherTopicID, issue: work.Issue, binding: *work.SessionBinding,
		routedEventID: target.EventIDs[routed.MessageID], direct: direct,
		report: core.ReportRequest{
			OperationID: "conversation-report", Kind: "ready", Summary: "Conversation build verified",
			Details: "All conversation build checks passed.", Evidence: []string{"artifacts/conversation.log"},
			Questions: []string{"Ready to publish?"}, GoalRevision: work.SessionBinding.StartGoalRevision,
		},
	}
}

func TestObserverTopicConversationReport(t *testing.T) {
	f := seedTopicConversation(t, 0)
	p := f.process
	topics, sidebarCursor := conversationPage(t, p, url.Values{"limit": {"1"}})
	require.Len(t, topics, 1)
	require.Equal(t, f.otherTopicID, topics[0].ID)
	require.NotEmpty(t, sidebarCursor)
	first, timelineCursor := conversationPage(t, p, url.Values{"topicId": {f.topicID}, "limit": {"1"}})
	require.Len(t, first, 1)
	require.Equal(t, "thread_direct/"+f.direct.EventID, first[0].ID)
	require.Equal(t, f.direct.ThreadID, first[0].ThreadID)
	require.NotEmpty(t, timelineCursor)

	report := roleWork[core.Report](t, p, f.issueToken, "submit_report", f.report)
	require.Equal(t, report, roleWork[core.Report](t, p, f.issueToken, "submit_report", f.report))
	notification := readRecord[workEventRecord](t, p, "internal-events/"+report.EventID)
	require.Equal(t, "report_submitted", notification.Kind)
	require.Equal(t, report, readRecord[core.Report](t, p, "reports/"+report.ID))

	older, next := conversationPage(t, p, url.Values{"limit": {"1"}, "cursor": {sidebarCursor}})
	require.Empty(t, next)
	require.Len(t, older, 1)
	require.Equal(t, f.topicID, older[0].ID)
	require.Equal(t, first[0].At, older[0].LastMessageAt, "the Report cannot change sidebar continuation")
	topics, _ = conversationPage(t, p, url.Values{})
	require.Len(t, topics, 2)
	require.Equal(t, f.topicID, topics[0].ID)
	require.Equal(t, report.CreatedAt, topics[0].LastMessageAt)

	remainder, _ := conversationPage(t, p, url.Values{"topicId": {f.topicID}, "cursor": {timelineCursor}})
	require.Len(t, remainder, 3)
	require.Equal(t, "issue_session_dispatch/"+f.binding.StartEventID, remainder[0].ID)
	require.Equal(t, "issue_created/"+f.issue.ID, remainder[1].ID)
	require.Equal(t, "router_routed/"+f.routedEventID, remainder[2].ID)

	all, _ := conversationPage(t, p, url.Values{"topicId": {f.topicID}})
	require.Len(t, all, 5)
	require.Equal(t, append(first, remainder...), all[1:])
	want := conversationRow{
		ID: "report/" + report.ID, TopicID: f.topicID, Kind: "report",
		Title: f.issue.Title, Actor: "issue", Recipient: "topic", At: report.CreatedAt, ReportID: report.ID,
		IssueID: f.issue.ID, SessionBindingID: f.binding.ID,
		ReportKind: f.report.Kind, Summary: f.report.Summary, EventID: report.EventID,
	}
	require.Equal(t, want, all[0])
	filtered, _ := conversationPage(t, p, url.Values{"topicId": {f.topicID}, "filter": {"report"}})
	require.Equal(t, []conversationRow{want}, filtered)
	linked, _ := conversationPage(t, p, url.Values{"topicId": {f.topicID}, "eventId": {want.ID}})
	require.Equal(t, filtered, linked)
}
