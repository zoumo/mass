package component

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

// stubMessage is a minimal Message implementation for data-logic tests.
type stubMessage struct {
	id           string
	role         MessageRole
	content      string
	thinking     string
	isThinking   bool
	isFinished   bool
	finishReason FinishReason
	toolCalls    []ToolCall
	toolResults  []ToolResult
}

func (s *stubMessage) GetID() string                    { return s.id }
func (s *stubMessage) GetRole() MessageRole             { return s.role }
func (s *stubMessage) Content() ContentBlock            { return ContentBlock{Text: s.content} }
func (s *stubMessage) ReasoningContent() ReasoningBlock { return ReasoningBlock{Thinking: s.thinking} }
func (s *stubMessage) IsThinking() bool                 { return s.isThinking }
func (s *stubMessage) IsFinished() bool                 { return s.isFinished }
func (s *stubMessage) FinishReason() FinishReason       { return s.finishReason }
func (s *stubMessage) FinishPart() *FinishPart          { return nil }
func (s *stubMessage) ToolCalls() []ToolCall            { return s.toolCalls }
func (s *stubMessage) ToolResults() []ToolResult        { return s.toolResults }
func (s *stubMessage) ThinkingDuration() time.Duration  { return 0 }
func (s *stubMessage) IsSummaryMessage() bool           { return false }

// ── ShouldRenderAssistantMessage ────────────────────────────────────────────

func TestShouldRender_NoToolCalls(t *testing.T) {
	msg := &stubMessage{role: RoleAssistant, content: ""}
	assert.True(t, ShouldRenderAssistantMessage(msg), "no tool calls → always render")
}

func TestShouldRender_ToolOnlyMessage(t *testing.T) {
	msg := &stubMessage{
		role:      RoleAssistant,
		toolCalls: []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.False(t, ShouldRenderAssistantMessage(msg), "tool-only, no text → skip")
}

func TestShouldRender_ToolWithText(t *testing.T) {
	msg := &stubMessage{
		role:      RoleAssistant,
		content:   "here is the result",
		toolCalls: []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.True(t, ShouldRenderAssistantMessage(msg), "tool + text → render")
}

func TestShouldRender_ToolWithThinking(t *testing.T) {
	msg := &stubMessage{
		role:      RoleAssistant,
		thinking:  "let me think",
		toolCalls: []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.True(t, ShouldRenderAssistantMessage(msg), "tool + thinking → render")
}

func TestShouldRender_ToolStillThinking(t *testing.T) {
	msg := &stubMessage{
		role:       RoleAssistant,
		isThinking: true,
		toolCalls:  []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.True(t, ShouldRenderAssistantMessage(msg), "tool + isThinking → render")
}

func TestShouldRender_ToolWithError(t *testing.T) {
	msg := &stubMessage{
		role:         RoleAssistant,
		finishReason: FinishReasonError,
		toolCalls:    []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.True(t, ShouldRenderAssistantMessage(msg), "tool + error → render")
}

func TestShouldRender_ToolCanceled(t *testing.T) {
	msg := &stubMessage{
		role:         RoleAssistant,
		finishReason: FinishReasonCanceled,
		toolCalls:    []ToolCall{{ID: "tc-1", Name: "read"}},
	}
	assert.True(t, ShouldRenderAssistantMessage(msg), "tool + canceled → render")
}

// ── BuildToolResultMap ──────────────────────────────────────────────────────

func TestBuildToolResultMap_Empty(t *testing.T) {
	m := BuildToolResultMap(nil)
	assert.Empty(t, m)
}

func TestBuildToolResultMap_IgnoresNonToolRole(t *testing.T) {
	msgs := []Message{
		&stubMessage{role: RoleUser, toolResults: []ToolResult{{ToolCallID: "tc-1"}}},
		&stubMessage{role: RoleAssistant, toolResults: []ToolResult{{ToolCallID: "tc-2"}}},
	}
	m := BuildToolResultMap(msgs)
	assert.Empty(t, m)
}

func TestBuildToolResultMap_CollectsToolResults(t *testing.T) {
	msgs := []Message{
		&stubMessage{role: RoleTool, toolResults: []ToolResult{
			{ToolCallID: "tc-1", Content: "result1"},
			{ToolCallID: "tc-2", Content: "result2"},
		}},
		&stubMessage{role: RoleTool, toolResults: []ToolResult{
			{ToolCallID: "tc-3", Content: "result3"},
		}},
	}
	m := BuildToolResultMap(msgs)
	assert.Len(t, m, 3)
	assert.Equal(t, "result1", m["tc-1"].Content)
	assert.Equal(t, "result2", m["tc-2"].Content)
	assert.Equal(t, "result3", m["tc-3"].Content)
}

func TestBuildToolResultMap_SkipsEmptyToolCallID(t *testing.T) {
	msgs := []Message{
		&stubMessage{role: RoleTool, toolResults: []ToolResult{
			{ToolCallID: "", Content: "orphan"},
			{ToolCallID: "tc-1", Content: "valid"},
		}},
	}
	m := BuildToolResultMap(msgs)
	assert.Len(t, m, 1)
	assert.Equal(t, "valid", m["tc-1"].Content)
}

// ── ExtractMessageItems ─────────────────────────────────────────────────────

func TestExtractMessageItems_UserMessage(t *testing.T) {
	sty := styles.DefaultStyles()
	msg := &stubMessage{id: "u1", role: RoleUser, content: "hello"}
	items := ExtractMessageItems(&sty, msg, nil)
	require.Len(t, items, 1)
	assert.Equal(t, "u1", items[0].ID())
}

func TestExtractMessageItems_AssistantWithText(t *testing.T) {
	sty := styles.DefaultStyles()
	msg := &stubMessage{id: "a1", role: RoleAssistant, content: "response"}
	items := ExtractMessageItems(&sty, msg, nil)
	require.Len(t, items, 1)
	// AssistantMessageItem
	_, ok := items[0].(*AssistantMessageItem)
	assert.True(t, ok)
}

func TestExtractMessageItems_AssistantWithTools(t *testing.T) {
	sty := styles.DefaultStyles()
	msg := &stubMessage{
		id:   "a2",
		role: RoleAssistant,
		toolCalls: []ToolCall{
			{ID: "tc-1", Name: "read", Finished: true},
			{ID: "tc-2", Name: "write", Finished: true},
		},
	}
	results := map[string]ToolResult{
		"tc-1": {ToolCallID: "tc-1", Content: "file data"},
	}
	items := ExtractMessageItems(&sty, msg, results)
	// Tool-only → no AssistantMessageItem, 2 ToolMessageItems
	assert.Len(t, items, 2)
}

func TestExtractMessageItems_AssistantToolsWithText(t *testing.T) {
	sty := styles.DefaultStyles()
	msg := &stubMessage{
		id:      "a3",
		role:    RoleAssistant,
		content: "here is the result",
		toolCalls: []ToolCall{
			{ID: "tc-1", Name: "read", Finished: true},
		},
	}
	items := ExtractMessageItems(&sty, msg, nil)
	// AssistantMessageItem + 1 ToolMessageItem
	assert.Len(t, items, 2)
	_, isAssistant := items[0].(*AssistantMessageItem)
	assert.True(t, isAssistant)
}

func TestExtractMessageItems_UnknownRole(t *testing.T) {
	sty := styles.DefaultStyles()
	msg := &stubMessage{id: "x", role: MessageRole("unknown")}
	items := ExtractMessageItems(&sty, msg, nil)
	assert.Empty(t, items)
}
