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

func TestReadEventLog_CorruptRowFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{not-json}\n"), 0o644))

	_, err := ReadEventLog(path, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode log entry")
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
