package chat

import (
	"strings"
	"testing"

	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/styles"
)

func TestGenericPrettyName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bash", "Bash"},
		{"file_read", "File Read"},
		{"mcp-tool", "Mcp Tool"},
		{"Read", "Read"},
	}
	for _, tt := range tests {
		got := genericPrettyName(tt.input)
		if got != tt.want {
			t.Errorf("genericPrettyName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenericToolRender_ParamsExpanded(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{
		ID:       "tc-1",
		Name:     "Read",
		Input:    `{"title":"pkg/main.go"}`,
		Finished: true,
	}
	item := NewGenericToolMessageItem(&sty, tc, nil, false)

	rendered := item.Render(120)

	// Should show the title value as a param, not raw JSON.
	if strings.Contains(rendered, `{"title"`) {
		t.Fatalf("should not show raw JSON in params, got: %q", rendered)
	}
	if !strings.Contains(rendered, "pkg/main.go") {
		t.Fatalf("should show title value 'pkg/main.go', got: %q", rendered)
	}
}

func TestGenericToolRender_WithResult(t *testing.T) {
	sty := styles.DefaultStyles()
	tc := ToolCall{
		ID:       "tc-1",
		Name:     "bash",
		Input:    `{"command":"ls"}`,
		Finished: true,
	}
	result := &ToolResult{
		ToolCallID: "tc-1",
		Content:    "file1.go\nfile2.go",
	}
	item := NewGenericToolMessageItem(&sty, tc, result, false)

	rendered := item.Render(120)

	if !strings.Contains(rendered, "Bash") {
		t.Fatalf("should contain tool name 'Bash', got: %q", rendered)
	}
	// Result content should appear somewhere in the output.
	if !strings.Contains(rendered, "file1.go") {
		t.Fatalf("should show result content, got: %q", rendered)
	}
}
