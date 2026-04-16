package chat

import (
	"testing"
	"time"

	"github.com/zoumo/mass/pkg/tui/component"
)

func TestStreamingMessage_Basic(t *testing.T) {
	m := NewStreamingMessage("msg-1", component.RoleAssistant)
	if m.GetID() != "msg-1" {
		t.Fatalf("want id msg-1, got %s", m.GetID())
	}
	if m.GetRole() != component.RoleAssistant {
		t.Fatalf("want role assistant, got %s", m.GetRole())
	}
	if m.Content().Text != "" {
		t.Fatal("new message should have empty text")
	}
	if m.IsFinished() {
		t.Fatal("new message should not be finished")
	}
}

func TestStreamingMessage_AppendText(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendText("hello ")
	m.AppendText("world")
	if m.Content().Text != "hello world" {
		t.Fatalf("want 'hello world', got %q", m.Content().Text)
	}
}

func TestStreamingMessage_AppendThinking(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendThinking("hmm ")
	m.AppendThinking("yes")
	if !m.IsThinking() {
		t.Fatal("should be thinking")
	}
	if m.ReasoningContent().Thinking != "hmm yes" {
		t.Fatalf("want 'hmm yes', got %q", m.ReasoningContent().Thinking)
	}
}

func TestStreamingMessage_ThinkingToText(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendThinking("think")
	if !m.IsThinking() {
		t.Fatal("should be thinking after AppendThinking")
	}
	m.AppendText("answer")
	if m.IsThinking() {
		t.Fatal("should stop thinking after AppendText")
	}
}

func TestStreamingMessage_Finish(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendText("done")
	m.Finish(component.FinishReasonEndTurn)
	if !m.IsFinished() {
		t.Fatal("should be finished")
	}
	if m.FinishReason() != component.FinishReasonEndTurn {
		t.Fatalf("want end_turn, got %s", m.FinishReason())
	}
}

func TestStreamingMessage_FinishPart_Nil_WhenNotFinished(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	if m.FinishPart() != nil {
		t.Fatal("FinishPart should be nil before finish")
	}
}

func TestStreamingMessage_ThinkingDuration(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendThinking("t")
	time.Sleep(10 * time.Millisecond)
	m.AppendText("a")
	d := m.ThinkingDuration()
	if d < 10*time.Millisecond {
		t.Fatalf("thinking duration too short: %v", d)
	}
}

func TestStreamingMessage_FinishStopsThinking(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	m.AppendThinking("t")
	m.Finish(component.FinishReasonEndTurn)
	if m.IsThinking() {
		t.Fatal("finish should stop thinking")
	}
}

func TestStreamingMessage_ToolCalls(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	if len(m.ToolCalls()) != 0 {
		t.Fatal("should have no tool calls initially")
	}
	m.toolCalls = append(m.toolCalls, component.ToolCall{ID: "tc1"})
	if len(m.ToolCalls()) != 1 {
		t.Fatal("should have 1 tool call")
	}
}

func TestStreamingMessage_ToolResults(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	if len(m.ToolResults()) != 0 {
		t.Fatal("should have no results initially")
	}
	m.toolResults = append(m.toolResults, component.ToolResult{ToolCallID: "tc1"})
	if len(m.ToolResults()) != 1 {
		t.Fatal("should have 1 result")
	}
}

func TestStreamingMessage_IsSummaryMessage(t *testing.T) {
	m := NewStreamingMessage("m", component.RoleAssistant)
	if m.IsSummaryMessage() {
		t.Fatal("should not be summary by default")
	}
	m.summary = true
	if !m.IsSummaryMessage() {
		t.Fatal("should be summary after set")
	}
}

func TestFinishedStreamingMessage(t *testing.T) {
	m := NewFinishedStreamingMessage("u-1", component.RoleUser, "hello")
	if m.Content().Text != "hello" {
		t.Fatalf("want 'hello', got %q", m.Content().Text)
	}
	if !m.IsFinished() {
		t.Fatal("should be finished")
	}
}
