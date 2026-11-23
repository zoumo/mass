package core_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/superagent/internal/triggercontent"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestSchema25TriggerMigrationPreservesReceiptsAndSendEvidence(t *testing.T) {
	f := fixtureAt(t)
	configureOutboxSource(t, f)
	p := f.start()
	requests := []core.SubmitTriggerMessageRequest{input("legacy-origin"), input("legacy-answer"), input("legacy-echo")}
	original := make([]core.Receipt, 0, len(requests))
	for _, req := range requests {
		original = append(original, submit(t, p, f, req))
	}
	p.stop(t, false)

	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.data, "core.sqlite")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	const oldAt = "2026-11-18T09:02:03.123456789+08:00"
	const oldURL = "https://github.example/org/repo/pull/42#comment"
	for i, req := range requests {
		text := req.Message.Content[0].Text.Text
		var msg map[string]any
		require.NoError(t, json.Unmarshal(fmt.Appendf(nil, `{"id":%q,"source":"dws","accountId":"account-1",
 "sourceMessageId":%q,"receivedAt":%q,"acceptance":"accepted","eventId":%q,"threadKey":%q,
 "threadRef":{"source":"dws","id":%q},"actor":"sender-1","addressed":"direct","text":%q,"at":%q,"url":%q,"channel":%s}`,
			original[i].MessageID, req.Message.SourceMessageID, original[i].AcceptedAt, original[i].EventID, req.Message.ThreadKey,
			fixtureConversation, text, oldAt, oldURL, req.ReplyTarget), &msg))
		if i == 1 {
			msg["quotedMessage"] = map[string]string{"message_id": "legacy-echo"}
		}
		if i == 2 {
			msg["echo"] = map[string]string{"status": "echo_unknown"}
			delete(msg, "eventId")
			_, err := db.ExecContext(t.Context(), `DELETE FROM internal_events WHERE event_id=?`, original[i].EventID)
			require.NoError(t, err)
			original[i].EventID = ""
			original[i].EchoStatus = "outbound_echo"
		}
		raw, err := json.Marshal(msg)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `UPDATE trigger_messages SET canonical=?,body=? WHERE id=?`, raw, raw, original[i].MessageID)
		require.NoError(t, err)
		// Old DWS queue replay keeps actor and time in text and appends an untyped URL.
		requests[i].ReplyTarget = json.RawMessage(fmt.Sprintf(`{"address":null,"type":"dws","conversationId":%q,"messageId":%q,"actorId":"sender-1"}`,
			fixtureConversation, req.Message.SourceMessageID))
		requests[i].Message.Content = []events.ContentBlock{
			events.Text(text + "\n\nAuthor: sender-1\n\nSource time: 2026-11-18T01:02:03.123456789Z"), triggercontent.WebLink(oldURL),
		}
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO outbox(id,message_id,content,status,receipt) VALUES
 ('legacy-reply',?,'{"type":"markdown","data":{"text":"Question?"}}','pending','{"taskId":"legacy-task"}');
 PRAGMA user_version=25;`, original[0].MessageID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	export := filepath.Join(f.data, "migration", "v25-dws-sent-evidence.ndjson")
	assertLegacy := func() {
		t.Helper()
		var version int
		var nativeJSON string
		require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
		require.Equal(t, 25, version)
		require.NoError(t, db.QueryRowContext(t.Context(), `SELECT body FROM trigger_messages WHERE id=?`, original[0].MessageID).Scan(&nativeJSON))
		require.Contains(t, nativeJSON, `"channel"`)
		require.NoFileExists(t, export)
	}
	f.fails("reconcile legacy echo for Message " + original[2].MessageID)
	assertLegacy()
	_, err = db.ExecContext(t.Context(), `UPDATE trigger_messages SET body=json_set(body,'$.echo',json('{"status":"outbound_echo","match":{"outboxId":"legacy-reply"}}')) WHERE id=?`, original[2].MessageID)
	require.NoError(t, err)
	f.fails("settle the pending DWS Outbox task")
	assertLegacy()
	_, err = db.ExecContext(t.Context(), `UPDATE outbox SET status='sent',receipt=json_object('taskId','legacy-task','conversationId',?,'messageId','legacy-echo')
 WHERE id='legacy-reply'`, fixtureConversation)
	require.NoError(t, err)
	requests[1].InReplyToOutboxID = "legacy-reply"

	for range 2 {
		p = f.start()
		var version int
		require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
		require.Equal(t, 26, version)
		for i, req := range requests {
			want := original[i]
			want.Duplicate = true
			require.Equal(t, want, submit(t, p, f, req))
		}
		require.Len(t, page(t, p, "routing-events").Items, 2)
		gotQuote := readRecord[quotedReplyRecord](t, p, "messages/"+original[1].MessageID)
		require.NotNil(t, gotQuote.QuotedReply)
		require.Equal(t, "legacy-reply", gotQuote.QuotedReply.OutboxID)
		sent := readOutbox(t, p, "legacy-reply")
		require.Equal(t, "sent", sent.Status)
		require.Equal(t, &core.OutboxReceipt{TaskID: "legacy-task"}, sent.Receipt)
		raw, err := os.ReadFile(export)
		require.NoError(t, err)
		require.JSONEq(t, fmt.Sprintf(`{"source":"dws","accountId":"account-1","outboxId":"legacy-reply","conversationId":%q,"messageId":"legacy-echo","taskId":"legacy-task"}`,
			fixtureConversation), string(raw))
		p.stop(t, false)
	}
}
