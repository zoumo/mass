package simulation_test

import (
	"testing"

	"github.com/zoumo/mass/mass-go/internal/recordingtest"
)

func TestClaudeBasicReplay(t *testing.T) {
	recordingtest.RunClaudeBasicReplay(t)
}

func TestClaudePermissionReplay(t *testing.T) {
	recordingtest.RunClaudePermissionReplay(t)
}
