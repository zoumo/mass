package service_test

import (
	"context"
	"errors"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"testing"
)

type SessionIdentitySuite struct {
	suite.Suite
	svc *service.Service
	st  *file.Store
}

func (s *SessionIdentitySuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.st = st
	s.T().Cleanup(func() { s.NoError(st.Close()) })
	s.svc = service.New(st, st, nil)
}
func (s *SessionIdentitySuite) TestUUIDCreateNameRejectedBeforeRuntimeAccess() {
	for _, name := range []string{"00000000-0000-0000-0000-000000000000", "12345678-abcd-ffff-0000-123456789abc", "12345678-ABCD-FFFF-0000-123456789ABC"} {
		_, err := s.svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: name}, Spec: resource.SessionSpec{RuntimeRef: "missing", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
		var public *runtime.OperationError
		s.Require().ErrorAs(err, &public)
		s.Equal("invalid_argument", public.Code)
		s.Equal("session_name_is_uuid", public.Reason)
	}
	page, err := s.svc.ListSessions(resource.SessionQuery{})
	s.Require().NoError(err)
	s.Empty(page.Items)
}
func TestSessionIdentitySuite(t *testing.T) { suite.Run(t, new(SessionIdentitySuite)) }

func (s *SessionIdentitySuite) seed(ns, name, id string) resource.Session {
	r, err := s.st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: ns, Name: name}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: id, State: "offline"}})
	s.Require().NoError(err)
	return r
}
func (s *SessionIdentitySuite) TestNameOrIDResolvesNamespaceAndDetectsAmbiguity() {
	first := s.seed("one", "alpha", "beta")
	s.seed("two", "beta", "elsewhere")
	for _, target := range []string{"alpha", "beta"} {
		ref, err := s.svc.ResolveSession("one", target)
		s.Require().NoError(err)
		s.Equal(resource.SessionRef{SessionID: "beta"}, ref)
		got, err := s.svc.GetSession(ref)
		s.Require().NoError(err)
		s.Equal(first, got)
	}
	_, err := s.svc.ResolveSession("two", "alpha")
	s.ErrorIs(err, store.ErrNotFound)
	s.seed("one", "beta", "other")
	_, err = s.svc.ResolveSession("one", "beta")
	s.ErrorIs(err, store.ErrConflict)
	same := s.seed("one", "same", "same")
	ref, err := s.svc.ResolveSession("one", "same")
	s.Require().NoError(err)
	got, err := s.svc.GetSession(ref)
	s.Require().NoError(err)
	s.Equal(same, got)
}

func (s *SessionIdentitySuite) TestInvalidIDsRejectedAtEveryServiceEntry() {
	for _, id := range []string{".", "..", string([]byte{0xff})} {
		ref := resource.SessionRef{SessionID: id}
		_, err := s.svc.GetSession(ref)
		s.invalidID(err)
		_, err = s.svc.ResolveSession("one", id)
		s.invalidID(err)
		_, err = s.svc.ResumeSession(context.Background(), ref)
		s.invalidID(err)
		_, err = s.svc.ArchiveSession(context.Background(), ref)
		s.invalidID(err)
		_, err = s.svc.SendPrompt(context.Background(), ref, runtime.Input{})
		s.invalidID(err)
		_, err = s.svc.Cancel(context.Background(), ref)
		s.invalidID(err)
		_, err = s.svc.AnswerInteraction(context.Background(), ref, "interaction", nil)
		s.invalidID(err)
		_, err = s.svc.ListHistoryEvents(ref, store.HistoryQuery{})
		s.invalidID(err)
		_, err = s.svc.SubscribeEvents(ref)
		s.invalidID(err)
	}
}
func (s *SessionIdentitySuite) invalidID(err error) {
	var public *runtime.OperationError
	s.Require().ErrorAs(err, &public)
	s.Equal("invalid_argument", public.Code)
	s.Equal("invalid_session_id", public.Reason)
}

func (s *SessionIdentitySuite) TestDirectoryFiltersAndPaginationWorkOffline() {
	s.seed("one", "a", "a/b")
	s.seed("one", "b", "a%2Fb")
	s.seed("two", "a", "unicode 空间")
	_, err := s.svc.ArchiveSession(context.Background(), resource.SessionRef{SessionID: "a/b"})
	s.Require().NoError(err)
	page, err := s.svc.ListSessions(resource.SessionQuery{Limit: 1})
	s.Require().NoError(err)
	s.Len(page.Items, 1)
	s.NotEmpty(page.Next)
	all := append([]resource.Session{}, page.Items...)
	for page.Next != "" {
		page, err = s.svc.ListSessions(resource.SessionQuery{Limit: 1, After: page.Next})
		s.Require().NoError(err)
		all = append(all, page.Items...)
	}
	s.Len(all, 3)
	filtered, err := s.svc.ListSessions(resource.SessionQuery{Namespace: "one", RuntimeRef: "agent", State: "archived"})
	s.Require().NoError(err)
	s.Require().Len(filtered.Items, 1)
	s.Equal("a/b", filtered.Items[0].Status.SessionID)
	for _, q := range []resource.SessionQuery{{Namespace: "../bad"}, {RuntimeRef: "/bad"}, {State: "invented"}, {Limit: -1}} {
		_, err := s.svc.ListSessions(q)
		s.ErrorIs(err, runtime.ErrInvalidArgument)
	}
}

func (s *SessionIdentitySuite) TestSessionCreateRejectsSystemStatusBeforeSideEffects() {
	_, err := s.svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "missing", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}, Status: resource.SessionStatus{SessionID: "forged"}})
	s.ErrorIs(err, runtime.ErrInvalidArgument)
}

type failingIDDirectory struct{ store.MetaStore }

func (f failingIDDirectory) GetSession(ref resource.SessionRef) (resource.Session, error) {
	if ref.SessionID != "" {
		return resource.Session{}, errors.New("injected directory failure")
	}
	return f.MetaStore.GetSession(ref)
}
func (s *SessionIdentitySuite) TestNameHitDoesNotHideDirectoryFailure() {
	s.seed("one", "demo", "native")
	svc := service.New(failingIDDirectory{s.st}, s.st, nil)
	_, err := svc.ResolveSession("one", "demo")
	s.Require().ErrorContains(err, "injected directory failure")
	s.NotErrorIs(err, store.ErrNotFound)
}
func (s *SessionIdentitySuite) TestReferenceFormsRemainExclusive() {
	s.seed("one", "demo", "native")
	for _, ref := range []resource.SessionRef{{SessionID: "native", Namespace: "one"}, {SessionID: "native", Name: "demo"}, {Namespace: "one"}, {Name: "demo"}, {}} {
		_, err := s.svc.GetSession(ref)
		s.ErrorIs(err, runtime.ErrInvalidArgument)
	}
}
