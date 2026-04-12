package shim

import (
	"testing"
	"time"

	"github.com/zoumo/oar/pkg/tui/chat"
)

func TestShimMessage_Basic(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	if m.GetID() != "msg-1" {
		t.Fatalf("GetID: got %q, want %q", m.GetID(), "msg-1")
	}
	if m.GetRole() != chat.RoleAssistant {
		t.Fatalf("GetRole: got %q, want %q", m.GetRole(), chat.RoleAssistant)
	}
	if m.Content().Text != "" {
		t.Fatal("initial content should be empty")
	}
	if m.IsFinished() {
		t.Fatal("should not be finished initially")
	}
	if m.IsThinking() {
		t.Fatal("should not be thinking initially")
	}
}

func TestShimMessage_AppendText(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	m.appendText("hello ")
	m.appendText("world")

	if got := m.Content().Text; got != "hello world" {
		t.Fatalf("Content().Text: got %q, want %q", got, "hello world")
	}
}

func TestShimMessage_AppendThinking(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	m.appendThinking("I should ")
	if !m.IsThinking() {
		t.Fatal("should be thinking after appendThinking")
	}

	m.appendThinking("look at the code")
	if got := m.ReasoningContent().Thinking; got != "I should look at the code" {
		t.Fatalf("Thinking: got %q", got)
	}
}

func TestShimMessage_ThinkingToText(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	m.appendThinking("thinking...")
	if !m.IsThinking() {
		t.Fatal("should be thinking")
	}

	m.appendText("answer")
	if m.IsThinking() {
		t.Fatal("should stop thinking after appendText")
	}
	if got := m.Content().Text; got != "answer" {
		t.Fatalf("text: got %q", got)
	}
	if got := m.ReasoningContent().Thinking; got != "thinking..." {
		t.Fatalf("thinking should be preserved: got %q", got)
	}
}

func TestShimMessage_Finish(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)
	m.appendText("done")

	m.finish(chat.FinishReasonEndTurn)

	if !m.IsFinished() {
		t.Fatal("should be finished")
	}
	if m.FinishReason() != chat.FinishReasonEndTurn {
		t.Fatalf("FinishReason: got %q", m.FinishReason())
	}
	if m.FinishPart() == nil {
		t.Fatal("FinishPart should not be nil when finished")
	}
}

func TestShimMessage_FinishPart_Nil_WhenNotFinished(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)
	if m.FinishPart() != nil {
		t.Fatal("FinishPart should be nil when not finished")
	}
}

func TestShimMessage_ThinkingDuration(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	// No thinking → zero duration.
	if m.ThinkingDuration() != 0 {
		t.Fatal("no thinking should mean zero duration")
	}

	// Start thinking.
	m.appendThinking("hmm")
	time.Sleep(10 * time.Millisecond)

	// Duration should be > 0 while thinking.
	d := m.ThinkingDuration()
	if d <= 0 {
		t.Fatalf("thinking duration should be > 0 during thinking, got %v", d)
	}

	// End thinking.
	m.appendText("answer")
	d2 := m.ThinkingDuration()
	if d2 <= 0 {
		t.Fatalf("thinking duration should be > 0 after thinking, got %v", d2)
	}

	// Duration should be frozen after thinking ends.
	time.Sleep(10 * time.Millisecond)
	d3 := m.ThinkingDuration()
	if d3 != d2 {
		t.Fatalf("duration should be frozen after thinking ends: %v != %v", d3, d2)
	}
}

func TestShimMessage_FinishStopsThinking(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)

	m.appendThinking("thinking...")
	m.finish(chat.FinishReasonCanceled)

	if m.IsThinking() {
		t.Fatal("finish should stop thinking")
	}
}

func TestShimMessage_ToolCalls(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)
	m.toolCalls = []chat.ToolCall{
		{ID: "tc-1", Name: "bash"},
	}

	if len(m.ToolCalls()) != 1 {
		t.Fatalf("ToolCalls len: got %d", len(m.ToolCalls()))
	}
	if m.ToolCalls()[0].Name != "bash" {
		t.Fatalf("ToolCalls[0].Name: got %q", m.ToolCalls()[0].Name)
	}
}

func TestShimMessage_ToolResults(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleTool)
	m.toolResults = []chat.ToolResult{
		{ToolCallID: "tc-1", Content: "ok"},
	}

	if len(m.ToolResults()) != 1 {
		t.Fatalf("ToolResults len: got %d", len(m.ToolResults()))
	}
}

func TestShimMessage_IsSummaryMessage(t *testing.T) {
	m := newShimMessage("msg-1", chat.RoleAssistant)
	if m.IsSummaryMessage() {
		t.Fatal("should not be summary by default")
	}
	m.summary = true
	if !m.IsSummaryMessage() {
		t.Fatal("should be summary when set")
	}
}
