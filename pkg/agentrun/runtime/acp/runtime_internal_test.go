package acp

import (
	"strings"
	"testing"
)

func TestBuildSeedSystemPrompt_AppendsGuard(t *testing.T) {
	seed := buildSeedSystemPrompt("role setup")
	if seed == "role setup" {
		t.Fatalf("expected guard to be appended")
	}
	if want := "protocol and role setup"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
	if want := "Do NOT execute any commands"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
	if want := "Wait for the next message"; !strings.Contains(seed, want) {
		t.Fatalf("expected seed prompt to contain %q, got %q", want, seed)
	}
}
