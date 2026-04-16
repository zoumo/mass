package chat

import "testing"

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
