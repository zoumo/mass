package simulation_test

import (
	"testing"

	"github.com/zoumo/mass/internal/recordingtest"
)

func TestClaudeBasicReplay(t *testing.T) {
	recordingtest.RunClaudeBasicReplay(t)
}

func TestClaudePermissionReplay(t *testing.T) {
	recordingtest.RunClaudePermissionReplay(t)
}

func TestClaudeCapturedUUIDReplay(t *testing.T) {
	recordingtest.RunClaudeCapturedUUIDReplay(t)
}
