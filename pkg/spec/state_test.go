package spec_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
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

func sampleState() spec.State {
	return spec.State{
		OarVersion:  "0.1.0",
		ID:          "test-session-123",
		Status:      spec.StatusCreated,
		PID:         42,
		Bundle:      "/path/to/bundle",
		Annotations: map[string]string{"key": "value"},
	}
}

func (s *StateSuite) TestStateDir() {
	dir := spec.StateDir(s.baseDir, "abc")
	s.Equal(s.baseDir+"/abc", dir)
}

func (s *StateSuite) TestWriteReadRoundTrip() {
	st := sampleState()
	dir := spec.StateDir(s.baseDir, st.ID)

	s.Require().NoError(spec.WriteState(dir, st))

	got, err := spec.ReadState(dir)
	s.Require().NoError(err)

	s.Equal(st.OarVersion, got.OarVersion)
	s.Equal(st.ID, got.ID)
	s.Equal(st.Status, got.Status)
	s.Equal(st.PID, got.PID)
	s.Equal(st.Bundle, got.Bundle)
	s.Equal(st.Annotations, got.Annotations)
}

func (s *StateSuite) TestWriteCreatesDir() {
	// WriteState should create the directory if it doesn't exist.
	dir := spec.StateDir(s.baseDir, "new-session")
	s.Require().NoError(spec.WriteState(dir, sampleState()))

	_, err := os.Stat(dir)
	s.Require().NoError(err, "directory should exist after WriteState")
}

func (s *StateSuite) TestReadMissingReturnsError() {
	dir := spec.StateDir(s.baseDir, "nonexistent")
	_, err := spec.ReadState(dir)
	s.Require().Error(err)
}

func (s *StateSuite) TestDeleteState() {
	st := sampleState()
	dir := spec.StateDir(s.baseDir, st.ID)
	s.Require().NoError(spec.WriteState(dir, st))

	s.Require().NoError(spec.DeleteState(dir))

	_, err := os.Stat(dir)
	s.True(os.IsNotExist(err), "directory should be removed after DeleteState")
}

func (s *StateSuite) TestDeleteNonexistentIsNoop() {
	dir := spec.StateDir(s.baseDir, "ghost")
	// Deleting a nonexistent dir should not error (RemoveAll semantics).
	s.NoError(spec.DeleteState(dir))
}

func (s *StateSuite) TestWriteIsAtomic() {
	// After a successful write, state.json should exist and be valid JSON.
	st := sampleState()
	dir := spec.StateDir(s.baseDir, st.ID)
	s.Require().NoError(spec.WriteState(dir, st))

	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	// Should only have state.json; no leftover temp files.
	s.Len(entries, 1)
	s.Equal("state.json", entries[0].Name())
}

func TestStateSuite(t *testing.T) {
	suite.Run(t, new(StateSuite))
}
