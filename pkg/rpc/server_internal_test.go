// Package rpc white-box tests for unexported helpers.
package rpc

import (
	"testing"

	"github.com/open-agent-d/open-agent-d/pkg/events"
	"github.com/stretchr/testify/assert"
)

// TestEventTypeName exercises every reachable branch of eventTypeName,
// driving coverage of the 11 concrete event types from ~15% to ~100%.
// The default branch is a safety net for future types; it cannot be reached
// from outside the sealed events package, so it is intentionally not tested.
func TestEventTypeName(t *testing.T) {
	cases := []struct {
		ev   events.Event
		want string
	}{
		{events.TextEvent{Text: "hi"}, "text"},
		{events.ThinkingEvent{Text: "hmm"}, "thinking"},
		{events.ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, "tool_call"},
		{events.ToolResultEvent{ID: "1", Status: "ok"}, "tool_result"},
		{events.FileWriteEvent{Path: "/a", Allowed: true}, "file_write"},
		{events.FileReadEvent{Path: "/b", Allowed: false}, "file_read"},
		{events.CommandEvent{Command: "ls", Allowed: true}, "command"},
		{events.PlanEvent{Entries: nil}, "plan"},
		{events.TurnStartEvent{}, "turn_start"},
		{events.TurnEndEvent{StopReason: "end_turn"}, "turn_end"},
		{events.ErrorEvent{Msg: "oops"}, "error"},
	}
	for _, tc := range cases {
		got := eventTypeName(tc.ev)
		assert.Equal(t, tc.want, got, "eventTypeName(%T)", tc.ev)
	}
}
