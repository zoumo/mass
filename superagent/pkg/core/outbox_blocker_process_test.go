package core_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type quotedReplyRecord struct {
	QuotedReply *struct {
		OutboxID   string
		BlockerRef core.BlockerRef
	} `json:"quotedReply"`
}

func TestOutboxQuotedRepliesAddressOneBlockerAndResumeCurrentSessionBinding(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	_, otherSource, _ := addOutboxPeerSources(t, f)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	origin := input("questions-origin")
	source := submit(t, p, f, origin)
	route := decide(t, p, routerToken, newRoute("questions-topic", []string{source.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	role := roleToken(t, f, topic)
	create := issueInput(f.dir, "question-a")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, role, "create_issue", create)
	waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	second := roleWork[core.WorkReceipt](t, p, role, "create_issue", issueInput(f.dir, "question-b")).Issue
	add := func(issue core.Issue, operation string, reasons ...core.BlockerInput) core.Issue {
		return roleWork[core.WorkReceipt](t, p, role, "issue_update_blockers", core.UpdateBlockersRequest{
			OperationID: operation, IssueID: issue.ID, ExpectedRevision: issue.Revision, Add: reasons,
		}).Issue
	}
	a := add(work.Issue, "wait-a", human("Confirm the region"), human("Confirm the date"))
	b := add(second, "wait-b", human("Confirm the owner"))
	require.Equal(t, "mass-t1-i1-b1", a.Blockers[0].ID)
	require.Equal(t, "mass-t1-i1-b2", a.Blockers[1].ID)
	require.Equal(t, "mass-t1-i2-b1", b.Blockers[0].ID)
	ask := func(issue core.Issue, blocker core.Blocker, id string) core.OutboxItem {
		ref := &core.BlockerRef{IssueID: issue.ID, BlockerID: blocker.ID}
		req := core.ReplyRequest{OperationID: "ask-" + id, MessageID: source.MessageID, Content: markdownReply(blocker.Reason), BlockerRef: ref}
		item := roleWork[core.OutboxItem](t, p, role, "send_reply", req)
		require.Equal(t, ref, item.BlockerRef)
		return item
	}
	region := ask(a, a.Blockers[0], "a1")
	date := ask(a, a.Blockers[1], "a2")
	owner := ask(b, b.Blockers[0], "b")
	code, raw := requestMCPTool(t, p.endpoint, "send_reply", routerToken,
		core.ReplyRequest{OperationID: "router-blocker", MessageID: source.MessageID, Content: markdownReply("Ask"), BlockerRef: region.BlockerRef})
	errorCode(t, code, raw, http.StatusForbidden, "forbidden")

	markSent := func(item core.OutboxItem) {
		resultOutbox(t, p, f.token, outboxResult(item, "sent", ""))
	}
	markSent(date)
	markSent(owner)
	quote := func(id, outboxID, text string) core.SubmitTriggerMessageRequest {
		msg := input(id)
		msg.Message.Content[0].Text.Text = text
		msg.InReplyToOutboxID = outboxID
		return msg
	}
	match := func(msg core.Receipt) quotedReplyRecord {
		return roleWork[quotedReplyRecord](t, p, role, "get_message", map[string]string{"messageId": msg.MessageID})
	}

	ambiguous := submit(t, p, f, quote("ambiguous", "", "Yes"))
	require.Nil(t, match(ambiguous).QuotedReply)
	partialInput := quote("partial-region", region.ID, "Use east region")
	code, raw = request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", f.token, partialInput)
	errorCode(t, code, raw, http.StatusConflict, "failed_precondition")
	markSent(region)
	partial := submit(t, p, f, partialInput)
	require.NotNil(t, match(partial).QuotedReply)
	require.Equal(t, region.ID, match(partial).QuotedReply.OutboxID)
	require.Equal(t, *region.BlockerRef, match(partial).QuotedReply.BlockerRef)
	list := roleWork[core.Page](t, p, role, "list_messages", map[string]any{"threadKey": source.ThreadKey})
	require.Len(t, list.Items, 3)
	require.Contains(t, string(list.Items[2]), region.ID)

	wrongThread := quote("wrong-thread", date.ID, "Date")
	moveInput(&wrongThread, "elsewhere")
	code, raw = request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", f.token, wrongThread)
	errorCode(t, code, raw, http.StatusConflict, "failed_precondition")
	foreign := quote("foreign-account", date.ID, "Date")
	code, raw = request(t, http.MethodPost, p.endpoint, "/v1/channel/messages", otherSource, foreign)
	errorCode(t, code, raw, http.StatusConflict, "failed_precondition")
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))
	require.Empty(t, workEvents(t, p, "issue_session_update"))

	resolve := func(operation string, issue core.Issue, blockerID string, msg core.Receipt) core.UpdateBlockersRequest {
		return core.UpdateBlockersRequest{
			OperationID: operation, IssueID: issue.ID, ExpectedRevision: issue.Revision,
			Resolve: []core.BlockerResolution{{BlockerID: blockerID, Reason: "Confirmed by the user", MessageID: msg.MessageID}},
		}
	}
	for _, wrong := range []core.UpdateBlockersRequest{
		resolve("wrong-issue", b, b.Blockers[0].ID, partial),
		resolve("wrong-part", a, a.Blockers[1].ID, partial),
	} {
		code, raw = requestMCPTool(t, p.endpoint, "issue_update_blockers", role, wrong)
		errorCode(t, code, raw, http.StatusConflict, "failed_precondition")
	}
	resolved := roleWork[core.WorkReceipt](t, p, role, "issue_update_blockers", resolve("resolve-region", a, a.Blockers[0].ID, partial))
	require.Equal(t, "blocked", resolved.Issue.Status)
	require.Equal(t, a.Blockers[1:], resolved.Issue.Blockers)
	require.Equal(t, partial.MessageID, resolved.Issue.ResolvedBlockers[0].Resolution.MessageID)
	require.Empty(t, resolved.NotificationEventID)
	a = resolved.Issue

	final := submit(t, p, f, quote("date-answer", date.ID, "Tuesday UTC"))
	waitRouteStatus(t, p, final.EventID, "sent")
	require.Equal(t, *date.BlockerRef, match(final).QuotedReply.BlockerRef)
	continueWork := resolve("resolve-date-and-continue", a, a.Blockers[0].ID, final)
	continueWork.NotifyIssueSession = true
	continueWork.UnblockedStatus = "active"
	p.stop(t, true)
	stage := "internal_event_before_settlement"
	p = f.start("SUPERAGENT_TEST_GATE_DIR="+f.dir, "SUPERAGENT_TEST_GATE_STAGE="+stage)
	topic = waitBinding(t, p, "topic", topic.OwnerID, "ready")
	role = roleToken(t, f, topic)
	require.Equal(t, *date.BlockerRef, match(final).QuotedReply.BlockerRef)
	continued := roleWork[core.WorkReceipt](t, p, role, "issue_update_blockers", continueWork)
	require.Equal(t, work.SessionBinding.ID, continued.Issue.CurrentSessionBindingID)
	require.Equal(t, "active", continued.Issue.Status)
	require.Empty(t, continued.Issue.Blockers)
	require.Equal(t, region.BlockerRef.BlockerID, continued.Issue.ResolvedBlockers[0].ID)
	require.Equal(t, date.BlockerRef.BlockerID, continued.Issue.ResolvedBlockers[1].ID)
	require.NotEmpty(t, continued.NotificationEventID)
	waitWorkGate(t, f.dir, stage)
	p.stop(t, true)
	p = f.start()
	status, body := requestMCPTool(t, p.endpoint, "issue_update_blockers", role, continueWork)
	errorCode(t, status, body, http.StatusConflict, "conflict")
	require.Equal(t, continued.Issue, readRecord[core.Issue](t, p, "issues/"+a.ID))
	delivered := waitWorkEvent(t, p, continued.NotificationEventID, "sent")
	require.Equal(t, "issue_session_update", delivered.Kind)
	require.Equal(t, work.SessionBinding.ID, delivered.Recipient.SessionBindingID)
	require.Len(t, workEvents(t, p, "issue_session_update"), 1)
	require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))

	reblocked := add(continued.Issue, "new-region-blocker", human("Confirm the region"))
	require.Equal(t, "mass-t1-i1-b3", reblocked.Blockers[0].ID)
	late := submit(t, p, f, quote("late-region", region.ID, "The region is still east"))
	require.Equal(t, *region.BlockerRef, match(late).QuotedReply.BlockerRef)
	code, raw = requestMCPTool(t, p.endpoint, "issue_update_blockers", role,
		resolve("late-is-old", reblocked, reblocked.Blockers[0].ID, late))
	errorCode(t, code, raw, http.StatusConflict, "failed_precondition")
	require.Equal(t, reblocked, readRecord[core.Issue](t, p, "issues/"+a.ID))
}
