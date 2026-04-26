package runtimespec_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	runtimespec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

type StateSuite struct {
	suite.Suite
	baseDir string
}

func (s *StateSuite) SetupTest() {
	var err error
	s.baseDir, err = os.MkdirTemp("", "oai-state-test-*")
	s.Require().NoError(err)
}

func (s *StateSuite) TeardownTest() {
	_ = os.RemoveAll(s.baseDir)
}

func sampleState() apiruntime.State {
	return apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "test-session-123",
		Phase:       apiruntime.PhaseIdle,
		PID:         42,
		Bundle:      "/path/to/bundle",
		Annotations: map[string]string{"key": "value"},
	}
}

func (s *StateSuite) TestStateDir() {
	dir := runtimespec.StateDir(s.baseDir, "abc")
	s.Equal(s.baseDir+"/abc", dir)
}

func (s *StateSuite) TestWriteReadRoundTrip() {
	st := sampleState()
	dir := runtimespec.StateDir(s.baseDir, st.ID)

	s.Require().NoError(runtimespec.WriteState(dir, st))

	got, err := runtimespec.ReadState(dir)
	s.Require().NoError(err)

	s.Equal(st.MassVersion, got.MassVersion)
	s.Equal(st.ID, got.ID)
	s.Equal(st.Phase, got.Phase)
	s.Equal(st.PID, got.PID)
	s.Equal(st.Bundle, got.Bundle)
	s.Equal(st.Annotations, got.Annotations)
}

func (s *StateSuite) TestWriteCreatesDir() {
	dir := runtimespec.StateDir(s.baseDir, "new-session")
	s.Require().NoError(runtimespec.WriteState(dir, sampleState()))

	_, err := os.Stat(dir)
	s.Require().NoError(err, "directory should exist after WriteState")
}

func (s *StateSuite) TestReadMissingReturnsError() {
	dir := runtimespec.StateDir(s.baseDir, "nonexistent")
	_, err := runtimespec.ReadState(dir)
	s.Require().Error(err)
}

func (s *StateSuite) TestDeleteState() {
	st := sampleState()
	dir := runtimespec.StateDir(s.baseDir, st.ID)
	s.Require().NoError(runtimespec.WriteState(dir, st))

	s.Require().NoError(runtimespec.DeleteState(dir))

	_, err := os.Stat(dir)
	s.True(os.IsNotExist(err), "directory should be removed after DeleteState")
}

func (s *StateSuite) TestDeleteNonexistentIsNoop() {
	dir := runtimespec.StateDir(s.baseDir, "ghost")
	s.NoError(runtimespec.DeleteState(dir))
}

func (s *StateSuite) TestWriteIsAtomic() {
	st := sampleState()
	dir := runtimespec.StateDir(s.baseDir, st.ID)
	s.Require().NoError(runtimespec.WriteState(dir, st))

	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	s.Len(entries, 1)
	s.Equal("state.json", entries[0].Name())
}

// ── Helpers for full-state round-trip tests ──────────────────────────────────

func strPtr(s string) *string { return &s }

func fullSessionState() *apiruntime.SessionState {
	return &apiruntime.SessionState{
		AgentInfo: &apiruntime.AgentInfo{
			Name:    "test-agent",
			Version: "1.2.3",
			Title:   strPtr("Test Agent Title"),
		},
		Capabilities: &apiruntime.AgentCapabilities{
			LoadSession: true,
			McpCapabilities: apiruntime.McpCapabilities{
				Http: true,
			},
			PromptCapabilities: apiruntime.PromptCapabilities{
				Image:           true,
				Audio:           true,
				EmbeddedContext: true,
			},
			SessionCapabilities: apiruntime.SessionCapabilities{
				Fork: &apiruntime.SessionForkCapabilities{},
			},
		},
		AvailableCommands: []apiruntime.AvailableCommand{
			{
				Name:        "run",
				Description: "Run something",
				Input: &apiruntime.AvailableCommandInput{
					Unstructured: &apiruntime.UnstructuredCommandInput{
						Hint: "Enter a command to run",
					},
				},
			},
			{
				Name:        "quit",
				Description: "Quit the session",
				// Input deliberately nil — tests the nil-input path.
			},
		},
		ConfigOptions: []apiruntime.ConfigOption{
			{
				// Ungrouped variant
				Select: &apiruntime.ConfigOptionSelect{
					ID:           "theme",
					Name:         "Theme",
					CurrentValue: "dark",
					Description:  strPtr("Color theme"),
					Options: apiruntime.ConfigSelectOptions{
						Ungrouped: []apiruntime.ConfigSelectOption{
							{Name: "Dark", Value: "dark"},
							{Name: "Light", Value: "light"},
						},
					},
				},
			},
			{
				// Grouped variant
				Select: &apiruntime.ConfigOptionSelect{
					ID:           "model",
					Name:         "Model",
					CurrentValue: "gpt-4",
					Category:     strPtr("AI"),
					Options: apiruntime.ConfigSelectOptions{
						Grouped: []apiruntime.ConfigSelectGroup{
							{
								Group: "OpenAI",
								Name:  "OpenAI Models",
								Options: []apiruntime.ConfigSelectOption{
									{Name: "GPT-4", Value: "gpt-4"},
									{Name: "GPT-3.5", Value: "gpt-3.5"},
								},
							},
							{
								Group: "Anthropic",
								Name:  "Anthropic Models",
								Options: []apiruntime.ConfigSelectOption{
									{Name: "Claude 3", Value: "claude-3"},
								},
							},
						},
					},
				},
			},
		},
		SessionInfo: &apiruntime.SessionInfo{
			Title:     strPtr("My Test Session"),
			UpdatedAt: strPtr("2025-06-01T12:00:00.000000000Z"),
		},
		CurrentMode: strPtr("coding"),
	}
}

func fullState() apiruntime.State {
	exitCode := 0
	return apiruntime.State{
		MassVersion: "0.2.0",
		ID:          "full-roundtrip-session",
		Phase:       apiruntime.PhaseRunning,
		PID:         9999,
		Bundle:      "/opt/bundles/test-agent",
		Annotations: map[string]string{"env": "test", "team": "platform"},
		ExitCode:    &exitCode,
		UpdatedAt:   "2025-06-01T12:00:00.123456789Z",
		Session:     fullSessionState(),
		EventCounts: map[string]int{
			"text":         42,
			"tool_call":    7,
			"state_change": 3,
			"turn_start":   2,
			"turn_end":     2,
			"user_message": 2,
		},
	}
}

// ── Full round-trip tests ────────────────────────────────────────────────────

func (s *StateSuite) TestFullStateRoundTrip() {
	want := fullState()
	dir := runtimespec.StateDir(s.baseDir, want.ID)

	s.Require().NoError(runtimespec.WriteState(dir, want))

	got, err := runtimespec.ReadState(dir)
	s.Require().NoError(err)

	// Top-level scalar & map fields
	s.Equal(want.MassVersion, got.MassVersion)
	s.Equal(want.ID, got.ID)
	s.Equal(want.Phase, got.Phase)
	s.Equal(want.PID, got.PID)
	s.Equal(want.Bundle, got.Bundle)
	s.Equal(want.Annotations, got.Annotations)
	s.Equal(want.ExitCode, got.ExitCode)
	s.Equal(want.UpdatedAt, got.UpdatedAt)
	s.Equal(want.EventCounts, got.EventCounts)

	// Session — deep equal covers all nested union variants
	s.Require().NotNil(got.Session)
	s.Equal(want.Session.AgentInfo, got.Session.AgentInfo, "AgentInfo")
	s.Equal(want.Session.Capabilities, got.Session.Capabilities, "Capabilities")
	s.Equal(want.Session.SessionInfo, got.Session.SessionInfo, "SessionInfo")
	s.Equal(want.Session.CurrentMode, got.Session.CurrentMode, "CurrentMode")

	// AvailableCommands — verify Unstructured input variant round-trips
	s.Require().Len(got.Session.AvailableCommands, 2)
	s.Equal(want.Session.AvailableCommands[0], got.Session.AvailableCommands[0], "AvailableCommand with Unstructured input")
	s.Equal(want.Session.AvailableCommands[1], got.Session.AvailableCommands[1], "AvailableCommand with nil input")

	// ConfigOptions — verify both Ungrouped and Grouped Select variants
	s.Require().Len(got.Session.ConfigOptions, 2)
	s.Equal(want.Session.ConfigOptions[0], got.Session.ConfigOptions[0], "ConfigOption Ungrouped")
	s.Equal(want.Session.ConfigOptions[1], got.Session.ConfigOptions[1], "ConfigOption Grouped")

	// Final deep-equal covers anything we might have missed above
	s.Equal(want, got, "full State deep-equal")
}

func (s *StateSuite) TestStateRoundTripNilSession() {
	st := sampleState() // Session is nil by default
	dir := runtimespec.StateDir(s.baseDir, "nil-session")

	s.Require().NoError(runtimespec.WriteState(dir, st))

	got, err := runtimespec.ReadState(dir)
	s.Require().NoError(err)

	s.Nil(got.Session, "nil Session should remain nil after round-trip")
}

func (s *StateSuite) TestStateRoundTripEmptyEventCounts() {
	st := sampleState() // EventCounts is nil by default
	dir := runtimespec.StateDir(s.baseDir, "nil-eventcounts")

	s.Require().NoError(runtimespec.WriteState(dir, st))

	got, err := runtimespec.ReadState(dir)
	s.Require().NoError(err)

	s.Nil(got.EventCounts, "nil EventCounts should remain nil after round-trip")
}

func TestStateSuite(t *testing.T) {
	suite.Run(t, new(StateSuite))
}
