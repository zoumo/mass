package chat

import (
	"testing"
)

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		input     string
		wantName  string
		wantArgs  string
		wantFound bool
	}{
		{"/help", "help", "", true},
		{"/clear", "clear", "", true},
		{"/status", "status", "", true},
		{"/cancel", "cancel", "", true},
		{"/exit", "exit", "", true},
		{"/quit", "exit", "", true},
		{"/q", "exit", "", true},
		{"/HELP", "help", "", true},
		{"/Exit", "exit", "", true},
		{"/unknown", "", "", false},
		{"hello", "", "", false},
		{"", "", "", false},
		{"/ ", "", "", false},
		{"/help  extra args ", "help", "extra args", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, args, found := parseSlashCommand(tt.input)
			if found != tt.wantFound {
				t.Fatalf("found=%v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if cmd.Name != tt.wantName {
				t.Errorf("name=%q, want %q", cmd.Name, tt.wantName)
			}
			if args != tt.wantArgs {
				t.Errorf("args=%q, want %q", args, tt.wantArgs)
			}
		})
	}
}

func TestLookupCommand(t *testing.T) {
	tests := []struct {
		name     string
		wantName string
		wantNil  bool
	}{
		{"help", "help", false},
		{"quit", "exit", false},
		{"q", "exit", false},
		{"nope", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := lookupCommand(tt.name)
			if tt.wantNil {
				if cmd != nil {
					t.Fatalf("want nil, got %q", cmd.Name)
				}
				return
			}
			if cmd == nil {
				t.Fatal("want non-nil command")
			}
			if cmd.Name != tt.wantName {
				t.Errorf("name=%q, want %q", cmd.Name, tt.wantName)
			}
		})
	}
}

func TestCommandRegistryComplete(t *testing.T) {
	expected := []string{"help", "clear", "cancel", "exit", "model", "status"}
	if len(commandRegistry) != len(expected) {
		t.Fatalf("registry has %d commands, want %d", len(commandRegistry), len(expected))
	}
	for i, name := range expected {
		if commandRegistry[i].Name != name {
			t.Errorf("registry[%d].Name=%q, want %q", i, commandRegistry[i].Name, name)
		}
		if commandRegistry[i].Handler == nil {
			t.Errorf("registry[%d] (%s) has nil handler", i, name)
		}
	}
}
