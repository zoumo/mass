package chat

import (
	"strings"
	"testing"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

func TestToolMessageItem_FinishedNotSpinning(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{ID: "tc-1", Name: "Read", Input: `{"path":"main.go"}`, Finished: true}
	item := NewToolMessageItem(&sty, "item-1", tc, nil, false)

	// With Finished=true, the tool should NOT be spinning.
	base := item.(*GenericToolMessageItem).baseToolMessageItem
	if base.isSpinning() {
		t.Fatal("tool with Finished=true should not be spinning")
	}
}

func TestToolMessageItem_UnfinishedIsSpinning(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{ID: "tc-1", Name: "Read", Input: `{"path":"main.go"}`, Finished: false}
	item := NewToolMessageItem(&sty, "item-1", tc, nil, false)

	base := item.(*GenericToolMessageItem).baseToolMessageItem
	if !base.isSpinning() {
		t.Fatal("tool with Finished=false should be spinning")
	}
}

func TestToolMessageItem_CanceledNotSpinning(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{ID: "tc-1", Name: "Read", Input: `{"path":"main.go"}`, Finished: false}
	item := NewToolMessageItem(&sty, "item-1", tc, nil, true) // canceled=true

	base := item.(*GenericToolMessageItem).baseToolMessageItem
	if base.isSpinning() {
		t.Fatal("canceled tool should not be spinning")
	}
}

func TestToolMessageItem_RenderShowsName(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{ID: "tc-1", Name: "bash", Input: `{"command":"ls -la"}`, Finished: true}
	item := NewToolMessageItem(&sty, "item-1", tc, nil, false)

	rendered := item.Render(80)
	// Should contain the tool name (prettified).
	if !strings.Contains(rendered, "Bash") {
		t.Fatalf("rendered tool should contain 'Bash', got: %q", rendered)
	}
}

func TestToolMessageItem_StatusUpdate(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{ID: "tc-1", Name: "Read", Input: `{"path":"main.go"}`, Finished: true}
	item := NewToolMessageItem(&sty, "item-1", tc, nil, false)

	ti := item.(ToolMessageItem)
	ti.SetStatus(ToolStatusSuccess)
	if ti.Status() != ToolStatusSuccess {
		t.Fatalf("expected ToolStatusSuccess, got %d", ti.Status())
	}

	ti.SetResult(&ToolResult{ToolCallID: "tc-1", Content: "file contents"})
	rendered := item.Render(80)
	if rendered == "" {
		t.Fatal("rendered should not be empty after setting result")
	}
}
