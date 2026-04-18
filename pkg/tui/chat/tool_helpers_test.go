package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/tui/component"
)

func TestDeriveToolKind(t *testing.T) {
	tests := []struct {
		kind, title, want string
	}{
		{"read", "", "read"},
		{"read", "Read File", "read"},
		{"", "Tool: workspace/send", "workspace/send"},
		{"", "Read File", "Read File"},
		{"", "", "tool"},
	}
	for _, tt := range tests {
		got := DeriveToolKind(tt.kind, tt.title)
		if got != tt.want {
			t.Errorf("DeriveToolKind(%q, %q) = %q, want %q", tt.kind, tt.title, got, tt.want)
		}
	}
}

func TestToolDisplayTitle(t *testing.T) {
	tests := []struct {
		kind, title, want string
	}{
		{"read", "Read File", "Read File"},
		{"", "Tool: workspace/send", ""},
		{"", "Read File", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := ToolDisplayTitle(tt.kind, tt.title)
		if got != tt.want {
			t.Errorf("ToolDisplayTitle(%q, %q) = %q, want %q", tt.kind, tt.title, got, tt.want)
		}
	}
}

// ── BuildInput ──────────────────────────────────────────────────────────────

func TestBuildInput_Empty(t *testing.T) {
	assert.Equal(t, "{}", BuildInput("", nil))
}

func TestBuildInput_TitleOnly(t *testing.T) {
	got := BuildInput("Read file", nil)
	assert.Contains(t, got, `"title":"Read file"`)
}

func TestBuildInput_SingleLocationWithLine(t *testing.T) {
	line := 10
	locs := []runapi.ToolCallLocation{{Path: "file.go", Line: &line}}
	got := BuildInput("", locs)
	assert.Contains(t, got, `"path":"file.go:10"`)
	assert.NotContains(t, got, `"paths"`)
}

func TestBuildInput_SingleLocationNoLine(t *testing.T) {
	locs := []runapi.ToolCallLocation{{Path: "file.go"}}
	got := BuildInput("", locs)
	assert.Contains(t, got, `"path":"file.go"`)
}

func TestBuildInput_MultipleLocations(t *testing.T) {
	line1 := 1
	locs := []runapi.ToolCallLocation{
		{Path: "a.go", Line: &line1},
		{Path: "b.go"},
	}
	got := BuildInput("", locs)
	assert.Contains(t, got, `"paths"`)
	assert.Contains(t, got, `"a.go:1"`)
	assert.Contains(t, got, `"b.go"`)
}

func TestBuildInput_TitleAndLocations(t *testing.T) {
	locs := []runapi.ToolCallLocation{{Path: "x.go"}}
	got := BuildInput("Edit", locs)
	assert.Contains(t, got, `"title":"Edit"`)
	assert.Contains(t, got, `"path":"x.go"`)
}

// ── truncateToolResult ──────────────────────────────────────────────────────

func TestTruncateToolResult_Short(t *testing.T) {
	s := "short content"
	assert.Equal(t, s, truncateToolResult(s))
}

func TestTruncateToolResult_ExactLimit(t *testing.T) {
	s := strings.Repeat("x", maxToolResultBytes)
	assert.Equal(t, s, truncateToolResult(s))
}

func TestTruncateToolResult_Long(t *testing.T) {
	s := strings.Repeat("A", 2048) + strings.Repeat("B", 6000) + strings.Repeat("C", 2048)
	got := truncateToolResult(s)
	half := maxToolResultBytes / 2

	// Head preserved
	assert.True(t, strings.HasPrefix(got, s[:half]))
	// Tail preserved
	assert.True(t, strings.HasSuffix(got, s[len(s)-half:]))
	// Truncation marker present
	assert.Contains(t, got, "bytes truncated")
	// Shorter than original
	assert.Less(t, len(got), len(s))
}

// ── BuildResultContent ──────────────────────────────────────────────────────

func textBlock(text string) runapi.ToolCallContent {
	return runapi.ToolCallContent{
		Content: &runapi.ToolCallContentContent{
			Content: runapi.TextBlock(text),
		},
	}
}

func diffBlock(path, newText string, oldText *string) runapi.ToolCallContent {
	return runapi.ToolCallContent{
		Diff: &runapi.ToolCallContentDiff{
			Path:    path,
			OldText: oldText,
			NewText: newText,
		},
	}
}

func terminalBlock(id string) runapi.ToolCallContent {
	return runapi.ToolCallContent{
		Terminal: &runapi.ToolCallContentTerminal{TerminalID: id},
	}
}

func TestBuildResultContent_TextBlock(t *testing.T) {
	blocks := []runapi.ToolCallContent{textBlock("hello world")}
	got := BuildResultContent(blocks, "success", nil)
	assert.Equal(t, "hello world", got)
}

func TestBuildResultContent_DiffBlock(t *testing.T) {
	blocks := []runapi.ToolCallContent{diffBlock("main.go", "new", nil)}
	got := BuildResultContent(blocks, "success", nil)
	assert.Equal(t, "diff: main.go", got)
}

func TestBuildResultContent_TerminalBlock(t *testing.T) {
	blocks := []runapi.ToolCallContent{terminalBlock("term-1")}
	got := BuildResultContent(blocks, "success", nil)
	assert.Equal(t, "terminal: term-1", got)
}

func TestBuildResultContent_MultipleBlocks(t *testing.T) {
	blocks := []runapi.ToolCallContent{
		textBlock("line1"),
		diffBlock("f.go", "x", nil),
	}
	got := BuildResultContent(blocks, "success", nil)
	assert.Equal(t, "line1\ndiff: f.go", got)
}

func TestBuildResultContent_FallbackRawOutput(t *testing.T) {
	got := BuildResultContent(nil, "success", "raw string output")
	assert.Equal(t, "raw string output", got)
}

func TestBuildResultContent_NoBlocksNoRawOutput(t *testing.T) {
	got := BuildResultContent(nil, "success", nil)
	assert.Empty(t, got)
}

func TestBuildResultContent_LargeResultTruncated(t *testing.T) {
	big := strings.Repeat("x", 8000)
	blocks := []runapi.ToolCallContent{textBlock(big)}
	got := BuildResultContent(blocks, "success", nil)
	assert.Less(t, len(got), len(big))
	assert.Contains(t, got, "bytes truncated")
}

// ── ExtractDiff ─────────────────────────────────────────────────────────────

func TestExtractDiff_NoDiffBlocks(t *testing.T) {
	blocks := []runapi.ToolCallContent{textBlock("hello")}
	assert.Nil(t, ExtractDiff(blocks))
}

func TestExtractDiff_EmptyBlocks(t *testing.T) {
	assert.Nil(t, ExtractDiff(nil))
}

func TestExtractDiff_WithOldText(t *testing.T) {
	old := "old content"
	blocks := []runapi.ToolCallContent{diffBlock("f.go", "new content", &old)}
	got := ExtractDiff(blocks)
	require.NotNil(t, got)
	assert.Equal(t, &component.ToolResultDiff{
		Path: "f.go", OldText: "old content", NewText: "new content",
	}, got)
}

func TestExtractDiff_NilOldText(t *testing.T) {
	blocks := []runapi.ToolCallContent{diffBlock("f.go", "new", nil)}
	got := ExtractDiff(blocks)
	require.NotNil(t, got)
	assert.Empty(t, got.OldText)
	assert.Equal(t, "new", got.NewText)
}

func TestExtractDiff_FirstDiffReturned(t *testing.T) {
	blocks := []runapi.ToolCallContent{
		diffBlock("a.go", "first", nil),
		diffBlock("b.go", "second", nil),
	}
	got := ExtractDiff(blocks)
	require.NotNil(t, got)
	assert.Equal(t, "a.go", got.Path)
}

// ── FormatRawOutput ─────────────────────────────────────────────────────────

func TestFormatRawOutput_String(t *testing.T) {
	assert.Equal(t, "hello", FormatRawOutput("hello"))
}

func TestFormatRawOutput_Nil(t *testing.T) {
	assert.Empty(t, FormatRawOutput(nil))
}

func TestFormatRawOutput_AggregatedOutput(t *testing.T) {
	m := map[string]any{
		"command":           "ls",
		"aggregated_output": "file1\nfile2",
	}
	assert.Equal(t, "file1\nfile2", FormatRawOutput(m))
}

func TestFormatRawOutput_ACPContentArray(t *testing.T) {
	m := map[string]any{
		"content": []any{
			map[string]any{"text": "line1"},
			map[string]any{"text": "line2"},
			map[string]any{"image": "ignored"},
		},
	}
	assert.Equal(t, "line1\nline2", FormatRawOutput(m))
}

func TestFormatRawOutput_MapFallbackJSON(t *testing.T) {
	m := map[string]any{"key": "value"}
	got := FormatRawOutput(m)
	assert.Contains(t, got, `"key"`)
	assert.Contains(t, got, `"value"`)
}

func TestFormatRawOutput_IntType(t *testing.T) {
	got := FormatRawOutput(42)
	assert.Equal(t, "42", got)
}
