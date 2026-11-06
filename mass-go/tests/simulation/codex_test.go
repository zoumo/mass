package simulation_test

import (
	"testing"

	"github.com/zoumo/mass/mass-go/internal/recordingtest"
)

func TestCodexBasicReplay(t *testing.T) {
	recordingtest.RunCodexBasicReplay(t)
}

func TestCodexPermissionReplay(t *testing.T) {
	recordingtest.RunCodexPermissionReplay(t)
}

func TestRecordingRejectsInvalidMaterial(t *testing.T) {
	recordingtest.RunRejectsInvalidMaterial(t)
}
