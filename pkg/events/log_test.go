package events

import (
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventLog_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)

	require.NoError(t, log.Append("text", TextEvent{Text: "hello"}))
	require.NoError(t, log.Append("turn_end", TurnEndEvent{StopReason: "end_turn"}))
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, 0, entries[0].Seq)
	assert.Equal(t, "text", entries[0].Type)
	assert.Equal(t, 1, entries[1].Seq)
	assert.Equal(t, "turn_end", entries[1].Type)
	assert.NotEmpty(t, entries[0].Timestamp)
}

func TestEventLog_FromSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	log, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoError(t, log.Append("text", TextEvent{Text: "x"}))
	}
	require.NoError(t, log.Close())

	entries, err := ReadEventLog(path, 3)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, 3, entries[0].Seq)
	assert.Equal(t, 4, entries[1].Seq)
}

func TestEventLog_SeqContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// First session: write 3 entries.
	log1, err := OpenEventLog(path)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, log1.Append("text", TextEvent{Text: "a"}))
	}
	require.NoError(t, log1.Close())

	// Reopen (simulates shim restart appending to the same file).
	log2, err := OpenEventLog(path)
	require.NoError(t, err)
	require.NoError(t, log2.Append("turn_end", TurnEndEvent{StopReason: "end_turn"}))
	require.NoError(t, log2.Close())

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	// Seq must be monotonically increasing across the reopen.
	assert.Equal(t, 3, entries[3].Seq)
}

func TestReadEventLog_NonExistentFile(t *testing.T) {
	entries, err := ReadEventLog("/nonexistent/path/events.jsonl", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestEventLog_TranslatorWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(path)
	require.NoError(t, err)

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, evLog)
	ch, _ := tr.Subscribe()
	tr.Start()

	// Send an event and wait for it to be processed by the subscriber.
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "logged"}},
		},
	}}
	<-ch // wait for broadcast to complete before reading log

	tr.Stop()
	evLog.Close()

	entries, err := ReadEventLog(path, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "text", entries[0].Type)
}
