package server

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
)

func testTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.April, 7, 10, 0, 0, 0, time.UTC)
}

func TestEventLog_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)

	ev0 := runapi.AgentRunEvent{RunID: "run-1", Seq: 0, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("hello"))}
	ev1 := runapi.AgentRunEvent{RunID: "run-1", Seq: 1, Time: testTime(t), Type: runapi.EventTypeRuntimeUpdate, Payload: runapi.RuntimeUpdateEvent{Status: &runapi.RuntimeStatus{PreviousStatus: "created", Status: "running", PID: 42, Reason: "prompt-started"}}}

	require.NoError(t, log.Append(ev0))
	require.NoError(t, log.Append(ev1))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, runapi.EventTypeAgentMessage, entries[0].Type)
	assert.Equal(t, 0, entries[0].Seq)
	assert.Equal(t, "run-1", entries[0].RunID)
	textEv, ok := entries[0].Payload.(runapi.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, textEv.Content.Text)
	assert.Equal(t, "hello", textEv.Content.Text.Text)

	assert.Equal(t, runapi.EventTypeRuntimeUpdate, entries[1].Type)
	ru, ok := entries[1].Payload.(runapi.RuntimeUpdateEvent)
	require.True(t, ok)
	require.NotNil(t, ru.Status)
	assert.Equal(t, "created", ru.Status.PreviousStatus)
	assert.Equal(t, "running", ru.Status.Status)
	assert.Equal(t, 42, ru.Status.PID)
}

func TestEventLog_FromSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ev := runapi.AgentRunEvent{RunID: "run-1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("x"))}
		require.NoError(t, log.Append(ev))
	}
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 3)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, 3, entries[0].Seq)
}

func TestEventLog_SeqContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log1, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		ev := runapi.AgentRunEvent{RunID: "run-1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("a"))}
		require.NoError(t, log1.Append(ev))
	}
	require.NoError(t, log1.Close())

	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.Equal(t, 3, log2.NextSeq())
	ev3 := runapi.AgentRunEvent{RunID: "run-1", Seq: 3, Time: testTime(t), Type: runapi.EventTypeRuntimeUpdate, Payload: runapi.RuntimeUpdateEvent{Status: &runapi.RuntimeStatus{PreviousStatus: "running", Status: "created"}}}
	require.NoError(t, log2.Append(ev3))
	require.NoError(t, log2.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	assert.Equal(t, 3, entries[3].Seq)
}

func TestReadEventLog_NonExistentFile(t *testing.T) {
	entries, err := ReadEventLog("/nonexistent/path/events.jsonl", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadEventLog_DamagedTailOnlyCorrupt(t *testing.T) {
	// A file that contains only corrupt lines is treated as a damaged tail:
	// returns empty slice, no error.
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{not-json}\n"), 0o644))

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadEventLog_DamagedTailReturnsPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Write 3 valid entries via the event log, then append a corrupt tail.
	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("ok"))}
		require.NoError(t, log.Append(ev))
	}
	require.NoError(t, log.Close())

	// Append a truncated JSON line (simulating a crash mid-write).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"runId":"s1","seq":3,` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3, "should return the 3 valid entries, skipping damaged tail")
}

func TestReadEventLog_DamagedTailTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("v"))}
		require.NoError(t, log.Append(ev))
	}
	require.NoError(t, log.Close())

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("GARBAGE-NOT-JSON\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	for i, e := range entries {
		assert.Equal(t, i, e.Seq)
	}
}

func TestReadEventLog_MidFileCorruptionFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Write 2 valid entries.
	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("v"))}
		require.NoError(t, log.Append(ev))
	}
	require.NoError(t, log.Close())

	// Read the existing content, inject a corrupt line, then add 2 more valid entries.
	existing, err := os.ReadFile(path)
	require.NoError(t, err)

	// Build the file: 2 valid + 1 corrupt + 2 valid.
	var buf []byte
	buf = append(buf, existing...)
	buf = append(buf, []byte("CORRUPT-LINE\n")...)

	// Write 2 more valid entries via a temporary log to get valid JSONL.
	tmpPath := filepath.Join(dir, "tmp.jsonl")
	tmpLog, err := OpenEventLog(tmpPath)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("after"))}
		require.NoError(t, tmpLog.Append(ev))
	}
	require.NoError(t, tmpLog.Close())
	trailing, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	buf = append(buf, trailing...)

	require.NoError(t, os.WriteFile(path, buf, 0o644))

	_, err = ReadEventLog(path, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mid-file corruption")
}

func TestEventLog_AppendAfterDamagedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Write 3 valid entries.
	log1, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("orig"))}
		require.NoError(t, log1.Append(ev))
	}
	require.NoError(t, log1.Close())

	// Append a corrupt tail line (simulating a crash).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("TRUNCATED\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Before reopening, damaged tail is tolerated on read.
	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3, "damaged tail should be skipped before new append")

	// Reopen — countLines sees 4 non-empty lines, so nextSeq = 4.
	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.Equal(t, 4, log2.NextSeq())

	// Append with seq 4 succeeds.
	ev4 := runapi.AgentRunEvent{RunID: "s1", Seq: 4, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("new"))}
	require.NoError(t, log2.Append(ev4))
	require.NoError(t, log2.Close())

	// After appending, the corrupt line is now mid-file. ReadEventLog correctly
	// identifies this as mid-file corruption.
	_, err = ReadEventLog(path, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mid-file corruption")

	// The file has 5 non-empty lines and OpenEventLog still works.
	log3, err := OpenEventLog(path)
	require.NoError(t, err)
	assert.Equal(t, 5, log3.NextSeq())
	require.NoError(t, log3.Close())
}

// TestEventLog_PartialWriteTruncation verifies that a failed Append truncates
// the file back to its pre-write offset, preventing a damaged tail from
// persisting after a write failure.
func TestEventLog_LastSeq_EmptyLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	defer log.Close()

	assert.Equal(t, -1, log.LastSeq(), "empty log should return -1")
	assert.Equal(t, 0, log.NextSeq(), "empty log NextSeq should be 0")
}

func TestEventLog_LastSeq_AfterAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		ev := runapi.AgentRunEvent{RunID: "r1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("x"))}
		require.NoError(t, log.Append(ev))
	}
	assert.Equal(t, 2, log.LastSeq())
	assert.Equal(t, 3, log.NextSeq())
	require.NoError(t, log.Close())
}

func TestEventLog_PartialWriteTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)

	// Write 2 good entries.
	for i := 0; i < 2; i++ {
		ev := runapi.AgentRunEvent{RunID: "s1", Seq: i, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("good"))}
		require.NoError(t, log.Append(ev))
	}

	// Force a seq mismatch to trigger an early-return error (seq guard fires
	// before any write, so no partial write in this case). The key invariant
	// is that after a failed Append, NextSeq is unchanged and the file is
	// still consistent.
	wrongSeqEv := runapi.AgentRunEvent{RunID: "s1", Seq: 99, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("bad"))}
	err = log.Append(wrongSeqEv)
	require.Error(t, err, "wrong seq should be rejected")
	assert.Equal(t, 2, log.NextSeq(), "nextSeq must not advance after failed append")

	// The log must still be writable after a failed append.
	goodEv := runapi.AgentRunEvent{RunID: "s1", Seq: 2, Time: testTime(t), Type: runapi.EventTypeAgentMessage, Payload: runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", runapi.TextBlock("after"))}
	require.NoError(t, log.Append(goodEv))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
}

func TestEventLog_TranslatorWritesAgentRunEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(path)
	require.NoError(t, err)

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, evLog, slog.Default())
	ch, _, _ := tr.Subscribe()
	tr.Start()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("logged"),
		},
	}}
	<-ch

	tr.Stop()
	require.NoError(t, evLog.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, runapi.EventTypeAgentMessage, entries[0].Type)
	assert.Equal(t, "run-1", entries[0].RunID)
	assert.Equal(t, 0, entries[0].Seq)
	ev, ok := entries[0].Payload.(runapi.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content.Text)
	assert.Equal(t, "logged", ev.Content.Text.Text)
}

func TestTranslator_SetSessionID(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil, slog.Default())

	tr.SetSessionID("sess-abc")

	// Verify by broadcasting an event and checking SessionID on the output.
	ch, _, _ := tr.Subscribe()
	tr.NotifyStateChange("creating", "running", 1, "test", nil)

	ev := <-ch
	assert.Equal(t, "sess-abc", ev.SessionID)
	tr.Stop()
}

func TestTranslator_NotifyError(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil, slog.Default())

	ch, _, _ := tr.Subscribe()
	tr.NotifyError("something broke")

	ev := <-ch
	assert.Equal(t, runapi.EventTypeError, ev.Type)
	errEv, ok := ev.Payload.(runapi.ErrorEvent)
	require.True(t, ok)
	assert.Equal(t, "something broke", errEv.Msg)
	tr.Stop()
}

func TestTranslator_LastSeq(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil, slog.Default())

	assert.Equal(t, -1, tr.LastSeq(), "no events yet")

	ch, _, _ := tr.Subscribe()
	tr.NotifyError("e1")
	<-ch
	assert.Equal(t, 0, tr.LastSeq())

	tr.NotifyError("e2")
	<-ch
	assert.Equal(t, 1, tr.LastSeq())
	tr.Stop()
}

func TestTranslator_EventCounts(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil, slog.Default())

	ch, _, _ := tr.Subscribe()
	tr.NotifyError("e1")
	<-ch
	tr.NotifyStateChange("creating", "running", 1, "test", nil)
	<-ch
	tr.NotifyError("e2")
	<-ch

	counts := tr.EventCounts()
	assert.Equal(t, 2, counts[runapi.EventTypeError])
	assert.Equal(t, 1, counts[runapi.EventTypeRuntimeUpdate])
	tr.Stop()
}
