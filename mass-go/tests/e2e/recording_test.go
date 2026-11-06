//go:build e2e

package e2e_test

import (
	"testing"

	"github.com/zoumo/mass/mass-go/internal/recordingtest"
)

func TestRecordCodexBasic(t *testing.T) {
	recordingtest.RunRecordCodexBasic(t)
}

func TestRecordCodexPermission(t *testing.T) {
	recordingtest.RunRecordCodexPermission(t)
}

func TestRecordClaudeBasic(t *testing.T) {
	recordingtest.RunRecordClaudeBasic(t)
}

func TestRecordClaudePermission(t *testing.T) {
	recordingtest.RunRecordClaudePermission(t)
}
