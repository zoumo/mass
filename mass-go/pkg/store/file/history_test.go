package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type appendSuite struct {
	suite.Suite
	st  *Store
	dir string
	r   resource.Session
}

func TestAppendSuite(t *testing.T) { suite.Run(t, new(appendSuite)) }

func (s *appendSuite) SetupTest() {
	s.dir = s.T().TempDir()
	var err error
	s.st, err = Open(s.dir, false)
	s.Require().NoError(err)
	s.r, err = s.st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "codex", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}}, Status: resource.SessionStatus{SessionID: "native", State: events.Unknown}})
	s.Require().NoError(err)
	owned := s.st
	s.T().Cleanup(func() { s.Require().NoError(owned.Close()) })
}

func (s *appendSuite) fact(text string) events.SessionEvent {
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: uuid.NewString(), Time: time.Now().UTC()}, events.AgentMessage{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}, events.NewSource())
	s.Require().NoError(err)
	return e
}

type failingAppendFile struct {
	*os.File
	stage string
}

func (f failingAppendFile) Write(b []byte) (int, error) {
	switch f.stage {
	case "before-write":
		return 0, errors.New("injected write failure")
	case "partial-write", "short-write":
		n, err := f.File.Write(b[:len(b)/3])
		if err != nil || f.stage == "short-write" {
			return n, err
		}
		return n, errors.New("injected partial write failure")
	}
	return f.File.Write(b)
}

func (f failingAppendFile) Sync() error {
	if f.stage == "sync" {
		return errors.New("injected sync failure")
	}
	return f.File.Sync()
}

func (f failingAppendFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return errors.Join(err, errors.New("injected close failure"))
	}
	return err
}

func (s *appendSuite) inject(stage string) func(string) (appendFile, error) {
	previous := s.st.appendOpen
	s.st.appendOpen = func(path string) (appendFile, error) {
		f, err := openHistory(path, os.O_RDWR|os.O_APPEND)
		if err != nil {
			return nil, err
		}
		return failingAppendFile{File: f, stage: stage}, nil
	}
	return previous
}

func (s *appendSuite) TestFailuresRecoverWithoutHistoricalPayloadCopiesOrDuplicateEvents() {
	for _, stage := range []string{"before-write", "partial-write", "short-write", "sync", "close"} {
		for _, recovery := range []string{"reconcile", "reopen"} {
			s.Run(stage+"/"+recovery, func() {
				// Each failure uses independent real files and an OS I/O wrapper.
				s.SetupTest()
				old := s.fact("old-payload-" + strings.Repeat("historical-only", 8192))
				second := s.fact("second committed event")
				_, err := s.st.Append("native", []events.SessionEvent{old, second})
				s.Require().NoError(err)
				path := s.st.historyPath(s.r)
				before, err := os.Stat(path)
				s.Require().NoError(err)
				prefix, err := os.ReadFile(path)
				s.Require().NoError(err)
				firstPage, err := s.st.ListHistory("native", store.HistoryQuery{Limit: 1})
				s.Require().NoError(err)
				one, two := s.fact("new-one"), s.fact("new-two")
				batch := []events.SessionEvent{old, one, one, two}
				originalOpen := s.inject(stage)
				_, err = s.st.Append("native", batch)
				s.Require().ErrorContains(err, "outcome_unknown")
				_, err = s.st.Append("native", batch)
				s.Require().ErrorContains(err, "outcome_unknown", "writer stays sealed")
				_, err = s.st.ListHistory("native", store.HistoryQuery{})
				s.Require().ErrorContains(err, "outcome_unknown")
				intentPath := filepath.Join(s.dir, ".append.pending.json")
				var intent appendIntent
				s.Require().NoError(readJSON(intentPath, &intent))
				s.Equal(int64(len(prefix)), intent.Offset)
				s.NotContains(string(intent.Data), "historical-only")
				s.NotContains(string(intent.Data), old.EventID)
				s.Equal(2, bytes.Count(intent.Data, []byte{'\n'}))
				_, err = Open(s.dir, true)
				s.Require().ErrorContains(err, "writable reopen required")
				if recovery == "reopen" {
					s.Require().NoError(s.st.Close())
					s.st, err = Open(s.dir, false)
					s.Require().NoError(err)
				} else {
					if stage == "partial-write" {
						s.Require().Error(s.st.Reconcile(), "recovery can itself encounter another short append")
					}
					s.st.appendOpen = originalOpen
					s.Require().NoError(s.st.Reconcile())
				}
				after, err := os.Stat(path)
				s.Require().NoError(err)
				s.True(os.SameFile(before, after), "recovery appends to the original inode")
				content, err := os.ReadFile(path)
				s.Require().NoError(err)
				s.True(bytes.HasPrefix(content, prefix), "committed bytes are never replaced")
				s.Equal(append(bytes.Clone(prefix), intent.Data...), content)
				page, err := s.st.ListHistory("native", store.HistoryQuery{})
				s.Require().NoError(err)
				s.Require().Len(page.Events, 4)
				s.Equal([]string{old.EventID, second.EventID, one.EventID, two.EventID}, []string{page.Events[0].EventID, page.Events[1].EventID, page.Events[2].EventID, page.Events[3].EventID})
				next, err := s.st.ListHistory("native", store.HistoryQuery{Limit: 1, After: firstPage.Next})
				s.Require().NoError(err)
				s.Equal(second.EventID, next.Events[0].EventID)
				progress, err := s.st.Append("native", batch)
				s.Require().NoError(err)
				s.Equal(two.EventID, *progress)
				retried, err := os.Stat(path)
				s.Require().NoError(err)
				s.True(os.SameFile(after, retried))
				s.Equal(after.ModTime(), retried.ModTime(), "idempotent retries do not write")
				s.Equal(after.Size(), retried.Size())
				_, err = os.Stat(intentPath)
				s.True(os.IsNotExist(err))
				s.Require().NoError(s.st.Close())
			})
		}
	}
}

func (s *appendSuite) TestConflictingTailAndCommittedPrefixArePreserved() {
	for _, conflict := range []string{"tail", "prefix"} {
		s.Run(conflict, func() {
			s.SetupTest()
			_, err := s.st.Append("native", []events.SessionEvent{s.fact("committed-marker")})
			s.Require().NoError(err)
			stage := "partial-write"
			if conflict == "prefix" {
				stage = "before-write"
			}
			s.inject(stage)
			_, err = s.st.Append("native", []events.SessionEvent{s.fact("new-marker")})
			s.Require().ErrorContains(err, "outcome_unknown")
			var intent appendIntent
			s.Require().NoError(readJSON(filepath.Join(s.dir, ".append.pending.json"), &intent))
			path := s.st.historyPath(s.r)
			content, err := os.ReadFile(path)
			s.Require().NoError(err)
			if conflict == "tail" {
				content[intent.Offset] ^= 1
			} else {
				content = bytes.Replace(content, []byte("committed-marker"), []byte("corrupted-marker"), 1)
			}
			s.Require().NoError(os.WriteFile(path, content, 0o600))
			s.Require().NoError(s.st.Close())
			_, err = Open(s.dir, false)
			s.Require().Error(err)
			s.Contains(err.Error(), map[string]string{"tail": "tail differs", "prefix": "prefix does not match"}[conflict])
			after, err := os.ReadFile(path)
			s.Require().NoError(err)
			s.Equal(content, after, "recovery must not truncate divergent bytes")
			_, err = os.Stat(filepath.Join(s.dir, ".append.pending.json"))
			s.Require().NoError(err, "retain unresolved append evidence")
		})
	}
}

func (s *appendSuite) TestReadsUseDiskAndMetadataRedoDoesNotContainHistory() {
	old := s.fact("unique-historical-body")
	_, err := s.st.Append("native", []events.SessionEvent{old})
	s.Require().NoError(err)
	path := s.st.historyPath(s.r)
	before, err := os.Stat(path)
	s.Require().NoError(err)
	content, err := os.ReadFile(path)
	s.Require().NoError(err)
	// A metadata-only failed create must not serialize any old event payload.
	blocked := filepath.Join(s.dir, "sessions", "default", "blocked", "state.json")
	s.Require().NoError(os.MkdirAll(blocked, 0o700))
	r := s.r
	r.Metadata = resource.Metadata{Namespace: "default", Name: "blocked"}
	r.Status.SessionID = "other-native"
	_, err = s.st.CreateSession(r)
	s.Require().ErrorContains(err, "outcome_unknown")
	pending, err := os.ReadFile(filepath.Join(s.dir, ".pending.json"))
	s.Require().NoError(err)
	s.NotContains(string(pending), "unique-historical-body")
	s.NotContains(string(pending), old.EventID)
	var metadata map[string]any
	s.Require().NoError(json.Unmarshal(pending, &metadata))
	s.NotContains(metadata["next"].(map[string]any), "history")
	s.Require().NoError(os.Remove(blocked))
	s.Require().NoError(s.st.Reconcile())
	after, err := os.Stat(path)
	s.Require().NoError(err)
	s.True(os.SameFile(before, after))
	s.Equal(before.ModTime(), after.ModTime())
	// Disk corruption after Open is visible to a query, proving that event bodies
	// are read from disk rather than returned from an in-memory history cache.
	changed := bytes.Replace(content, []byte("unique-historical-body"), []byte("broken-historical-body"), 1)
	s.Require().NoError(os.WriteFile(path, changed, 0o600))
	_, err = s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().ErrorContains(err, "committed history record changed")
}

func (s *appendSuite) TestSnapshotReadersAndConflictingBatch() {
	first := s.fact("first")
	_, err := s.st.Append("native", []events.SessionEvent{first})
	s.Require().NoError(err)
	reader, err := Open(s.dir, true)
	s.Require().NoError(err)
	defer reader.Close()
	second := s.fact("second")
	_, err = s.st.Append("native", []events.SessionEvent{second})
	s.Require().NoError(err)
	page, err := reader.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(first.EventID, page.Events[0].EventID)
	conflicting := second
	conflicting.Time = conflicting.Time.Add(time.Second)
	_, err = s.st.Append("native", []events.SessionEvent{s.fact("should not commit"), conflicting})
	s.Require().ErrorContains(err, "content conflict")
	page, err = s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(page.Events, 2)
	_, err = os.Stat(filepath.Join(s.dir, ".append.pending.json"))
	s.True(os.IsNotExist(err), fmt.Sprint(err))
}

type crashingAppendFile struct {
	*os.File
	stage string
}

func (f crashingAppendFile) Write(b []byte) (int, error) {
	if f.stage == "partial" {
		n, err := f.File.Write(b[:len(b)/2])
		if err != nil {
			return n, err
		}
		if err := f.File.Sync(); err != nil {
			return n, err
		}
		os.Exit(77)
	}
	return f.File.Write(b)
}

func (f crashingAppendFile) Sync() error {
	if err := f.File.Sync(); err != nil {
		return err
	}
	if f.stage == "synced" {
		os.Exit(77)
	}
	return nil
}

// The same binary acts as a crash peer. Parent assertions use only disk state;
// no in-memory Store object survives the forced process exit.
func TestAppendProcessCrashRecovery(t *testing.T) {
	if dir := os.Getenv("MASS_TEST_APPEND_CRASH_DIR"); dir != "" {
		st, err := Open(dir, false)
		require.NoError(t, err)
		st.appendOpen = func(path string) (appendFile, error) {
			f, err := openHistory(path, os.O_RDWR|os.O_APPEND)
			if err != nil {
				return nil, err
			}
			return crashingAppendFile{File: f, stage: os.Getenv("MASS_TEST_APPEND_CRASH_STAGE")}, nil
		}
		e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: "crash-item", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "crash-message", Content: []events.ContentBlock{events.Text("new crash event")}}, events.NewSource())
		require.NoError(t, err)
		_, err = st.Append("native", []events.SessionEvent{e})
		t.Fatalf("crash injection was not reached: %v", err)
	}
	for _, stage := range []string{"partial", "synced"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			st, err := Open(dir, false)
			require.NoError(t, err)
			r, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "codex"}, Status: resource.SessionStatus{SessionID: "native", State: events.Unknown}})
			require.NoError(t, err)
			old, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: "old-item", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "old-message", Content: []events.ContentBlock{events.Text("old committed event")}}, events.NewSource())
			require.NoError(t, err)
			_, err = st.Append("native", []events.SessionEvent{old})
			require.NoError(t, err)
			path := st.historyPath(r)
			before, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, st.Close())
			binary, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestAppendProcessCrashRecovery$")
			cmd.Env = append(os.Environ(), "MASS_TEST_APPEND_CRASH_DIR="+dir, "MASS_TEST_APPEND_CRASH_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, string(output))
			require.Equal(t, 77, exited.ExitCode(), string(output))
			_, err = Open(dir, true)
			require.ErrorContains(t, err, "writable reopen required")
			var intent appendIntent
			require.NoError(t, readJSON(filepath.Join(dir, ".append.pending.json"), &intent))
			st, err = Open(dir, false)
			require.NoError(t, err)
			defer st.Close()
			page, err := st.ListHistory("native", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 2)
			require.Equal(t, old.EventID, page.Events[0].EventID)
			require.Equal(t, int64(len(intent.Data))+intent.Offset, st.histories["native"].End)
			progress, err := st.Append("native", page.Events)
			require.NoError(t, err)
			require.Equal(t, page.Events[1].EventID, *progress)
			after, err := os.Stat(path)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			_, err = os.Stat(filepath.Join(dir, ".append.pending.json"))
			require.True(t, os.IsNotExist(err))
		})
	}
}
