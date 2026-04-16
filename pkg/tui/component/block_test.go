package component

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestRenderBlock_Empty(t *testing.T) {
	got := RenderBlock(BlockConfig{})
	if got != "" {
		t.Fatalf("empty block should return empty, got %q", got)
	}
}

func TestRenderBlock_BodyOnly(t *testing.T) {
	got := RenderBlock(BlockConfig{Body: "hello"})
	// No border, no background → 2-char indent.
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("should be indented, got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("should contain body, got %q", got)
	}
}

func TestRenderBlock_WithBorder(t *testing.T) {
	got := RenderBlock(BlockConfig{
		Border: &BorderConfig{Char: "▌", Color: color.White},
		Body:   "hello",
	})
	if !strings.Contains(got, "▌") {
		t.Fatalf("should contain border char, got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("should contain body, got %q", got)
	}
}

func TestRenderBlock_WithLabel(t *testing.T) {
	got := RenderBlock(BlockConfig{
		Label: &LabelConfig{Text: "[User]", Style: lipgloss.NewStyle().Bold(true)},
		Body:  "hello",
	})
	if !strings.Contains(got, "[User]") {
		t.Fatalf("should contain label, got %q", got)
	}
}

func TestRenderBlock_WithBackground(t *testing.T) {
	var bg color.Color = color.RGBA{R: 50, G: 50, B: 50, A: 255}
	got := RenderBlock(BlockConfig{
		Label:      &LabelConfig{Text: "[User]", Style: lipgloss.NewStyle()},
		Body:       "hello",
		Background: &bg,
		Width:      40,
	})
	// With background, no border prefix.
	if strings.HasPrefix(got, "▌") {
		t.Fatal("background block should not have border")
	}
	if !strings.Contains(got, "[User]") {
		t.Fatalf("should contain label, got %q", got)
	}
}

func TestRenderBlock_WithDetail(t *testing.T) {
	got := RenderBlock(BlockConfig{
		Border: &BorderConfig{Char: "▌", Color: color.White},
		Body:   "main text",
		Detail: "detail text",
	})
	if !strings.Contains(got, "main text") {
		t.Fatalf("should contain body, got %q", got)
	}
	if !strings.Contains(got, "detail text") {
		t.Fatalf("should contain detail, got %q", got)
	}
}

func TestRenderBlock_BodyStyle(t *testing.T) {
	faint := lipgloss.NewStyle().Faint(true)
	got := RenderBlock(BlockConfig{
		Body:      "styled text",
		BodyStyle: &faint,
	})
	// The text should be present (styling is ANSI codes, hard to test visually).
	if !strings.Contains(got, "styled text") {
		t.Fatalf("should contain body text, got %q", got)
	}
}
