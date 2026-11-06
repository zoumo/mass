package simulation_test

import (
	"testing"

	"github.com/zoumo/mass/mass-go/internal/recordingtest"
)

func TestRecordingGate(t *testing.T) {
	recordingtest.RunGate(t)
}

func TestRecordingAuditRejectsInvalidCaseSelection(t *testing.T) {
	recordingtest.RunAuditRejectsInvalidCaseSelection(t)
}

func TestRecordingAuditRejectsInvalidArtifacts(t *testing.T) {
	recordingtest.RunAuditRejectsInvalidArtifacts(t)
}

func TestRecordingAuditRejectsSensitiveMaterial(t *testing.T) {
	recordingtest.RunAuditRejectsSensitiveMaterial(t)
}

func TestRecordingRejectsIncompleteExecution(t *testing.T) {
	recordingtest.RunRejectsIncompleteExecution(t)
}
