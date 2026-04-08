package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.NoError(t, log.Append(NewSessionUpdateEnvelope("session-1", 0, testTime(t), TextEvent{Text: "hello"})))
	require.NoError(t, log.Append(NewRuntimeStateChangeEnvelope("session-1", 1, testTime(t), "created", "running", 42, "prompt-started")))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, MethodSessionUpdate, entries[0].Method)
	seq, err := entries[0].Seq()
	require.NoError(t, err)
	assert.Equal(t, 0, seq)

	update, ok := entries[0].Params.(SessionUpdateParams)
	require.True(t, ok)
	assert.Equal(t, "session-1", update.SessionID)
	require.IsType(t, TextEvent{}, update.Event.Payload)
	assert.Equal(t, TextEvent{Text: "hello"}, update.Event.Payload)

	stateChange, ok := entries[1].Params.(RuntimeStateChangeParams)
	require.True(t, ok)
	assert.Equal(t, "created", stateChange.PreviousStatus)
	assert.Equal(t, "running", stateChange.Status)
	assert.Equal(t, 42, stateChange.PID)
}

func TestEventLog_FromSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoError(t, log.Append(NewSessionUpdateEnvelope("session-1", i, testTime(t), TextEvent{Text: "x"})))
	}
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 3)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	seq, err := entries[0].Seq()
	require.NoError(t, err)
	assert.Equal(t, 3, seq)
}

func TestEventLog_SeqContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log1, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, log1.Append(NewSessionUpdateEnvelope("session-1", i, testTime(t), TextEvent{Text: "a"})))
	}
	require.NoError(t, log1.Close())

	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.Equal(t, 3, log2.NextSeq())
	require.NoError(t, log2.Append(NewRuntimeStateChangeEnvelope("session-1", 3, testTime(t), "running", "created", 0, "prompt-completed")))
	require.NoError(t, log2.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	seq, err := entries[3].Seq()
	require.NoError(t, err)
	assert.Equal(t, 3, seq)
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
		require.NoError(t, log.Append(NewSessionUpdateEnvelope("s1", i, testTime(t), TextEvent{Text: "ok"})))
	}
	require.NoError(t, log.Close())

	// Append a truncated JSON line (simulating a crash mid-write).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"method":"session/update","params":{"seq":3,` + "\n")
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
		require.NoError(t, log.Append(NewSessionUpdateEnvelope("s1", i, testTime(t), TextEvent{Text: "v"})))
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
		seq, err := e.Seq()
		require.NoError(t, err)
		assert.Equal(t, i, seq)
	}
}

func TestReadEventLog_MidFileCorruptionFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Write 2 valid entries.
	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, log.Append(NewSessionUpdateEnvelope("s1", i, testTime(t), TextEvent{Text: "v"})))
	}
	require.NoError(t, log.Close())

	// Read the existing content, inject a corrupt line, then add 2 more valid entries.
	existing, err := os.ReadFile(path)
	require.NoError(t, err)

	// Build the file: 2 valid + 1 corrupt + 2 valid.
	var buf []byte
	buf = append(buf, existing...)
	buf = append(buf, []byte("CORRUPT-LINE\n")...)

	// We need 2 more valid entries with seq 3 and 4.
	// Write them via a temporary log to get valid JSONL.
	tmpPath := filepath.Join(dir, "tmp.jsonl")
	tmpLog, err := OpenEventLog(tmpPath)
	require.NoError(t, err)
	// countLines of tmp file = 0, so seq starts at 0; we just need valid JSON lines.
	require.NoError(t, tmpLog.Append(NewSessionUpdateEnvelope("s1", 0, testTime(t), TextEvent{Text: "after"})))
	require.NoError(t, tmpLog.Append(NewSessionUpdateEnvelope("s1", 1, testTime(t), TextEvent{Text: "after"})))
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
		require.NoError(t, log1.Append(NewSessionUpdateEnvelope("s1", i, testTime(t), TextEvent{Text: "orig"})))
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
	// The corrupt line is a "lost slot" (seq 3 is gone).
	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.Equal(t, 4, log2.NextSeq())

	// Append with seq 4 succeeds — the append path is unaffected by the damage.
	require.NoError(t, log2.Append(NewSessionUpdateEnvelope("s1", 4, testTime(t), TextEvent{Text: "new"})))
	require.NoError(t, log2.Close())

	// After appending, the corrupt line is now mid-file (valid lines follow it).
	// ReadEventLog correctly identifies this as mid-file corruption.
	_, err = ReadEventLog(path, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mid-file corruption")

	// However, the file has 5 non-empty lines and OpenEventLog still works.
	log3, err := OpenEventLog(path)
	require.NoError(t, err)
	assert.Equal(t, 5, log3.NextSeq())
	require.NoError(t, log3.Close())
}

func TestEventLog_TranslatorWritesCanonicalEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(path)
	require.NoError(t, err)

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, evLog)
	ch, _, _ := tr.Subscribe()
	tr.Start()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "logged"}},
		},
	}}
	<-ch

	tr.Stop()
	require.NoError(t, evLog.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, MethodSessionUpdate, entries[0].Method)
	params := entries[0].Params.(SessionUpdateParams)
	assert.Equal(t, "session-1", params.SessionID)
	assert.Equal(t, TextEvent{Text: "logged"}, params.Event.Payload)
}
