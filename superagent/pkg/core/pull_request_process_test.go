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

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/pkg/core"
)

const (
	firstPR      = "https://github.com/example/alpha/pull/42"
	firstPRAlias = "https://GitHub.COM/Example/Alpha/pull/0042/?utm_source=fixture#discussion_r1234"
	secondPR     = "https://github.com/example/beta/pull/42"
	internalPR   = "https://review.example.invalid/code/Repo%2FSDK?change=42&next=a%2fb&utm_source=identity#review%2Fversion"
)

func addPRSource(t *testing.T, f *fixture, source string) string {
	t.Helper()
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	token := strings.Repeat("p", 48) + source
	path := filepath.Join(f.dir, source+".token")
	write(t, path, token)
	write(t, f.config, strings.Replace(string(config), "sources:\n",
		"sources:\n  - source: "+source+"\n    accountId: "+source+"-account\n    tokenFile: "+path+
			"\n    threadIdPrefixes: [https://github.com/, https://GitHub.COM/, https://review.example.invalid/]\n", 1))
	return token
}

func prMessage(id, resourceURL, body string) core.SubmitTriggerMessageRequest {
	return core.SubmitTriggerMessageRequest{OperationID: "op-" + id, Message: core.TriggerMessage{
		SourceMessageID: id, ThreadID: resourceURL, Content: []events.ContentBlock{events.Text(body)},
	}}
}

func acceptPRMessage(t *testing.T, p *process, token string, input core.SubmitTriggerMessageRequest) core.Receipt {
	t.Helper()
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/core/trigger-messages", token, input)
	require.Equal(t, http.StatusOK, status, string(data))
	var receipt core.Receipt
	require.NoError(t, json.Unmarshal(data, &receipt))
	return receipt
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
	token := addPRSource(t, f, "github")
	otherSource := addPRSource(t, f, "antcode")
	f.execution(boundary)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	review := acceptPRMessage(t, p, token, prMessage("review-first", firstPR, "Review requested"))
	require.Equal(t, firstPR, review.ThreadID)
	require.Equal(t, "router", readRecord[observedRoutingEvent](t, p, "internal-events/"+review.EventID).Recipient.Kind)
	original := readThread(t, p, review.ThreadID)
	require.Equal(t, firstPR, original.ID)
	r := newRoute("create-first-topic", []string{review.MessageID}, f.dir)
	decision := mcpWork[core.RouteRequest](t, router, "route_messages", r)
	first := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	primary := startRoleMCP(t, f, first)
	require.Contains(t, string(primary.call("tools/list", struct{}{})["result"]), "update_topic")
	require.NotContains(t, string(router.call("tools/list", struct{}{})["result"]), "update_topic")
	register := prUpdate("first-register", 1, firstPR)
	status, data := requestMCPTool(t, p.endpoint, "update_topic", roleToken(t, f, routerBinding), register)
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	before := readThread(t, p, firstPR)
	require.Len(t, before.Subscriptions, 1)
	registered := mcpWork[core.Topic](t, primary, "update_topic", register)
	require.Len(t, registered.PullRequests, 1)
	association := registered.PullRequests[0].PullRequest
	require.Equal(t, firstPR, association.CanonicalURL)
	require.Equal(t, first.OwnerID, association.TopicID)
	require.Empty(t, association.IssueID)
	require.Empty(t, association.SessionBindingID)
	require.Equal(t, original.Thread, before.Thread)
	require.Equal(t, before, readThread(t, p, firstPR))
	require.Equal(t, before.Subscriptions[0].ID, association.SubscriptionID)
	mcpWorkError(t, primary, "update_topic", "conflict", prUpdate("first-register", 1, firstPRAlias))
	require.Equal(t, registered, mcpWork[core.Topic](t, primary, "update_topic", prUpdate("same-association", registered.Revision, firstPR)))
	require.Len(t, page(t, p, "threads").Items, 1)

	work := issueInput(f.dir, "start-issue")
	work.Start = true
	current := mcpWork[core.WorkReceipt](t, primary, "create_issue", work)
	issueBinding := waitBinding(t, p, "issue", current.SessionBinding.ID, "ready")
	issue := startRoleMCP(t, f, issueBinding)
	require.Contains(t, string(issue.call("tools/list", struct{}{})["result"]), "update_issue")
	require.Equal(t, registered.PullRequests, readRecord[core.Topic](t, p, "topics/"+first.OwnerID).PullRequests)
	require.Empty(t, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests)
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
	require.NotEqual(t, firstPR, issuePR.ThreadID)
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

	empty := readThread(t, p, secondPR)
	require.Empty(t, empty.LastMessageAt)
	require.Empty(t, page(t, p, "trigger-messages?threadId="+url.QueryEscape(secondPR)).Items)
	merged := prMessage("merged", secondPR, "Pull request merged\n"+secondPR+"#pullrequestreview-987")
	merged.Message.Meta = json.RawMessage(`{"state":"merged","changes":["merged"],"draft":false,"observationVersion":5}`)
	accepted := acceptPRMessage(t, p, token, merged)
	require.Equal(t, issuePR.ThreadID, accepted.ThreadID)
	direct := waitRouteStatus(t, p, accepted.EventID, "sent")
	require.Equal(t, "core", direct.Sender.Kind)
	require.Equal(t, "routed_message", direct.Kind)
	require.Equal(t, first.OwnerID, direct.Recipient.TopicID)
	delivered := mcpWork[core.Message](t, primary, "get_message", map[string]string{"messageId": accepted.MessageID})
	require.Equal(t, merged.Message.Content, delivered.Content)
	require.Empty(t, delivered.TriggerMeta)
	sharedMessage := acceptPRMessage(t, p, otherSource, merged)
	require.NotEqual(t, accepted.MessageID, sharedMessage.MessageID)
	require.Equal(t, issuePR.ThreadID, sharedMessage.ThreadID)
	require.Equal(t, first.OwnerID, waitRouteStatus(t, p, sharedMessage.EventID, "sent").Recipient.TopicID)
	after := readThread(t, p, secondPR)
	require.Equal(t, empty.CreatedAt, after.CreatedAt)
	require.Equal(t, sharedMessage.AcceptedAt, after.LastMessageAt)
	replay := accepted
	replay.Duplicate = true
	require.Equal(t, replay, acceptPRMessage(t, p, token, merged))
	merged.OperationID = "native-duplicate"
	duplicate := acceptPRMessage(t, p, token, merged)
	require.True(t, duplicate.Duplicate)
	require.Equal(t, accepted.MessageID, duplicate.MessageID)
	merged.Message.ThreadID = firstPR
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/core/trigger-messages", token, merged)
	errorCode(t, status, data, http.StatusConflict, "conflict")
	require.Equal(t, current.Issue.Status, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+first.OwnerID).Status)

	sameIssue := prUpdate("same-issue", linked.Issue.Revision, secondPR)
	sameIssue["issueId"] = current.Issue.ID
	sameIssuePR := mcpWork[core.WorkReceipt](t, primary, "update_issue", sameIssue).Issue.PullRequests[0].PullRequest
	require.Equal(t, issuePR, sameIssuePR)
	require.Len(t, readThread(t, p, secondPR).Subscriptions, 1)
	expanded := mcpWork[core.Issue](t, primary, "get_issue", map[string]any{
		"issueId": current.Issue.ID, "include": []string{"pullRequests"},
	})
	require.Len(t, expanded.PullRequests, 1)
	require.Equal(t, expanded.PullRequests, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests)
	require.Equal(t, topicWork.PullRequests, readRecord[core.Topic](t, p, "topics/"+first.OwnerID).PullRequests)

	report := core.ReportRequest{OperationID: "url-in-report", Kind: "ready", Summary: "See " + internalPR, Evidence: []string{internalPR}, GoalRevision: current.SessionBinding.StartGoalRevision}
	mcpWork[core.Report](t, issue, "submit_report", report)
	require.Len(t, page(t, p, "threads").Items, 2)
	require.Equal(t, expanded.PullRequests, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests)
	require.Equal(t, topicWork.PullRequests, readRecord[core.Topic](t, p, "topics/"+first.OwnerID).PullRequests)
	for _, raw := range []string{
		"/change/42", "https://me:secret@internal.invalid/42", "http://:80/42",
		"https://short.example.invalid/x?change=%", firstPR + " ",
	} {
		mcpWorkError(t, primary, "update_topic", "invalid_argument", prUpdate("invalid", topicWork.Revision, raw))
	}
	withInternal := mcpWork[core.Topic](t, primary, "update_topic", prUpdate("internal-register", topicWork.Revision, internalPR))
	internal := withInternal.PullRequests[2].PullRequest
	require.Equal(t, internalPR, internal.CanonicalURL)
	internalMessage := acceptPRMessage(t, p, token, prMessage("internal", internalPR, "Internal review"))
	require.Equal(t, internal.ThreadID, internalMessage.ThreadID)
	require.Equal(t, internalPR, readThread(t, p, internal.ThreadID).ID)
	saved := readRecord[core.Topic](t, p, "topics/"+first.OwnerID)
	require.Equal(t, withInternal.PullRequests, saved.PullRequests)
	require.Equal(t, topicWork.PullRequests, saved.PullRequests[:2])

	alias := mcpWork[core.Topic](t, primary, "update_topic", prUpdate("exact-pr-key", withInternal.Revision, firstPRAlias)).PullRequests[3].PullRequest
	require.Equal(t, firstPRAlias, alias.CanonicalURL)
	require.Equal(t, firstPRAlias, alias.ThreadID)
	literal := acceptPRMessage(t, p, token, prMessage("exact-key", firstPRAlias, "Fixture literal key"))
	require.Equal(t, firstPRAlias, literal.ThreadID)
	require.Equal(t, firstPRAlias, readThread(t, p, firstPRAlias).ID)
	require.Equal(t, first.OwnerID, waitRouteStatus(t, p, literal.EventID, "sent").Recipient.TopicID)

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
	require.Equal(t, issuePR.ThreadID, shared.ThreadID)
	require.NotEqual(t, issuePR.SubscriptionID, shared.SubscriptionID)
	require.Len(t, readThread(t, p, secondPR).Subscriptions, 2)

	multi := acceptPRMessage(t, p, token, prMessage("multi", secondPR, "Pull request closed"))
	event := waitRouteStatus(t, p, multi.EventID, "sent")
	require.Equal(t, "router", event.Recipient.Kind)
	require.Empty(t, event.Recipient.TopicID)
	submission := mcpWork[core.Page](t, foreign, "list_messages", map[string]any{"threadId": secondPR})
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
	require.Len(t, readThread(t, p, secondPR).Subscriptions, 2)
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
	require.Equal(t, expanded.PullRequests, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests)
	require.Len(t, readThread(t, p, secondPR).Subscriptions, 2)
	p.stop(t, false)
	p = f.start()
	require.Equal(t, expanded.PullRequests, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).PullRequests)
	mcpWorkError(t, primary, "update_topic", "conflict", register)
	t.Log("Public Message and HTTP MCP agreed on both arrival orders, scoped deduplication, issue authority and shared-Thread routing.")
}
