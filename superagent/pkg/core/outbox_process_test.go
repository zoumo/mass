package core_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const channelOutbox = "/v1/channel/outbox"

type outboxDetail struct {
	core.OutboxItem
	Attempts []struct {
		core.OutboxAttempt
		Claims []struct {
			Generation     int64
			Mode           string
			LeaseExpiresAt string
			ReleasedAt     string
		}
	}
}

func configureOutboxSource(t *testing.T, f *fixture) {
	t.Helper()
	cfg, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(cfg), "    tokenFile:", "    instanceId: collector-1\n    tokenFile:", 1))
}

func addOutboxPeerSources(t *testing.T, f *fixture) (sameSource, otherSource, ingressOnly string) {
	t.Helper()
	cfg, err := os.ReadFile(f.config)
	require.NoError(t, err)
	sameSource, otherSource, ingressOnly = strings.Repeat("b", 48), strings.Repeat("c", 48), strings.Repeat("d", 48)
	for name, token := range map[string]string{"same": sameSource, "other": otherSource, "ingress": ingressOnly} {
		write(t, filepath.Join(f.dir, name+".token"), token)
	}
	write(t, f.config, string(cfg)+
		"  - source: dws\n    accountId: account-1\n    instanceId: collector-2\n    tokenFile: ./same.token\n"+
		"  - source: other\n    accountId: account-2\n    instanceId: collector-1\n    tokenFile: ./other.token\n"+
		"  - source: dws\n    accountId: account-1\n    tokenFile: ./ingress.token\n")
	return sameSource, otherSource, ingressOnly
}

func readOutbox(t *testing.T, p *process, id string) outboxDetail {
	t.Helper()
	code, data := request(t, "GET", p.endpoint, "/v1/observer/outbox/"+id, "", nil)
	require.Equal(t, 200, code, "%s", data)
	require.NotContains(t, string(data), "claimId")
	var detail outboxDetail
	require.NoError(t, json.Unmarshal(data, &detail))
	return detail
}

func listOutbox(t *testing.T, endpoint, token, query string) core.Page {
	t.Helper()
	code, data := request(t, "GET", endpoint, channelOutbox+query, token, nil)
	require.Equal(t, 200, code, "%s", data)
	var result core.Page
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func channelCommand[T any](t *testing.T, endpoint, token, command string, req core.OutboxCommand) T {
	t.Helper()
	code, data := request(t, "POST", endpoint, channelOutbox+"/"+command, token, req)
	require.Equal(t, 200, code, "%s", data)
	var result T
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func claimCommand(op string, grant core.OutboxClaim) core.OutboxCommand {
	return core.OutboxCommand{OperationID: op, OutboxID: grant.OutboxID, AttemptID: grant.AttemptID, ClaimID: grant.ClaimID}
}

func finishCommand(op string, grant core.OutboxClaim, outcome, errorCode, nativeID string) core.OutboxCommand {
	req := claimCommand(op, grant)
	req.Outcome, req.ErrorCode, req.MessageID = outcome, errorCode, nativeID
	if nativeID != "" {
		req.ConversationID = "actual:conversation"
	}
	return req
}

func beginOutbox(t *testing.T, p *process, token, op, id string) core.OutboxClaim {
	t.Helper()
	return channelCommand[core.OutboxClaim](t, p.endpoint, token, "begin-attempt", core.OutboxCommand{OperationID: op, OutboxID: id})
}

func outboxClock(t *testing.T, f *fixture) (string, func(time.Duration) time.Time) {
	t.Helper()
	path := filepath.Join(f.dir, "outbox-clock")
	start := time.Now().UTC()
	advance := func(offset time.Duration) time.Time {
		t.Helper()
		at := start.Add(offset)
		write(t, path+".next", at.Format(time.RFC3339Nano))
		require.NoError(t, os.Rename(path+".next", path))
		return at
	}
	advance(0)
	return "SUPERAGENT_TEST_OUTBOX_CLOCK=" + path, advance
}

func TestOutboxRoleTargetAndScope(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	peer, other, intake := addOutboxPeerSources(t, f)
	f.execution(boundary)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	routerToken := roleToken(t, f, router)
	routerMCP := startRoleMCP(t, f, router)
	require.Contains(t, string(routerMCP.call("tools/list", map[string]any{})["result"]), "message_reply")

	addressless := input("addressless")
	addressless.Message.Channel = nil
	addressless.Message.At = "2098-01-02T08:00:00Z"
	first := submit(t, p, f, addressless)
	addressed := input("verified-instance")
	addressed.Message.At = "2000-01-01T12:00:00Z"
	addressed.Message.CollectorInstance = "collector-2"
	second := submit(t, p, f, addressed)
	code, raw := request(t, "POST", p.endpoint, "/v1/channel/messages", peer, addressed)
	require.Equal(t, 200, code, "%s", raw)
	route := decide(t, p, routerToken, newRoute("outbox-scope", []string{second.MessageID, first.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	topicToken := roleToken(t, f, topic)
	unrelatedInput := input("unrelated")
	unrelatedInput.Message.ThreadRef.ID, unrelatedInput.Message.Channel.ConversationID = "another-conversation", "another-conversation"
	unrelated := submit(t, p, f, unrelatedInput)
	otherDecision := decide(t, p, routerToken, newRoute("other-topic", []string{unrelated.MessageID}, f.dir))
	otherTopic := waitBinding(t, p, "topic", otherDecision.Targets[0].TopicID, "ready")

	for _, tc := range []struct {
		token string
		req   core.ReplyRequest
		code  string
		http  int
	}{
		{routerToken, core.ReplyRequest{OperationID: "empty", Text: "no current range"}, "invalid_argument", 400},
		{routerToken, core.ReplyRequest{OperationID: "no-address-router", Text: "no fallback", MessageIDs: []string{second.MessageID, first.MessageID}}, "failed_precondition", 409},
		{routerToken, core.ReplyRequest{OperationID: "missing", Text: "require every supplied Message", MessageIDs: []string{second.MessageID, "absent"}}, "not_found", 404},
		{topicToken, core.ReplyRequest{OperationID: "no-address-topic", Text: "no fallback"}, "failed_precondition", 409},
		{topicToken, core.ReplyRequest{OperationID: "outside", Text: "out of Topic", MessageID: unrelated.MessageID}, "forbidden", 403},
	} {
		code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", tc.token, tc.req)
		errorCode(t, code, raw, tc.http, tc.code)
	}
	topicMCP := startRoleMCP(t, f, topic)
	args := core.ReplyRequest{OperationID: "explicit", Text: "From the owning Topic", MessageID: second.MessageID}
	mcpReply := topicMCP.call("tools/call", map[string]any{"name": "message_reply", "arguments": args})
	var mcpResult struct {
		StructuredContent core.OutboxItem
	}
	require.NoError(t, json.Unmarshal(mcpReply["result"], &mcpResult))
	reply := roleWork[core.OutboxItem](t, p, topicToken, "message_reply", args)
	require.Equal(t, mcpResult.StructuredContent, reply)
	require.Equal(t, topic.ID, reply.AuthorBindingID)
	require.Equal(t, topic.OwnerID, reply.AuthorID)
	require.Equal(t, "collector-1", reply.InstanceID)
	require.Equal(t, *addressed.Message.Channel, reply.Channel)
	args.Text = "changed payload"
	code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", topicToken, args)
	errorCode(t, code, raw, 409, "conflict")

	topicReply := roleWork[core.OutboxItem](t, p, roleToken(t, f, otherTopic), "message_reply", core.ReplyRequest{OperationID: "default", Text: "Own oldest Message"})
	require.Equal(t, unrelated.MessageID, topicReply.MessageID)
	rangeArgs := core.ReplyRequest{OperationID: "router-default", Text: "Current range oldest", MessageIDs: []string{unrelated.MessageID, second.MessageID}}
	routerReply := roleWork[core.OutboxItem](t, p, routerToken, "message_reply", rangeArgs)
	require.Equal(t, second.MessageID, routerReply.MessageID)
	rangeArgs.MessageIDs[0], rangeArgs.MessageIDs[1] = rangeArgs.MessageIDs[1], rangeArgs.MessageIDs[0]
	require.Equal(t, routerReply, roleWork[core.OutboxItem](t, p, routerToken, "message_reply", rangeArgs))

	create := issueInput(f.dir, "outbox-worker")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, topicToken, "issue_create", create)
	worker := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	workerMCP := startRoleMCP(t, f, worker)
	require.NotContains(t, string(workerMCP.call("tools/list", map[string]any{})["result"]), "message_reply")
	code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", roleToken(t, f, worker), args)
	errorCode(t, code, raw, 403, "forbidden")
	code, raw = request(t, "POST", p.endpoint, "/v1/role/source_query", topicToken, map[string]string{"collection": "outbox", "id": topicReply.ID})
	errorCode(t, code, raw, 404, "not_found")
	own := roleWork[core.Page](t, p, topicToken, "source_query", map[string]string{"collection": "outbox", "threadKey": second.ThreadKey})
	require.Len(t, own.Items, 1)
	require.Contains(t, string(own.Items[0]), reply.ID)
	require.Len(t, page(t, p, "outbox?messageId="+second.MessageID).Items, 2)
	require.Len(t, page(t, p, "outbox?threadKey="+url.QueryEscape(second.ThreadKey)).Items, 2)

	firstPage := listOutbox(t, p.endpoint, f.token, "?limit=1")
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "?cursor="+firstPage.NextCursor).Items, 2)
	require.Empty(t, listOutbox(t, p.endpoint, peer, "").Items)
	require.Empty(t, listOutbox(t, p.endpoint, other, "").Items)
	for _, token := range []string{peer, other} {
		code, raw = request(t, "GET", p.endpoint, channelOutbox+"/items/"+reply.ID, token, nil)
		errorCode(t, code, raw, 404, "not_found")
		code, raw = request(t, "POST", p.endpoint, channelOutbox+"/begin-attempt", token, core.OutboxCommand{OperationID: "outside", OutboxID: reply.ID})
		errorCode(t, code, raw, 404, "not_found")
	}
	code, raw = request(t, "GET", p.endpoint, channelOutbox, intake, nil)
	errorCode(t, code, raw, 403, "forbidden")
	code, raw = request(t, "POST", p.endpoint, "/v1/channel/messages", intake, input("unbound-admission"))
	require.Equal(t, 200, code, "%s", raw)
	var unbound core.Receipt
	require.NoError(t, json.Unmarshal(raw, &unbound))
	code, raw = request(t, "POST", p.endpoint, "/v1/role/message_reply", routerToken,
		core.ReplyRequest{OperationID: "ambiguous-source-instance", Text: "Require a verified instance", MessageID: unbound.MessageID})
	errorCode(t, code, raw, 409, "failed_precondition")
	code, raw = request(t, "GET", p.endpoint, channelOutbox, "", nil)
	errorCode(t, code, raw, 401, "unauthorized")
	code, _ = request(t, "POST", p.endpoint, "/v1/observer/outbox/"+reply.ID, "", struct{}{})
	require.Equal(t, 405, code)
	require.Equal(t, "pending", readOutbox(t, p, reply.ID).Status)
	require.Empty(t, readOutbox(t, p, reply.ID).Attempts)
	t.Log("Router/Topic MCP and role operations, authenticated Message provenance, earliest target/address errors, scoped history and read-only pulls verified")
}

func TestOutboxClaimExpiryAndLateResults(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
	message := submit(t, p, f, input("lease-source"))
	queue := func(op string) core.OutboxItem {
		return roleWork[core.OutboxItem](t, p, routerToken, "message_reply", core.ReplyRequest{OperationID: op, Text: op, MessageID: message.MessageID})
	}
	item := queue("recover-uncertain")
	type claimResponse struct {
		code int
		raw  []byte
		op   string
	}
	var workers sync.WaitGroup
	replies := make(chan claimResponse, 8)
	for i := range cap(replies) {
		workers.Go(func() {
			op := fmt.Sprintf("race-%d", i)
			code, raw := request(t, "POST", p.endpoint, channelOutbox+"/begin-attempt", f.token, core.OutboxCommand{OperationID: op, OutboxID: item.ID})
			replies <- claimResponse{code: code, raw: raw, op: op}
		})
	}
	workers.Wait()
	close(replies)
	var grant core.OutboxClaim
	var wins int
	for r := range replies {
		if r.code == 200 {
			wins++
			require.NoError(t, json.Unmarshal(r.raw, &grant))
			require.Equal(t, grant, beginOutbox(t, p, f.token, r.op, item.ID))
		} else {
			errorCode(t, r.code, r.raw, 409, "conflict")
		}
	}
	require.Equal(t, 1, wins)
	require.Equal(t, "send", grant.Mode)
	require.Equal(t, int64(1), grant.Generation)
	confirm := claimCommand("confirm-first", grant)
	require.Equal(t, grant, channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", confirm))
	code, raw := request(t, "POST", p.endpoint, channelOutbox+"/confirm-send", f.token, claimCommand("confirm-twice", grant))
	errorCode(t, code, raw, 409, "conflict")

	at := advance(20 * time.Second)
	renewed := channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "renew-lease", claimCommand("renew-first", grant))
	expires, err := time.Parse(time.RFC3339Nano, renewed.LeaseExpiresAt)
	require.NoError(t, err)
	require.Equal(t, at.Add(time.Minute), expires)
	advance(61 * time.Second)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	require.Equal(t, "sending", readOutbox(t, p, item.ID).Status)
	p.stop(t, true)

	advance(80 * time.Second)
	p = f.start(env)
	require.Eventually(t, func() bool { return readOutbox(t, p, item.ID).Status == "unknown" }, 5*time.Second, 100*time.Millisecond)
	after := readOutbox(t, p, item.ID)
	require.True(t, after.PossibleDuplicate)
	require.Equal(t, "lease_expired_without_result", after.ErrorCode)
	require.Equal(t, "unknown", after.Attempts[0].State)
	require.NotEmpty(t, after.Attempts[0].Claims[0].ReleasedAt)
	code, raw = request(t, "POST", p.endpoint, channelOutbox+"/renew-lease", f.token, claimCommand("renew-late", grant))
	errorCode(t, code, raw, 409, "conflict")
	advance(139 * time.Second)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	code, raw = request(t, "POST", p.endpoint, channelOutbox+"/begin-attempt", f.token, core.OutboxCommand{OperationID: "too-early", OutboxID: item.ID})
	errorCode(t, code, raw, 409, "failed_precondition")
	advance(140 * time.Second)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)
	second := beginOutbox(t, p, f.token, "reserve-retry", item.ID)
	require.Equal(t, int64(2), second.Generation)
	// A success received after reservation but before confirmation prevents another send.
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finishCommand("first-late", grant, "sent", "", "native-first"))
	code, raw = request(t, "POST", p.endpoint, channelOutbox+"/confirm-send", f.token, claimCommand("blocked-by-success", second))
	errorCode(t, code, raw, 409, "conflict")
	after = readOutbox(t, p, item.ID)
	require.Equal(t, "sent", after.Status)
	require.Equal(t, "skipped", after.Attempts[1].State)
	require.Empty(t, after.Attempts[1].StartedAt)

	item = queue("two-sends-already-started")
	first := beginOutbox(t, p, f.token, "in-flight-1", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("in-flight-confirm-1", first))
	advance(260 * time.Second)
	second = beginOutbox(t, p, f.token, "in-flight-2", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("in-flight-confirm-2", second))
	firstResult := finishCommand("in-flight-late-1", first, "sent", "", "native-A")
	saved := channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", firstResult)
	after = readOutbox(t, p, item.ID)
	require.Equal(t, "sent", after.Status)
	require.Equal(t, second.AttemptID, after.CurrentAttempt)
	require.Equal(t, "started", after.Attempts[1].State)
	require.Empty(t, after.Attempts[1].Claims[0].ReleasedAt)
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finishCommand("in-flight-second", second, "sent", "", "native-B"))
	staleFailure := finishCommand("in-flight-stale-failure", first, "permanent_failure", "permission_denied", "")
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", staleFailure)
	require.Equal(t, saved, channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", firstResult))
	after = readOutbox(t, p, item.ID)
	require.True(t, after.PossibleDuplicate)
	require.Equal(t, "sent", after.Status)
	require.Equal(t, "sent", after.Attempts[0].State)
	require.Len(t, after.Attempts[0].Results, 2)
	require.Equal(t, "native-A", after.Attempts[0].Results[0].MessageID)
	require.Equal(t, "native-B", after.Attempts[1].Results[0].MessageID)

	item = queue("late-task-before-retry")
	first = beginOutbox(t, p, f.token, "late-task-first", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("late-task-start", first))
	advance(380 * time.Second)
	second = beginOutbox(t, p, f.token, "late-task-reservation", item.ID)
	lateTask := claimCommand("late-task-record", first)
	lateTask.TaskID = "original-late-task"
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "record-task", lateTask)
	code, raw = request(t, "POST", p.endpoint, channelOutbox+"/confirm-send", f.token, claimCommand("late-task-blocks-send", second))
	errorCode(t, code, raw, 409, "conflict")
	recoverClaim := beginOutbox(t, p, f.token, "late-task-recovery", item.ID)
	require.Equal(t, "recover", recoverClaim.Mode)
	require.Equal(t, first.AttemptID, recoverClaim.AttemptID)
	require.Equal(t, lateTask.TaskID, recoverClaim.TaskID)
	governed := finishCommand("old-claim-late-unknown", first, "unknown", "poll_interrupted", "")
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", governed)
	after = readOutbox(t, p, item.ID)
	require.Equal(t, "task", after.Attempts[0].State)
	require.Empty(t, after.Attempts[0].Claims[1].ReleasedAt)
	recovered := finishCommand("late-task-settlement", recoverClaim, "sent", "", "late-task-native")
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", recovered)
	require.Equal(t, "sent", readOutbox(t, p, item.ID).Status)
	t.Log("8 concurrent claims yield one winner, renewal fences expiry, unknown lease recovery waits 60s, late success fences reserved sends and retains both already-started results")
}

func TestOutboxKnownTaskRecoveryAfterUnknown(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	router := waitBinding(t, p, "router", "", "ready")
	message := submit(t, p, f, input("known-task-unknown-result"))
	item := roleWork[core.OutboxItem](t, p, roleToken(t, f, router), "message_reply", core.ReplyRequest{
		OperationID: "reply", Text: "Recover the original task", MessageID: message.MessageID,
	})
	send := beginOutbox(t, p, f.token, "send", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("confirm", send))
	record := claimCommand("task", send)
	record.TaskID = "original-native-task"
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "record-task", record)
	unknown := finishCommand("first-poll-unavailable", send, "unknown", "task_query_unavailable", "")
	stored := channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", unknown)
	require.Equal(t, record.TaskID, stored.TaskID)
	require.Len(t, stored.Results, 1)
	require.Equal(t, "unknown", stored.Results[0].Outcome)
	p.stop(t, true)

	advance(2 * time.Minute)
	p = f.start(env)
	require.Len(t, listOutbox(t, p.endpoint, f.token, "").Items, 1)
	recovered := beginOutbox(t, p, f.token, "recover", item.ID)
	require.Equal(t, "recover", recovered.Mode)
	require.Equal(t, send.AttemptID, recovered.AttemptID)
	require.Equal(t, record.TaskID, recovered.TaskID)
	require.Equal(t, int64(2), recovered.Generation)
	status, body := request(t, "POST", p.endpoint, channelOutbox+"/confirm-send", f.token, claimCommand("wrong-mode", recovered))
	errorCode(t, status, body, 409, "conflict")
	detail := readOutbox(t, p, item.ID)
	require.Equal(t, "sending", detail.Status)
	require.False(t, detail.PossibleDuplicate)
	require.Empty(t, detail.FailureEventID)
	require.Len(t, detail.Attempts, 1)
	require.Equal(t, stored, channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", unknown))
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt",
		finishCommand("second-poll-unavailable", recovered, "unknown", "task_query_unavailable", ""))
	recovered = beginOutbox(t, p, f.token, "recover-again", item.ID)
	require.Equal(t, "recover", recovered.Mode)
	require.Equal(t, record.TaskID, recovered.TaskID)
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt",
		finishCommand("verified-success", recovered, "sent", "", "actual-task-result"))
	detail = readOutbox(t, p, item.ID)
	require.Equal(t, "sent", detail.Status)
	require.Len(t, detail.Attempts, 1)
	require.Len(t, detail.Attempts[0].Claims, 3)
	require.Len(t, detail.Attempts[0].Results, 3)
	require.Equal(t, "actual-task-result", detail.Attempts[0].Results[2].MessageID)
	t.Log("poll uncertainty survives restart and two recovery claims on the same native task; no additional send attempt")
}

func TestOutboxFailureNotification(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
	msg := submit(t, p, f, input("failures"))
	route := decide(t, p, routerToken, newRoute("failure-scope", []string{msg.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", route.Targets[0].TopicID, "ready")
	token := roleToken(t, f, topic)
	queue := func(op string) core.OutboxItem {
		return roleWork[core.OutboxItem](t, p, token, "message_reply", core.ReplyRequest{OperationID: op, Text: op})
	}
	retried, terminal := queue("retry-budget"), queue("permanent-failure")
	grant := beginOutbox(t, p, f.token, "terminal-begin", terminal.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("terminal-confirm", grant))
	deterministic := finishCommand("terminal-finish", grant, "permanent_failure", "permission_denied", "")
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", deterministic)
	var last core.OutboxCommand
	for i := range 10 {
		op := fmt.Sprintf("repeat-%d", i)
		advance(time.Duration(i) * time.Minute)
		grant = beginOutbox(t, p, f.token, op, retried.ID)
		channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand(op, grant))
		outcome := "unknown"
		if i%2 == 0 {
			outcome = "retryable_failure"
		}
		last = finishCommand(op, grant, outcome, "controlled_failure", "")
		channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", last)
		if i < 9 {
			require.Empty(t, readOutbox(t, p, retried.ID).FailureEventID)
		}
	}
	advance(20 * time.Minute)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	p.stop(t, true)
	p = f.start(env)
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", last)
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", deterministic)
	total := 0
	for _, event := range page(t, p, "events").Items {
		var e core.Event
		require.NoError(t, json.Unmarshal(event, &e))
		if e.Type == "outbox.failed" {
			require.Equal(t, topic.OwnerID, e.TargetID)
			require.Equal(t, topic.ID, e.BindingID)
			observed := roleWork[core.Event](t, p, token, "event_get", map[string]string{"eventId": e.ID})
			require.Equal(t, e.ID, observed.ID)
			require.JSONEq(t, string(e.Payload), string(observed.Payload))
			require.Equal(t, "act", observed.Intent)
			total++
		}
	}
	require.Equal(t, 2, total)
	for id, count := range map[string]int{terminal.ID: 1, retried.ID: 10} {
		item := readOutbox(t, p, id)
		require.Equal(t, "failed", item.Status)
		require.NotEmpty(t, item.FailureEventID)
		require.Len(t, item.Attempts, count)
	}
	active := roleWork[core.Topic](t, p, token, "topic_query", map[string]string{"collection": "topics", "id": topic.OwnerID})
	require.Equal(t, "active", active.Status)
	t.Log("a permanent permission error stops after one send; ten retryable/unknown outcomes stop and notify once across restart, with Topic business state active")
}

func TestOutboxFailureNotificationAfterCoreAndDriverRestart(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	configureOutboxSource(t, f)
	gate := t.TempDir()
	const stage = "outbox_finish-attempt_after_commit"
	p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topic, _ := nativeWorkTopic(t, f, p, peer, router, "native-outbox-failure")

	inbound := input("native-outbox-reply-target")
	inbound.Message.ThreadRef.ID = "native-outbox-failure"
	inbound.Message.Channel.ConversationID = inbound.Message.ThreadRef.ID
	message := submit(t, p, f, inbound)
	incoming := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, incoming.session)
	decodeBatch(t, incoming, message.EventID)
	incoming.answer <- nativeDeliveryAnswer{complete: true}
	waitDelivery(t, p, message.EventID, "delivered")

	author := roleToken(t, f, topic)
	item := roleWork[core.OutboxItem](t, p, author, "message_reply", core.ReplyRequest{
		OperationID: "native-failure-reply", Text: "A controlled outbound failure", MessageID: message.MessageID,
	})
	claim := beginOutbox(t, p, f.token, "native-failure-begin", item.ID)
	channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("native-failure-confirm", claim))
	finish := finishCommand("native-failure-finish", claim, "permanent_failure", "permission_denied", "")
	body, err := json.Marshal(finish)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+channelOutbox+"/finish-attempt", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	outcome := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if resp != nil {
			_ = resp.Body.Close()
			err = errors.New("finish returned before the Core crash")
		}
		outcome <- err
	}()
	waitWorkGate(t, gate, stage)
	p.stop(t, true)
	require.ErrorContains(t, <-outcome, "EOF")

	require.NoError(t, massd.StopRuntime(t.Context(), "agent", nil))
	var offline resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: topic.SessionID}, &offline))
	require.Equal(t, "offline", string(offline.Status.State))
	require.NoError(t, massd.StartRuntime(t.Context(), "agent", nil))
	p = f.start()
	stored := readOutbox(t, p, item.ID)
	require.Equal(t, "failed", stored.Status)
	require.NotEmpty(t, stored.FailureEventID)
	notification := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, notification.session)
	event := decodeBatch(t, notification, stored.FailureEventID).Events[0]
	require.Equal(t, "outbox.failed", event.Type)
	require.Equal(t, "act", event.Intent)
	require.Contains(t, string(event.Payload), "permission_denied")
	notification.answer <- nativeDeliveryAnswer{complete: true}
	waitDelivery(t, p, stored.FailureEventID, "delivered")
	channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finish)
	require.Len(t, issueEvents(t, p, "outbox.failed"), 1)
	require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
	peer.quiet(t, 350*time.Millisecond)
	t.Log("the authored failure notice survived lost finish response and Core and Driver restart, reached the original native Topic, and was recorded delivered exactly once")
}

func TestOutboxLostFinishReceipt(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"outbox_finish-attempt_before_commit", "outbox_finish-attempt_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			boundary, f := newRouteBoundary(t), fixtureAt(t)
			configureOutboxSource(t, f)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
			msg := submit(t, p, f, input("finish-crash"))
			item := roleWork[core.OutboxItem](t, p, routerToken, "message_reply",
				core.ReplyRequest{OperationID: "finish-crash", Text: "Preserve known receipt", MessageID: msg.MessageID})
			grant := beginOutbox(t, p, f.token, "lost-begin", item.ID)
			channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand("lost-confirm", grant))
			finish := finishCommand("known-success", grant, "sent", "", "native-known")
			body, err := json.Marshal(finish)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+channelOutbox+"/finish-attempt", bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.token)
			outcome := make(chan error, 1)
			go func() {
				client := http.Client{Timeout: 15 * time.Second}
				resp, callErr := client.Do(req)
				if resp != nil {
					_ = resp.Body.Close()
					callErr = errors.New("finish returned a response before process kill")
				}
				outcome <- callErr
			}()
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			require.ErrorContains(t, <-outcome, "EOF")
			p = f.start()
			first := readOutbox(t, p, item.ID)
			if strings.HasSuffix(stage, "_before_commit") {
				require.Equal(t, "sending", first.Status)
				require.Empty(t, first.Attempts[0].Results)
			} else {
				require.Equal(t, "sent", first.Status)
				require.Len(t, first.Attempts[0].Results, 1)
			}
			stored := channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finish)
			require.Equal(t, stored, channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finish))
			end := readOutbox(t, p, item.ID)
			require.Equal(t, "sent", end.Status)
			require.Len(t, end.Attempts, 1)
			require.Len(t, end.Attempts[0].Results, 1)
			require.Equal(t, "native-known", end.Attempts[0].Results[0].MessageID)
			require.Empty(t, listOutbox(t, p.endpoint, f.token, "").Items)
			t.Log("SIGKILL discarded the finish HTTP response; known success replays once with the original operationId")
		})
	}
}

// This test binary also serves as a separate, disposable consumer process.
// The parent supplies only synthetic credentials through stdin and controls
// channel acknowledgement timing at a loopback HTTP boundary.
func TestOutboxConsumerPeer(t *testing.T) {
	if os.Getenv("SUPERAGENT_OUTBOX_CONSUMER_PEER") != "1" {
		t.Skip("subprocess entry")
	}
	var args struct {
		Core, Token, Channel, Operation, ItemID, Mode string
	}
	require.NoError(t, json.NewDecoder(os.Stdin).Decode(&args))
	items := listOutbox(t, args.Core, args.Token, "")
	require.Len(t, items.Items, 1)
	var item core.OutboxItem
	require.NoError(t, json.Unmarshal(items.Items[0], &item))
	require.Equal(t, args.ItemID, item.ID)
	grant := channelCommand[core.OutboxClaim](t, args.Core, args.Token, "begin-attempt", core.OutboxCommand{OperationID: args.Operation, OutboxID: item.ID})
	require.Equal(t, args.Mode, grant.Mode)
	var result struct {
		TaskID, ConversationID, MessageID string
	}
	if grant.Mode == "send" {
		channelCommand[core.OutboxClaim](t, args.Core, args.Token, "confirm-send", claimCommand(args.Operation, grant))
		code, raw := request(t, "POST", args.Channel, "/send", "", item)
		require.Equal(t, 200, code)
		require.NoError(t, json.Unmarshal(raw, &result))
		if result.TaskID != "" {
			task := claimCommand(args.Operation, grant)
			task.TaskID = result.TaskID
			channelCommand[core.OutboxAttempt](t, args.Core, args.Token, "record-task", task)
			grant.TaskID = result.TaskID
		}
	}
	if grant.TaskID != "" {
		code, raw := request(t, "GET", args.Channel, "/tasks/"+grant.TaskID, "", nil)
		require.Equal(t, 200, code)
		require.NoError(t, json.Unmarshal(raw, &result))
	}
	finish := claimCommand(args.Operation, grant)
	finish.Outcome, finish.ConversationID, finish.MessageID = "sent", result.ConversationID, result.MessageID
	channelCommand[core.OutboxAttempt](t, args.Core, args.Token, "finish-attempt", finish)
}

func TestOutboxConsumerRestart(t *testing.T) {
	t.Parallel()
	boundary, f := newRouteBoundary(t), fixtureAt(t)
	configureOutboxSource(t, f)
	f.execution(boundary)
	env, advance := outboxClock(t, f)
	p := f.start(env)
	routerToken := roleToken(t, f, waitBinding(t, p, "router", "", "ready"))
	m := submit(t, p, f, input("clarify"))
	waitDelivered(t, p, m.MessageID, "router", "")
	clarification := roleWork[core.OutboxItem](t, p, routerToken, "message_reply",
		core.ReplyRequest{OperationID: "clarify", Text: "Which workspace should I use?", MessageIDs: []string{m.MessageID}})

	var sends, queries atomic.Int32
	taskQueried := make(chan struct{})
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/send":
			var item core.OutboxItem
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil || item.Channel.ConversationID != "conversation:Case/%原文" {
				http.Error(w, "invalid native target", http.StatusBadRequest)
				return
			}
			if sends.Add(1) == 1 && item.Text == clarification.Text && item.Channel.MessageID == "clarify" {
				_, _ = fmt.Fprint(w, `{"conversationId":"actual:conversation","messageId":"clarification-reply"}`)
			} else if item.Channel.MessageID == "clarification-answer" {
				_, _ = fmt.Fprint(w, `{"taskId":"task-42"}`)
			} else {
				http.Error(w, "unexpected reply target", http.StatusBadRequest)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-42":
			if queries.Add(1) == 1 {
				close(taskQueried)
				<-r.Context().Done()
				return
			}
			_, _ = fmt.Fprint(w, `{"conversationId":"actual:conversation","messageId":"confirmation-reply"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(channel.Close)
	consumer := func(op, id, mode string) *exec.Cmd {
		t.Helper()
		b, err := json.Marshal(map[string]string{"Core": p.endpoint, "Token": f.token, "Channel": channel.URL, "Operation": op, "ItemID": id, "Mode": mode})
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOutboxConsumerPeer$")
		cmd.Env = []string{"SUPERAGENT_OUTBOX_CONSUMER_PEER=1", "GORACE=atexit_sleep_ms=0", "HOME=" + f.dir, "PATH=" + f.dir}
		cmd.Stdin = bytes.NewReader(b)
		return cmd
	}
	output, err := consumer("initial-clarification", clarification.ID, "send").CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Equal(t, "sent", readOutbox(t, p, clarification.ID).Status)
	answer := input("clarification-answer")
	answer.Message.Text = "Use the current project workspace."
	followup := submit(t, p, f, answer)
	require.Equal(t, m.ThreadKey, followup.ThreadKey)
	waitDelivered(t, p, followup.MessageID, "router", "")
	threads := page(t, p, "messages?threadKey="+url.QueryEscape(m.ThreadKey))
	require.Len(t, threads.Items, 2)
	require.Len(t, page(t, p, "outbox?threadKey="+url.QueryEscape(m.ThreadKey)).Items, 1)
	reply := roleWork[core.OutboxItem](t, p, routerToken, "message_reply",
		core.ReplyRequest{OperationID: "after-answer", Text: "Workspace confirmed.", MessageID: followup.MessageID})
	inFlight := consumer("before-consumer-kill", reply.ID, "send")
	var buf bytes.Buffer
	inFlight.Stdout, inFlight.Stderr = &buf, &buf
	require.NoError(t, inFlight.Start())
	t.Cleanup(func() {
		if inFlight.ProcessState == nil {
			_ = inFlight.Process.Kill()
			_ = inFlight.Wait()
		}
	})
	select {
	case <-taskQueried:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer did not query the persisted task")
	}
	task := readOutbox(t, p, reply.ID)
	require.Equal(t, "sending", task.Status)
	require.Equal(t, "task-42", task.Attempts[0].TaskID)
	require.Empty(t, task.Attempts[0].Results)
	require.NoError(t, inFlight.Process.Kill())
	require.Error(t, inFlight.Wait())
	p.stop(t, true)
	advance(time.Minute)
	p = f.start(env)
	output, err = consumer("after-consumer-kill", reply.ID, "recover").CombinedOutput()
	require.NoError(t, err, "%s", output)
	final := readOutbox(t, p, reply.ID)
	require.Equal(t, "sent", final.Status)
	require.Len(t, final.Attempts, 1)
	require.Len(t, final.Attempts[0].Claims, 2)
	require.Equal(t, "recover", final.Attempts[0].Claims[1].Mode)
	require.Equal(t, "confirmation-reply", final.Attempts[0].Results[0].MessageID)
	require.Equal(t, int32(2), sends.Load())
	require.Equal(t, int32(2), queries.Load())
	t.Log("consumer subprocess sent clarification, human followup reached Router, then both processes were killed after task persistence; a replacement queried the original task and sent nothing again")
}

func seedObserverOutbox(t *testing.T, p *process, f *fixture, routerToken, channel string) {
	t.Helper()
	source := input("outbox-observer-source")
	source.Message.ThreadRef.ID, source.Message.Channel.ConversationID = "outbox-observer-conversation", "outbox-observer-conversation"
	m := submit(t, p, f, source)
	for _, outcome := range []string{"pending", "task", "unknown", "permanent_failure", "sent"} {
		op := "browser-outbox-" + outcome
		item := roleWork[core.OutboxItem](t, p, routerToken, "message_reply", core.ReplyRequest{OperationID: op, Text: "Observer reply " + outcome, MessageID: m.MessageID})
		if outcome == "pending" {
			continue
		}
		grant := beginOutbox(t, p, f.token, op, item.ID)
		channelCommand[core.OutboxClaim](t, p.endpoint, f.token, "confirm-send", claimCommand(op, grant))
		switch outcome {
		case "task":
			task := claimCommand(op, grant)
			task.TaskID = "observer-task-42"
			channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "record-task", task)
		case "sent":
			code, nativeID := request(t, http.MethodPost, channel, "/send", "", item)
			require.Equal(t, http.StatusOK, code)
			channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finishCommand(op, grant, outcome, "", string(nativeID)))
		default:
			channelCommand[core.OutboxAttempt](t, p.endpoint, f.token, "finish-attempt", finishCommand(op, grant, outcome, "observer_"+outcome, ""))
		}
	}
	echo := input("observer-actual-message")
	echo.Message.ThreadRef.ID, echo.Message.Channel.ConversationID = "actual:conversation", "actual:conversation"
	submit(t, p, f, echo)
}
