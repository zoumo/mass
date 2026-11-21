package core_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	firstPR      = "https://github.com/example/alpha/pull/42"
	firstPRAlias = "https://GitHub.COM/Example/Alpha/pull/0042/?utm_source=fixture#discussion_r1234"
	secondPR     = "https://github.com/example/beta/pull/42"
	internalPR   = "https://review.example.invalid/code/Repo%2FSDK?change=42&next=a%2fb&utm_source=identity#review%2Fversion"
)

func addPRSource(t *testing.T, f *fixture) string {
	t.Helper()
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	token := strings.Repeat("p", 48)
	path := filepath.Join(f.dir, "pr-fixture.token")
	write(t, path, token)
	write(t, f.config, strings.Replace(string(config), "sources:\n",
		"sources:\n  - source: review-fixture\n    accountId: review-account\n    tokenFile: "+path+"\n", 1))
	return token
}

func prMessage(id, resourceURL, body string) core.SubmitRequest {
	return core.SubmitRequest{OperationID: "op-" + id, Message: core.MessageInput{
		Source: "review-fixture", AccountID: "review-account", SourceMessageID: id,
		ThreadRef: core.ThreadRef{Source: "pr:url", ID: resourceURL}, Actor: "fixture-collector", Text: body,
	}}
}

func acceptPRMessage(t *testing.T, p *process, token string, input core.SubmitRequest) core.Receipt {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", token, input)
	require.Equal(t, http.StatusOK, status, string(data))
	var receipt core.Receipt
	require.NoError(t, json.Unmarshal(data, &receipt))
	return receipt
}

func readPRThread(t *testing.T, p *process, key string) core.Thread {
	t.Helper()
	return readRecord[core.Thread](t, p, "threads/"+url.PathEscape(key))
}

func prUpdate(operationID string, expectedRevision int, rawURL string) map[string]any {
	return map[string]any{
		"operationId": operationID, "expectedRevision": expectedRevision,
		"pullRequests": map[string][]string{"add": {rawURL}},
	}
}

func TestPullRequestRegistrationAndSourceRouting(t *testing.T) {
	t.Parallel()
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	token := addPRSource(t, f)
	f.execution(boundary)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	review := acceptPRMessage(t, p, token, prMessage("review-first", firstPRAlias, "Review requested"))
	require.Equal(t, firstPR, review.ThreadKey)
	require.Equal(t, "router", readRecord[observedRoutingEvent](t, p, "routing-events/"+review.EventID).Recipient.Kind)
	original := readPRThread(t, p, review.ThreadKey)
	ref, err := core.ParseThreadKey(review.ThreadKey)
	require.NoError(t, err)
	require.Equal(t, original.Ref, ref)
	require.Equal(t, firstPR, ref.ID)
	r := newRoute("create-first-topic", []string{review.MessageID}, f.dir)
	decision := mcpWork[core.RouteRequest](t, router, "route_messages", r)
	first := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	primary := startRoleMCP(t, f, first)
	require.Contains(t, string(primary.call("tools/list", struct{}{})["result"]), "update_topic")
	require.NotContains(t, string(router.call("tools/list", struct{}{})["result"]), "update_topic")
	register := prUpdate("first-register", 1, firstPRAlias)
	status, data := requestMCPTool(t, p.endpoint, "update_topic", roleToken(t, f, routerBinding), register)
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	before := page(t, p, "subscriptions?threadKey="+url.QueryEscape(firstPR))
	require.Len(t, before.Items, 1)
	registered := mcpWork[core.Topic](t, primary, "update_topic", register)
	require.Len(t, registered.PullRequests, 1)
	association := registered.PullRequests[0].PullRequest
	require.Equal(t, firstPR, association.CanonicalURL)
	require.Equal(t, first.OwnerID, association.TopicID)
	require.Empty(t, association.IssueID)
	require.Empty(t, association.SessionBindingID)
	require.Equal(t, original, readPRThread(t, p, firstPR))
	require.Equal(t, before, page(t, p, "subscriptions?threadKey="+url.QueryEscape(firstPR)))
	var sub core.Subscription
	require.NoError(t, json.Unmarshal(before.Items[0], &sub))
	require.Equal(t, sub.ID, association.SubscriptionID)
	mcpWorkError(t, primary, "update_topic", "conflict", prUpdate("first-register", 1, firstPR))
	require.Equal(t, registered, mcpWork[core.Topic](t, primary, "update_topic", prUpdate("same-association", registered.Revision, firstPR)))
	require.Len(t, page(t, p, "threads").Items, 1)

	work := issueInput(f.dir, "start-issue")
	work.Start = true
	current := mcpWork[core.WorkReceipt](t, primary, "create_issue", work)
	issueBinding := waitBinding(t, p, "issue", current.SessionBinding.ID, "ready")
	issue := startRoleMCP(t, f, issueBinding)
	require.Contains(t, string(issue.call("tools/list", struct{}{})["result"]), "update_issue")
	require.Len(t, page(t, p, "pull-requests").Items, 1)
	require.Len(t, page(t, p, "threads").Items, 1)
	issueRequest := prUpdate("issue-register", current.Issue.Revision, secondPR)
	forged := prUpdate("scope-override", current.Issue.Revision, secondPR)
	forged["issueId"] = current.Issue.ID
	mcpWorkError(t, issue, "update_issue", "invalid_argument", forged)
	forged = prUpdate("scope-override", registered.Revision, secondPR)
	forged["sessionBindingId"] = current.SessionBinding.ID
	mcpWorkError(t, primary, "update_topic", "invalid_argument", forged)
	linked := mcpWork[core.WorkReceipt](t, issue, "update_issue", issueRequest)
	issuePR := linked.Issue.PullRequests[0].PullRequest
	require.Equal(t, first.OwnerID, issuePR.TopicID)
	require.Equal(t, current.Issue.ID, issuePR.IssueID)
	require.Equal(t, current.SessionBinding.ID, issuePR.SessionBindingID)
	require.NotEqual(t, firstPR, issuePR.ThreadKey)
	topicWork := mcpWork[core.Topic](t, primary, "get_topic", map[string][]string{"include": {"pullRequests"}})
	require.Len(t, topicWork.PullRequests, 2)
	parentPR := topicWork.PullRequests[1].PullRequest
	require.Equal(t, secondPR, parentPR.CanonicalURL)
	require.Empty(t, parentPR.IssueID)
	require.Equal(t, issuePR.SubscriptionID, parentPR.SubscriptionID)
	require.Equal(t, parentPR, mcpWork[core.Topic](t, primary, "update_topic",
		prUpdate("same-topic-url", topicWork.Revision, secondPR)).PullRequests[1].PullRequest)
	require.Equal(t, issuePR, mcpWork[core.WorkReceipt](t, issue, "update_issue",
		prUpdate("same-issue-url", linked.Issue.Revision, secondPR)).Issue.PullRequests[0].PullRequest)

	empty := readPRThread(t, p, secondPR)
	require.Empty(t, empty.LastMessageAt)
	require.Empty(t, page(t, p, "messages?threadKey="+url.QueryEscape(secondPR)).Items)
	merged := prMessage("merged", secondPR+"#pullrequestreview-987", "Pull request merged")
	merged.Message.Meta = map[string]any{"state": "merged", "changes": []string{"merged"}, "draft": false, "observationVersion": 5}
	accepted := acceptPRMessage(t, p, token, merged)
	require.Equal(t, issuePR.ThreadKey, accepted.ThreadKey)
	direct := waitRouteStatus(t, p, accepted.EventID, "sent")
	require.Equal(t, "core", direct.Sender.Kind)
	require.Equal(t, "routed_message", direct.Kind)
	require.Equal(t, first.OwnerID, direct.Recipient.TopicID)
	delivered := mcpWork[core.Message](t, primary, "get_message", map[string]string{"messageId": accepted.MessageID})
	require.Equal(t, merged.Message.Text, delivered.Text)
	require.Equal(t, map[string]any{"state": "merged", "changes": []any{"merged"}, "draft": false}, delivered.Meta)
	after := readPRThread(t, p, secondPR)
	require.Equal(t, empty.CreatedAt, after.CreatedAt)
	require.Equal(t, accepted.AcceptedAt, after.LastMessageAt)
	merged.Message.ThreadRef.ID = secondPR
	replay := accepted
	replay.Duplicate = true
	require.Equal(t, replay, acceptPRMessage(t, p, token, merged))
	merged.OperationID = "native-duplicate"
	duplicate := acceptPRMessage(t, p, token, merged)
	require.True(t, duplicate.Duplicate)
	require.Equal(t, accepted.MessageID, duplicate.MessageID)
	merged.Message.ThreadRef.ID = firstPR
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", token, merged)
	errorCode(t, status, data, http.StatusConflict, "conflict")
	require.Equal(t, current.Issue.Status, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+first.OwnerID).Status)

	sameIssue := prUpdate("same-issue", linked.Issue.Revision, secondPR)
	sameIssue["issueId"] = current.Issue.ID
	sameIssuePR := mcpWork[core.WorkReceipt](t, primary, "update_issue", sameIssue).Issue.PullRequests[0].PullRequest
	require.Equal(t, issuePR, sameIssuePR)
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 1)
	require.Len(t, page(t, p, "pull-requests?issueId="+current.Issue.ID).Items, 1)
	expanded := mcpWork[core.Issue](t, primary, "get_issue", map[string]any{
		"issueId": current.Issue.ID, "include": []string{"pullRequests"},
	})
	require.Len(t, expanded.PullRequests, 1)
	require.Equal(t, issuePR, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests[0].PullRequest)
	require.Equal(t, topicWork.PullRequests, readRecord[core.Topic](t, p, "topics/"+first.OwnerID).PullRequests)

	report := core.ReportRequest{OperationID: "url-in-report", Kind: "ready", Summary: "See " + internalPR, Evidence: []string{internalPR}, GoalRevision: current.SessionBinding.StartGoalRevision}
	mcpWork[core.Report](t, issue, "submit_report", report)
	require.Len(t, page(t, p, "threads").Items, 2)
	require.Len(t, page(t, p, "pull-requests").Items, 3)
	for _, raw := range []string{
		"/change/42", "https://me:secret@internal.invalid/42", "http://:80/42",
		"https://github.com/Example/Alpha/pull/42/files", firstPR + "#other-anchor",
		firstPR + "?unknown=1", "https://short.example.invalid/x?change=%",
	} {
		mcpWorkError(t, primary, "update_topic", "invalid_argument", prUpdate("invalid", topicWork.Revision, raw))
	}
	topicPRQuery := "pull-requests?topicId=" + first.OwnerID + "&limit=1"
	firstLink := page(t, p, topicPRQuery)
	require.Len(t, firstLink.Items, 1)
	require.NotEmpty(t, firstLink.NextCursor)
	internal := mcpWork[core.Topic](t, primary, "update_topic",
		prUpdate("internal-register", topicWork.Revision, internalPR)).PullRequests[2].PullRequest
	require.Equal(t, internalPR, internal.CanonicalURL)
	internalMessage := acceptPRMessage(t, p, token, prMessage("internal", internalPR, "Internal review"))
	require.Equal(t, internal.ThreadKey, internalMessage.ThreadKey)
	require.Equal(t, internalPR, readPRThread(t, p, internal.ThreadKey).Ref.ID)
	snapshot := page(t, p, topicPRQuery+"&cursor="+url.QueryEscape(firstLink.NextCursor))
	require.Len(t, snapshot.Items, 1)
	require.Empty(t, snapshot.NextCursor)
	var savedParent core.PullRequest
	require.NoError(t, json.Unmarshal(snapshot.Items[0], &savedParent))
	require.Equal(t, parentPR, savedParent)

	seed := submit(t, p, f, input("independent-topic"))
	d := mcpWork[core.RouteRequest](t, router, "route_messages", newRoute("create-second-topic", []string{seed.MessageID}, f.dir))
	second := waitBinding(t, p, "topic", d.Targets[0].TopicID, "ready")
	foreign := startRoleMCP(t, f, second)
	foreignPR := prUpdate("foreign-issue", linked.Issue.Revision, secondPR)
	foreignPR["issueId"] = current.Issue.ID
	mcpWorkError(t, foreign, "update_issue", "not_found", foreignPR)
	mcpWorkError(t, foreign, "get_issue", "not_found", map[string]any{
		"issueId": current.Issue.ID, "include": []string{"pullRequests"},
	})
	shared := mcpWork[core.Topic](t, foreign, "update_topic", prUpdate("shared-pr", 1, secondPR)).PullRequests[0].PullRequest
	require.Equal(t, issuePR.ThreadKey, shared.ThreadKey)
	require.NotEqual(t, issuePR.SubscriptionID, shared.SubscriptionID)
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 2)

	multi := acceptPRMessage(t, p, token, prMessage("multi", secondPR, "Pull request closed"))
	event := waitRouteStatus(t, p, multi.EventID, "sent")
	require.Equal(t, "router", event.Recipient.Kind)
	require.Empty(t, event.Recipient.TopicID)
	submission := mcpWork[core.Page](t, foreign, "list_messages", map[string]any{"threadKey": secondPR})
	require.Empty(t, submission.Items)
	mcpWork[core.RouteDecision](t, router, "route_messages", core.RouteRequest{
		OperationID: "route-pr-status",
		MessageIDs:  []string{multi.MessageID},
		Rounds:      map[string]int{multi.MessageID: 1},
		Kind:        "route",
		Reason:      "Both Topics need the PR status",
		Targets: []core.RouteTarget{
			{TopicID: first.OwnerID, MessageIDs: []string{multi.MessageID}},
			{TopicID: second.OwnerID, MessageIDs: []string{multi.MessageID}},
		},
	})
	waitDelivered(t, p, multi.MessageID, "topic", first.OwnerID)
	waitDelivered(t, p, multi.MessageID, "topic", second.OwnerID)
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 2)
	require.Equal(t, current.Issue.Status, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+first.OwnerID).Status)
	completion := mcpWork[core.WorkReceipt](t, primary, "issue_complete", core.CompleteIssueRequest{
		IssueID: current.Issue.ID, CompleteRequest: acceptance("accept-pr-issue", linked.Issue.Revision, multi.MessageID),
	})
	require.Equal(t, "completed", completion.Issue.Status)
	mcpWorkError(t, issue, "update_issue", "stale_binding", issueRequest)
	mcpWorkError(t, issue, "update_issue", "stale_binding", prUpdate("retired-write", linked.Issue.Revision, internalPR))
	report.OperationID = "historical"
	require.True(t, mcpWork[core.Report](t, issue, "submit_report", report).Historical)
	require.Equal(t, issuePR, readRecord[core.PullRequest](t, p, "pull-requests/"+issuePR.ID))
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 2)
	p.stop(t, false)
	p = f.start()
	require.Equal(t, issuePR, readRecord[core.PullRequest](t, p, "pull-requests/"+issuePR.ID))
	require.Equal(t, issuePR, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests[0].PullRequest)
	mcpWorkError(t, primary, "update_topic", "conflict", register)
	t.Log("Public Message and HTTP MCP agreed on both arrival orders, scoped deduplication, issue authority and shared-Thread routing.")
}
