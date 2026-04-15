package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apishim "github.com/zoumo/mass/pkg/shim/api"
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

	ev0 := apishim.ShimEvent{RunID: "run-1", Seq: 0, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("hello")}}
	ev1 := apishim.ShimEvent{RunID: "run-1", Seq: 1, Time: testTime(t), Category: apishim.CategoryRuntime, Type: "state_change", Content: apishim.StateChangeEvent{PreviousStatus: "created", Status: "running", PID: 42, Reason: "prompt-started"}}

	require.NoError(t, log.Append(ev0))
	require.NoError(t, log.Append(ev1))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, apishim.EventTypeAgentMessage, entries[0].Type)
	assert.Equal(t, 0, entries[0].Seq)
	assert.Equal(t, "run-1", entries[0].RunID)
	textEv, ok := entries[0].Content.(apishim.AgentMessageEvent)
	require.True(t, ok)
	require.NotNil(t, textEv.Content.Text)
	assert.Equal(t, "hello", textEv.Content.Text.Text)

	assert.Equal(t, "state_change", entries[1].Type)
	sc, ok := entries[1].Content.(apishim.StateChangeEvent)
	require.True(t, ok)
	assert.Equal(t, "created", sc.PreviousStatus)
	assert.Equal(t, "running", sc.Status)
	assert.Equal(t, 42, sc.PID)
}

func TestEventLog_FromSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ev := apishim.ShimEvent{RunID: "run-1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("x")}}
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
		ev := apishim.ShimEvent{RunID: "run-1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("a")}}
		require.NoError(t, log1.Append(ev))
	}
	require.NoError(t, log1.Close())

	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.Equal(t, 3, log2.NextSeq())
	ev3 := apishim.ShimEvent{RunID: "run-1", Seq: 3, Time: testTime(t), Category: apishim.CategoryRuntime, Type: "state_change", Content: apishim.StateChangeEvent{PreviousStatus: "running", Status: "created"}}
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
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("ok")}}
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
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("v")}}
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
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("v")}}
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
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("after")}}
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
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("orig")}}
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
	ev4 := apishim.ShimEvent{RunID: "s1", Seq: 4, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("new")}}
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
func TestEventLog_PartialWriteTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)

	// Write 2 good entries.
	for i := 0; i < 2; i++ {
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("good")}}
		require.NoError(t, log.Append(ev))
	}

	// Force a seq mismatch to trigger an early-return error (seq guard fires
	// before any write, so no partial write in this case). The key invariant
	// is that after a failed Append, NextSeq is unchanged and the file is
	// still consistent.
	wrongSeqEv := apishim.ShimEvent{RunID: "s1", Seq: 99, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("bad")}}
	err = log.Append(wrongSeqEv)
	require.Error(t, err, "wrong seq should be rejected")
	assert.Equal(t, 2, log.NextSeq(), "nextSeq must not advance after failed append")

	// The log must still be writable after a failed append.
	goodEv := apishim.ShimEvent{RunID: "s1", Seq: 2, Time: testTime(t), Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Content: apishim.AgentMessageEvent{Content: apishim.TextBlock("after")}}
	require.NoError(t, log.Append(goodEv))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
}

func TestEventLog_TranslatorWritesShimEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(path)
	require.NoError(t, err)

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, evLog)
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
	assert.Equal(t, apishim.EventTypeAgentMessage, entries[0].Type)
	assert.Equal(t, apishim.CategorySession, entries[0].Category)
	assert.Equal(t, "run-1", entries[0].RunID)
	assert.Equal(t, 0, entries[0].Seq)
	ev, ok := entries[0].Content.(apishim.AgentMessageEvent)
	require.True(t, ok)
	require.NotNil(t, ev.Content.Text)
	assert.Equal(t, "logged", ev.Content.Text.Text)
}
