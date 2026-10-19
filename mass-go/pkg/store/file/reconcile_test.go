package file_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type reconcileSuite struct {
	suite.Suite
	st     *file.Store
	dir    string
	events []runtime.SessionEvent
}

func TestReconcileSuite(t *testing.T) { suite.Run(t, new(reconcileSuite)) }
func (s *reconcileSuite) SetupTest() {
	var err error
	s.st, s.dir = open(s.T())
	_, err = s.st.CreateSession(session("default", "demo", "native"))
	s.Require().NoError(err)
	s.events = []runtime.SessionEvent{
		event(s.T(), "native", "one", runtime.AgentMessage{MessageID: "one", Content: []runtime.ContentBlock{runtime.Text("first")}}),
		event(s.T(), "native", "two", runtime.AgentMessage{MessageID: "two", Content: []runtime.ContentBlock{runtime.Text("second")}}),
	}
}

// A failing batch implementation is allowed to persist a prefix. This injects
// that allowed Store outcome, not an Agent fact or a file crash guarantee.
type partialBatch struct{ store.HistoryStore }

func (p partialBatch) Append(id string, events []runtime.SessionEvent) (*string, error) {
	result, err := p.HistoryStore.Append(id, events[:1])
	if err != nil {
		return result, err
	}
	return result, errors.New("injected unknown after partial batch")
}
func (s *reconcileSuite) TestPartialBatchRetainsUUIDAndOrder() {
	_, err := (partialBatch{s.st}).Append("native", s.events)
	s.Require().Error(err)
	page, err := s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(s.events[0].EventID, page.Events[0].EventID)
	s.Require().NoError(s.st.Reconcile())
	progress, err := s.st.Append("native", s.events)
	s.Require().NoError(err)
	s.Equal(s.events[1].EventID, *progress)
	page, err = s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 2)
	for i, e := range page.Events {
		s.Equal(s.events[i].EventID, e.EventID)
	}
	progress, err = s.st.Append("native", s.events[:1])
	s.Require().NoError(err)
	s.Equal(s.events[1].EventID, *progress)
}
func (s *reconcileSuite) TestFailedValidationDoesNotReopenWriter() {
	_, err := s.st.Append("native", s.events[:1])
	s.Require().NoError(err)
	path := filepath.Join(s.dir, "state.json")
	original, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(path, []byte("corrupt"), 0600))
	s.Require().Error(s.st.Reconcile())
	s.Require().NoError(os.WriteFile(path, original, 0600))
	_, err = s.st.Append("native", s.events)
	s.Require().ErrorContains(err, "outcome_unknown")
	s.Require().NoError(s.st.Reconcile())
	_, err = s.st.Append("native", s.events)
	s.Require().NoError(err)
}
