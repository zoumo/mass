package runtimespec_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
	runtimespec "github.com/zoumo/oar/pkg/runtime-spec"
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
		OarVersion:  "0.1.0",
		ID:          "test-session-123",
		Status:      apiruntime.StatusIdle,
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

	s.Equal(st.OarVersion, got.OarVersion)
	s.Equal(st.ID, got.ID)
	s.Equal(st.Status, got.Status)
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

func TestStateSuite(t *testing.T) {
	suite.Run(t, new(StateSuite))
}
