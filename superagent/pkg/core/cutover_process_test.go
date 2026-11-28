package core_test

import (
	"context"
	"database/sql"
	encodingbinary "encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestCutoverDefaultAndSchema(t *testing.T) {
	f := fixtureAt(t)
	config, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(config), "dataDir: "+f.data+"\n", "", 1))
	p := f.start()
	require.NotEmpty(t, submit(t, p, f, input("session-binding-schema")).MessageID)
	p.stop(t, false)
	require.NoFileExists(t, filepath.Join(f.data, "core.sqlite"))
	assertCutoverSchema(t, filepath.Join(f.dir, "superagent-core-data", "core.sqlite"))
}

const dropStartupQueryIndexes = dropIssueQueryIndexes + `
DROP INDEX session_bindings_pending;
DROP INDEX session_bindings_ready_owner;
DROP INDEX topics_created_at;`

func assertStartupQueryIndexes(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema
 WHERE type='index' AND name IN ('session_bindings_pending','session_bindings_ready_owner',
 'topics_created_at','issues_topic_status','issues_unfinished')`).Scan(&count))
	require.Equal(t, want, count)
}

func TestCutoverInstallsStartupQueryIndexes(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	message := submit(t, p, f, input("retained-before-index-install"))
	p.stop(t, false)

	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), dropStartupQueryIndexes+"\nVACUUM")
	require.NoError(t, err)
	var pages int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages))
	assertStartupQueryIndexes(t, db, 0)

	// The first empty index fits on one additional page. The second runs out
	// of pages and startup must roll back both statements.
	f.configure(pages + 1)
	f.fails("unavailable")
	assertStartupQueryIndexes(t, db, 0)

	f.configure(0)
	for range 2 {
		p = f.start()
		assertStartupQueryIndexes(t, db, 5)
		retained := submit(t, p, f, input("retained-before-index-install"))
		require.True(t, retained.Duplicate)
		require.Equal(t, message.MessageID, retained.MessageID)
		p.stop(t, false)
	}
}

func TestCutoverRejectsPriorSchemasWithoutWriting(t *testing.T) {
	for _, version := range []int{24, 25, 26, 27} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := fixtureAt(t)
			require.NoError(t, os.MkdirAll(f.data, 0o700))
			path := filepath.Join(f.data, "core.sqlite")
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), fmt.Sprintf(`PRAGMA application_id=1398096210;
PRAGMA user_version=%d;
CREATE TABLE trigger_messages (id TEXT PRIMARY KEY, body TEXT);
INSERT INTO trigger_messages VALUES ('mass-m1','{"text":"retained in prior Core"}');`, version))
			require.NoError(t, err)
			require.NoError(t, db.Close())
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.EqualValues(t, version, encodingbinary.BigEndian.Uint32(before[60:64]))

			f.fails(fmt.Sprintf("schema version %d is unsupported", version))

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			entries, err := os.ReadDir(f.data)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, "core.sqlite", entries[0].Name())
		})
	}
}

func TestCutoverRejectsUnsupportedWALVersion(t *testing.T) {
	f := fixtureAt(t)
	p := f.start()
	p.stop(t, false)
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), "PRAGMA user_version=999")
	require.NoError(t, err)
	header, err := os.ReadFile(filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	require.EqualValues(t, 28, encodingbinary.BigEndian.Uint32(header[60:64]))
	f.fails("unsupported")
	var version int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 999, version)
}

func assertCutoverSchema(t *testing.T, path string) {
	t.Helper()
	require.FileExists(t, path)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var version int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 28, version)
	assertStartupQueryIndexes(t, db, 5)
	check := func(query string, expected []string) {
		rows, err := db.QueryContext(t.Context(), query)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		var values []string
		for rows.Next() {
			var value string
			require.NoError(t, rows.Scan(&value))
			values = append(values, value)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, expected, values)
		t.Logf("%s -> %v", query, values)
	}
	check("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
		[]string{"internal_events", "issues", "outbox", "scheduled_tasks", "session_bindings", "threads", "topics", "trigger_messages"})
	check("SELECT name FROM pragma_table_info('outbox') ORDER BY cid", []string{"id", "message_id", "content", "status", "receipt", "blocker_ref"})
	check("SELECT name FROM pragma_table_info('internal_events') ORDER BY cid",
		[]string{"event_id", "kind", "sender", "recipient", "content", "body", "refs", "created_at", "status", "sent_at"})
	check(`SELECT name||':'||hidden FROM pragma_table_xinfo('scheduled_tasks') ORDER BY cid`,
		[]string{
			"seq:0", "id:3", "owner_type:0", "owner_id:0", "topic_generation:0", "created_at:0", "schedule_anchor:0",
			"deleted_at:0", "body:0", "status:3", "next_run_at:3", "creation_key:0", "creation_digest:0",
		})
	check(`SELECT name||':'||partial FROM pragma_index_list('scheduled_tasks') WHERE origin='c' ORDER BY name`,
		[]string{"scheduled_tasks_due:1", "scheduled_tasks_owner:1"})
	check(`SELECT m.name||'.'||f."from"||' -> '||f."table"||'.'||f."to"
 FROM sqlite_schema m,pragma_foreign_key_list(m.name) f WHERE m.type='table' ORDER BY 1`,
		[]string{
			"issues.topic_id -> topics.id", "outbox.message_id -> trigger_messages.id", "scheduled_tasks.owner_id -> topics.id",
			"trigger_messages.scheduled_task_id -> scheduled_tasks.id", "trigger_messages.thread_id -> threads.id",
		})
	check(`SELECT "table" FROM pragma_foreign_key_check`, nil)
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback()) }()
	insert := `INSERT INTO internal_events (event_id,kind,sender,recipient,content,body,created_at,status)
 VALUES ('constraint-probe','test','{"kind":"core"}','{"kind":"router"}',?,'"reason"',?,'pending')`
	for _, value := range []string{"plain text", "[]", "null"} {
		_, err := tx.ExecContext(t.Context(), insert, value, time.Now().UTC().Format(time.RFC3339Nano))
		require.ErrorContains(t, err, "constraint failed")
	}
	_, err = tx.ExecContext(t.Context(), insert, nil, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	assertScheduledTaskConstraints(t, tx)
}

func assertScheduledTaskConstraints(t *testing.T, tx *sql.Tx) {
	t.Helper()
	const insert = `INSERT INTO scheduled_tasks(owner_type,owner_id,created_at,schedule_anchor,body,creation_key,creation_digest)
 VALUES(?1,?2,?3,?3,?4,?5,'digest')`
	const at = "2026-11-25T09:00:00.000000000Z"
	active := []byte(`{"status":"active","nextRunAt":"` + at + `"}`)
	var seq, generation int
	var id, status, nextRunAt, bodyType string
	err := tx.QueryRowContext(t.Context(),
		insert+` RETURNING seq,id,topic_generation,status,next_run_at,typeof(body)`, nil, nil, at, active, "key").
		Scan(&seq, &id, &generation, &status, &nextRunAt, &bodyType)
	require.NoError(t, err)
	require.Equal(t, "mass-st"+strconv.Itoa(seq), id)
	require.Zero(t, generation)
	require.Equal(t, "active", status)
	require.Equal(t, at, nextRunAt)
	require.Equal(t, "blob", bodyType)

	_, err = tx.ExecContext(t.Context(), insert, nil, nil, at, active, "key")
	require.ErrorContains(t, err, "UNIQUE constraint failed: scheduled_tasks.creation_key")
	_, err = tx.ExecContext(t.Context(), insert, "topic", "mass-t0", at, active, "missing-topic")
	require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
	_, err = tx.ExecContext(t.Context(), insert, "topic", nil, at, active, "unpaired-owner")
	require.ErrorContains(t, err, "CHECK constraint failed")
	_, err = tx.ExecContext(t.Context(), insert, nil, nil, at, []byte(`{}`), "missing-status")
	require.ErrorContains(t, err, "NOT NULL constraint failed: scheduled_tasks.status")
	_, err = tx.ExecContext(t.Context(), insert, nil, nil, at, []byte(`{"status":"active"}`), "missing-next-run")
	require.ErrorContains(t, err, "CHECK constraint failed")
}

func TestCutoverTriggerIssueAcrossRestart(t *testing.T) {
	dws := os.Getenv("SUPERAGENT_CUTOVER_DWS")
	coreExe := os.Getenv("SUPERAGENT_CUTOVER_CORE")
	if dws == "" && coreExe == "" {
		t.Skip("explicit three-process cutover acceptance")
	}
	for _, path := range []string{dws, coreExe} {
		require.True(t, filepath.IsAbs(path), "provide Superagent and DWS fixture executable paths")
		require.FileExists(t, path)
	}
	peer := newNativeSubmitPeer(t)
	massd, _ := startEventTestMassd(t, peer)
	f := fixtureAt(t)
	f.executable = coreExe

	f.execution(&routeBoundary{url: massd})
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)

	const conversation = "cutover:test-direct"
	const firstNative = "cutover-request"
	const questionNative = "cutover-native-question"
	triggerStatus := startCutoverTrigger(t, f, p.endpoint, coreExe, dws)
	feed := func(index int, id, body, quote string) {
		t.Helper()
		data := map[string]any{
			"type": "user_im_message_receive_o2o_all", "message_id": id, "conversation_id": conversation,
			"sender_open_dingtalk_id": "peer", "content": body,
		}
		if quote != "" {
			data["quoted_message"] = map[string]any{"conversation_id": conversation, "message_id": quote}
		}
		raw, err := json.Marshal(data)
		require.NoError(t, err)
		writeCutoverFile(t, filepath.Join(f.dir, fmt.Sprintf("event-%03d.json", index)), raw)
	}
	feed(1, firstNative, "Check the selected region.", "")
	awaitMessage := func(native string) (m core.Message) {
		t.Helper()
		require.Eventually(t, func() bool {
			for _, row := range page(t, p, "trigger-messages").Items {
				require.NoError(t, json.Unmarshal(row, &m))
				if m.SourceMessageID == native {
					return true
				}
			}
			return false
		}, 8*time.Second, 50*time.Millisecond)
		return m
	}
	accept := func(session, eventID, turn string) {
		t.Helper()
		call := peer.next(t, "turn/start")
		require.Equal(t, session, call.session)
		decodeInternalBatch(t, call, eventID)
		call.answer <- nativeSubmitAnswer{workID: turn, complete: true}
		waitWorkEvent(t, p, eventID, "sent")
	}
	first := awaitMessage(firstNative)
	accept(routerBinding.SessionID, first.EventID, "cutover-router")
	route := newRoute("cutover-topic", []string{first.ID}, f.dir)
	decision := mcpWork[core.RouteDecision](t, router, "route_messages", route)
	target := decision.Targets[0]
	topicBinding := waitBinding(t, p, "topic", target.TopicID, "ready")
	accept(topicBinding.SessionID, target.EventIDs[first.ID], "cutover-topic")
	topic := startRoleMCP(t, f, topicBinding)
	create := issueInput(f.dir, "cutover-issue")
	create.Start = true
	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	x := work.SessionBinding
	issueBinding := waitBinding(t, p, "issue", x.ID, "ready")
	issue := startRoleMCP(t, f, issueBinding)
	firstWork := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, firstWork.session)
	decodeInternalBatch(t, firstWork, x.StartEventID)
	firstWork.answer <- nativeSubmitAnswer{workID: "cutover-start"}
	waitWorkEvent(t, p, x.StartEventID, "sent")

	report := func(op, kind, summary string, questions []string) core.Report {
		return mcpWork[core.Report](t, issue, "submit_report", core.ReportRequest{
			OperationID: op, Kind: kind, Summary: summary, Questions: questions, GoalRevision: x.StartGoalRevision,
		})
	}
	const question = "Which region should I check?"
	blockedReport := report("cutover-blocked", "blocked", "Region required", []string{question})
	firstWork.end("cutover-start")
	firstWork.idle()
	accept(topicBinding.SessionID, blockedReport.EventID, "cutover-blocked-report")
	waiting := blockers(t, topic, work.Issue, "cutover-human-blocker", human(question))
	require.Equal(t, "blocked", waiting.Status)
	ref := &core.BlockerRef{IssueID: waiting.ID, BlockerID: waiting.Blockers[0].ID}
	reply := mcpWork[core.OutboxItem](t, topic, "send_reply", core.ReplyRequest{
		OperationID: "cutover-question", MessageID: first.ID, Content: markdownReply(question), BlockerRef: ref,
	})
	var task string
	require.Eventually(t, func() bool {
		item := readOutbox(t, p, reply.ID)
		if item.Receipt == nil || item.Receipt.TaskID == "" {
			return false
		}
		require.Equal(t, "pending", item.Status)
		task = item.Receipt.TaskID
		return true
	}, 8*time.Second, 50*time.Millisecond)

	endpoint := p.endpoint
	p.stop(t, true)
	p = f.start()
	require.Equal(t, endpoint, p.endpoint)
	require.Equal(t, waiting, readRecord[core.Issue](t, p, "issues/"+waiting.ID))
	require.Equal(t, "sent", waitWorkEvent(t, p, blockedReport.EventID, "sent").Status)
	require.Equal(t, task, readOutbox(t, p, reply.ID).Receipt.TaskID)
	writeCutoverFile(t, filepath.Join(f.dir, task+".json"), fmt.Appendf(nil,
		`{"success":true,"openTaskId":%q,"result":{"sendStatus":"SUCCESS","openConversationId":%q,"openMessageId":%q}}`, task, conversation, questionNative))
	require.Eventually(t, func() bool { return readOutbox(t, p, reply.ID).Status == "sent" }, 8*time.Second, 50*time.Millisecond)
	sent := readOutbox(t, p, reply.ID)
	require.Equal(t, task, sent.Receipt.TaskID)
	evidence, err := filepath.Glob(filepath.Join(f.dir, "dws.sqlite.dws-sends", "*.json"))
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	rawEvidence, err := os.ReadFile(evidence[0])
	require.NoError(t, err)
	var attempt struct {
		OutboxID string
		Result   struct{ ConversationID, MessageID string }
	}
	require.NoError(t, json.Unmarshal(rawEvidence, &attempt))
	require.Equal(t, reply.ID, attempt.OutboxID)
	require.Equal(t, questionNative, attempt.Result.MessageID)
	require.Equal(t, conversation, attempt.Result.ConversationID)

	feed(2, "cutover-answer", "Use the east region.", questionNative)
	answer := awaitMessage("cutover-answer")
	accept(topicBinding.SessionID, answer.EventID, "cutover-answer")
	quoted := mcpWork[quotedReplyRecord](t, topic, "get_message", map[string]string{"messageId": answer.ID})
	require.NotNil(t, quoted.QuotedReply)
	require.Equal(t, reply.ID, quoted.QuotedReply.OutboxID)
	require.Equal(t, *ref, quoted.QuotedReply.BlockerRef)
	continued := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "cutover-resolve", IssueID: waiting.ID, ExpectedRevision: waiting.Revision,
		Resolve:            []core.BlockerResolution{{BlockerID: ref.BlockerID, Reason: "User confirmed the region", MessageID: answer.ID}},
		NotifyIssueSession: true,
		UnblockedStatus:    "active",
	})
	next := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, next.session)
	internalEvent := decodeInternalBatch(t, next, continued.NotificationEventID)[0]
	require.Equal(t, "issue_session_update", internalEvent.Kind)
	require.Contains(t, next.text, answer.ID)
	next.answer <- nativeSubmitAnswer{workID: "cutover-continue"}
	waitWorkEvent(t, p, continued.NotificationEventID, "sent")
	require.Equal(t, x.ID, continued.Issue.CurrentSessionBindingID)
	require.Equal(t, answer.ID, continued.Issue.ResolvedBlockers[0].Resolution.MessageID)
	require.Empty(t, continued.Issue.Blockers)
	ready := report("cutover-ready", "ready", "East region checked.", nil)
	next.end("cutover-continue")
	next.idle()
	accept(topicBinding.SessionID, ready.EventID, "cutover-ready-report")
	complete := core.CompleteIssueRequest{IssueID: waiting.ID, CompleteRequest: acceptance("cutover-complete", continued.Issue.Revision, ready.ID)}
	outcome := mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	require.Equal(t, "completed", outcome.Issue.Status)
	require.Len(t, outcome.Issue.Reports, 2)
	require.Len(t, page(t, p, "session-bindings?issueId="+waiting.ID).Items, 1)
	require.Len(t, workEvents(t, p, "issue_session_update"), 1)
	require.Len(t, page(t, p, "outbox-messages").Items, 1)
	require.Len(t, page(t, p, "trigger-messages").Items, 2)
	require.Equal(t, "completed", readRecord[core.Issue](t, p, "issues/"+waiting.ID).Status)

	calls, err := os.ReadFile(filepath.Join(f.dir, "calls.ndjson"))
	require.NoError(t, err)
	sends, queries := 0, 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
		var args []string
		require.NoError(t, json.Unmarshal([]byte(line), &args))
		if args[0] == "+messages-query-send-status" {
			require.Contains(t, args, task)
			queries++
		} else {
			require.Equal(t, "+messages-reply", args[0])
			require.Contains(t, args, firstNative)
			require.Contains(t, args, question)
			sends++
		}
	}
	require.Equal(t, 1, sends)
	require.Positive(t, queries)
	status, err := exec.CommandContext(t.Context(), coreExe, "triggers", "dws", "status", "--endpoint", triggerStatus).Output()
	require.NoError(t, err)
	require.Contains(t, string(status), `"accepted":2`)
	require.Contains(t, string(status), `"backlog":0`)
	require.NotContains(t, string(status), "healthReporting")
	code, _ := request(t, http.MethodPost, p.endpoint, "/v1/channel/heartbeat", f.token, nil)
	require.Equal(t, http.StatusNotFound, code)
	p.stop(t, false)
	assertCutoverSchema(t, filepath.Join(f.data, "core.sqlite"))
	t.Logf("business issue=%s sessionBinding=%s completed; delivery=%s sent; Outbox=%s task=%s native=%s sent with one send and %d task queries",
		waiting.ID, x.ID, internalEvent.ID, reply.ID, task, questionNative, queries)
}

func writeCutoverFile(t *testing.T, path string, content []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path+".next", content, 0o600))
	require.NoError(t, os.Rename(path+".next", path))
}

func startCutoverTrigger(t *testing.T, f *fixture, coreEndpoint, superagent, dws string) string {
	t.Helper()
	config := filepath.Join(f.dir, "trigger.yaml")
	body, err := json.Marshal(map[string]any{
		"dwsExecutable": dws, "profile": "corp:account", "source": "dws", "accountId": "account",
		"selfOpenDingtalkId": "self", "instanceId": "collector-1",
		"coreEndpoint": coreEndpoint, "coreTokenEnv": "CUTOVER_TEST_TOKEN", "retryMillis": 25,
		"databasePath": filepath.Join(f.dir, "dws.sqlite"),
		"direct": map[string]any{
			"enabled": true,
			"match":   []map[string]string{{"regex": ".+"}},
		},
	})
	require.NoError(t, err)
	writeCutoverFile(t, config, body)
	logPath := filepath.Join(f.dir, "trigger.log")
	log, err := os.Create(logPath)
	require.NoError(t, err)
	cmd := exec.CommandContext(context.Background(), superagent, "triggers", "dws", "serve", "--config", config)
	cmd.Env = []string{
		"HOME=" + f.dir, "PATH=" + f.dir, "CUTOVER_TEST_TOKEN=" + f.token,
		"DWS_FIXTURE_MODE=feed", "DWS_FIXTURE_INPUT=" + f.dir, "DWS_FIXTURE_EGRESS=" + f.dir,
		"DWS_FIXTURE_PID=" + filepath.Join(f.dir, "dws.pid"),
	}
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			data, _ := os.ReadFile(logPath)
			require.NoError(t, err, "%s", data)
			if t.Failed() {
				t.Logf("DWS Trigger log:\n%s", data)
			}
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("Trigger did not stop")
		}
		require.NoError(t, log.Close())
		t.Logf("DWS Trigger process released pid=%d", cmd.Process.Pid)
	})
	var startup struct{ StatusEndpoint string }
	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(logPath)
		first, _, ok := strings.Cut(string(raw), "\n")
		return ok && json.Unmarshal([]byte(first), &startup) == nil && startup.StatusEndpoint != ""
	}, 6*time.Second, 50*time.Millisecond)
	return startup.StatusEndpoint
}
