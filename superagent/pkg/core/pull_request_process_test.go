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
	require.Equal(t, "router", readRecord[core.Event](t, p, "events/"+review.EventID).TargetRole)
	original := readPRThread(t, p, review.ThreadKey)
	ref, err := core.ParseThreadKey(review.ThreadKey)
	require.NoError(t, err)
	require.Equal(t, original.Ref, ref)
	require.Equal(t, firstPR, ref.ID)
	r := newRoute("create-first-topic", []string{review.MessageID}, f.dir)
	decision := mcpWork[core.RouteRequest](t, router, "route_decide", r)
	first := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	primary := startRoleMCP(t, f, first)
	require.Contains(t, string(primary.call("tools/list", struct{}{})["result"]), "pr_register")
	require.NotContains(t, string(router.call("tools/list", struct{}{})["result"]), "pr_register")
	register := core.RegisterPRRequest{OperationID: "first-register", URL: firstPRAlias}
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/pr_register", roleToken(t, f, routerBinding), register)
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	before := page(t, p, "subscriptions?threadKey="+url.QueryEscape(firstPR))
	require.Len(t, before.Items, 1)
	association := mcpWork[core.PullRequest](t, primary, "pr_register", register)
	require.Equal(t, firstPR, association.CanonicalURL)
	require.Equal(t, first.OwnerID, association.TopicID)
	require.Empty(t, association.IssueID)
	require.Empty(t, association.ExecutionID)
	require.Equal(t, original, readPRThread(t, p, firstPR))
	require.Equal(t, before, page(t, p, "subscriptions?threadKey="+url.QueryEscape(firstPR)))
	var sub core.Subscription
	require.NoError(t, json.Unmarshal(before.Items[0], &sub))
	require.Equal(t, sub.ID, association.SubscriptionID)
	register.URL = firstPR
	require.Equal(t, association, mcpWork[core.PullRequest](t, primary, "pr_register", register))
	register.OperationID = "same-association"
	require.Equal(t, association, mcpWork[core.PullRequest](t, primary, "pr_register", register))
	register.URL = secondPR
	mcpWorkError(t, primary, "pr_register", "conflict", register)
	require.Len(t, page(t, p, "threads").Items, 1)

	work := issueInput(f.dir, "start-worker")
	work.Start = true
	current := mcpWork[core.WorkReceipt](t, primary, "issue_create", work)
	workerBinding := waitBinding(t, p, "worker", current.Execution.ID, "ready")
	worker := startRoleMCP(t, f, workerBinding)
	require.Contains(t, string(worker.call("tools/list", struct{}{})["result"]), "pr_register")
	report := core.ReportRequest{OperationID: "url-in-report", Kind: "ready", Summary: "See " + secondPR, Evidence: []string{secondPR}}
	mcpWork[core.Report](t, worker, "report_submit", report)
	require.Len(t, page(t, p, "pull-requests").Items, 1)
	require.Len(t, page(t, p, "threads").Items, 1)
	workerRequest := core.RegisterPRRequest{OperationID: "worker-register", URL: secondPR}
	mcpWorkError(t, worker, "pr_register", "invalid_argument", core.RegisterPRRequest{OperationID: "scope-override", URL: secondPR, IssueID: current.Issue.ID})
	mcpWorkError(t, primary, "pr_register", "invalid_argument", map[string]string{
		"operationId": "scope-override", "url": secondPR, "executionId": current.Execution.ID,
	})
	workerPR := mcpWork[core.PullRequest](t, worker, "pr_register", workerRequest)
	require.Equal(t, first.OwnerID, workerPR.TopicID)
	require.Equal(t, current.Issue.ID, workerPR.IssueID)
	require.Equal(t, current.Execution.ID, workerPR.ExecutionID)
	require.NotEqual(t, firstPR, workerPR.ThreadKey)
	empty := readPRThread(t, p, secondPR)
	require.Empty(t, empty.LastMessageAt)
	require.Empty(t, page(t, p, "messages?threadKey="+url.QueryEscape(secondPR)).Items)
	merged := prMessage("merged", secondPR+"#pullrequestreview-987", "Pull request merged")
	accepted := acceptPRMessage(t, p, token, merged)
	require.Equal(t, workerPR.ThreadKey, accepted.ThreadKey)
	require.Equal(t, first.OwnerID, readRecord[core.Event](t, p, "events/"+accepted.EventID).TargetID)
	after := readPRThread(t, p, secondPR)
	require.Equal(t, empty.CreatedAt, after.CreatedAt)
	require.Equal(t, accepted.AcceptedAt, after.LastMessageAt)
	merged.Message.ThreadRef.ID = secondPR
	require.Equal(t, accepted, acceptPRMessage(t, p, token, merged))
	merged.OperationID = "native-duplicate"
	duplicate := acceptPRMessage(t, p, token, merged)
	require.True(t, duplicate.Duplicate)
	require.Equal(t, accepted.MessageID, duplicate.MessageID)
	merged.Message.ThreadRef.ID = firstPR
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", token, merged)
	errorCode(t, status, data, http.StatusConflict, "conflict")
	require.Equal(t, current.Issue.Status, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+first.OwnerID).Status)

	issuePR := mcpWork[core.PullRequest](t, primary, "pr_register",
		core.RegisterPRRequest{OperationID: "same-issue", IssueID: current.Issue.ID, URL: secondPR})
	require.Empty(t, issuePR.ExecutionID)
	require.Equal(t, workerPR.SubscriptionID, issuePR.SubscriptionID)
	require.Len(t, page(t, p, "pull-requests?issueId="+current.Issue.ID).Items, 2)
	require.Len(t, mcpWork[core.Page](t, primary, "work_query",
		core.QueryRequest{Collection: "pull-requests", IssueID: current.Issue.ID}).Items, 2)
	for _, raw := range []string{
		"/change/42", "https://me:secret@internal.invalid/42", "http://:80/42",
		"https://github.com/Example/Alpha/pull/42/files", firstPR + "#other-anchor",
		firstPR + "?unknown=1", "https://short.example.invalid/x?change=%",
	} {
		mcpWorkError(t, primary, "pr_register", "invalid_argument", core.RegisterPRRequest{OperationID: "invalid", URL: raw})
	}
	internal := mcpWork[core.PullRequest](t, primary, "pr_register",
		core.RegisterPRRequest{OperationID: "internal-register", URL: internalPR})
	require.Equal(t, internalPR, internal.CanonicalURL)
	internalMessage := acceptPRMessage(t, p, token, prMessage("internal", internalPR, "Internal review"))
	require.Equal(t, internal.ThreadKey, internalMessage.ThreadKey)
	require.Equal(t, internalPR, readPRThread(t, p, internal.ThreadKey).Ref.ID)

	seed := submit(t, p, f, input("independent-topic"))
	d := mcpWork[core.RouteRequest](t, router, "route_decide", newRoute("create-second-topic", []string{seed.MessageID}, f.dir))
	second := waitBinding(t, p, "topic", d.Targets[0].TopicID, "ready")
	foreign := startRoleMCP(t, f, second)
	mcpWorkError(t, foreign, "pr_register", "not_found", core.RegisterPRRequest{
		OperationID: "foreign-issue", IssueID: current.Issue.ID, URL: secondPR,
	})
	mcpWorkError(t, foreign, "work_query", "not_found", core.QueryRequest{Collection: "pull-requests", ID: workerPR.ID})
	shared := mcpWork[core.PullRequest](t, foreign, "pr_register", core.RegisterPRRequest{OperationID: "shared-pr", URL: secondPR})
	require.Equal(t, workerPR.ThreadKey, shared.ThreadKey)
	require.NotEqual(t, workerPR.SubscriptionID, shared.SubscriptionID)
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 2)

	multi := acceptPRMessage(t, p, token, prMessage("multi", secondPR, "Pull request closed"))
	event := readRecord[core.Event](t, p, "events/"+multi.EventID)
	require.Equal(t, "router", event.TargetRole)
	require.Empty(t, event.TargetID)
	submission := mcpWork[core.Page](t, foreign, "source_query", core.QueryRequest{Collection: "messages", ThreadKey: secondPR})
	require.Empty(t, submission.Items)
	require.Equal(t, current.Issue.Status, readRecord[core.Issue](t, p, "issues/"+current.Issue.ID).Status)
	mcpWork[core.WorkReceipt](t, primary, "issue_cancel", issueLifecycle("retire-pr-worker", current.Issue))
	mcpWorkError(t, worker, "pr_register", "stale_binding", workerRequest)
	mcpWorkError(t, worker, "pr_register", "stale_binding", core.RegisterPRRequest{OperationID: "retired-write", URL: internalPR})
	report.OperationID = "historical"
	require.True(t, mcpWork[core.Report](t, worker, "report_submit", report).Historical)
	require.Equal(t, workerPR, readRecord[core.PullRequest](t, p, "pull-requests/"+workerPR.ID))
	require.Len(t, page(t, p, "subscriptions?threadKey="+url.QueryEscape(secondPR)).Items, 2)
	p.stop(t, false)
	p = f.start()
	require.Equal(t, workerPR, readRecord[core.PullRequest](t, p, "pull-requests/"+workerPR.ID))
	require.Equal(t, association, mcpWork[core.PullRequest](t, primary, "pr_register",
		core.RegisterPRRequest{OperationID: "first-register", URL: firstPRAlias}))
	t.Log("Public Message and stdio MCP agreed on both arrival orders, scoped deduplication, worker authority and shared-Thread routing.")
}
