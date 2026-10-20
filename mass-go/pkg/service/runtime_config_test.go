package service_test

import (
	"encoding/json"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"strings"
	"testing"
)

type RuntimeConfigSuite struct {
	suite.Suite
	svc service.RuntimeService
}

func (s *RuntimeConfigSuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(st.Close()) })
	s.svc = service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": configFactory{}}))
}

type configFactory struct{}

func (configFactory) Validate(runtime.Config) error { return nil }
func (configFactory) New(string, runtime.Config) (runtime.Driver, error) {
	panic("configuration must not construct a Driver")
}
func (s *RuntimeConfigSuite) TestCreationRejectsReadOnlyStatus() {
	_, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}, Status: resource.RuntimeStatus{State: "running"}})
	s.Require().ErrorIs(err, runtime.ErrInvalidArgument)
	items, err := s.svc.ListRuntimes(resource.RuntimeQuery{})
	s.Require().NoError(err)
	s.Empty(items)
}
func (s *RuntimeConfigSuite) TestQueriesValidateBusinessArguments() {
	_, err := s.svc.GetRuntime("../agent")
	s.ErrorIs(err, runtime.ErrInvalidArgument)
	_, err = s.svc.ListRuntimes(resource.RuntimeQuery{State: "imaginary"})
	s.ErrorIs(err, runtime.ErrInvalidArgument)
	_, err = s.svc.UpdateRuntime("../agent", resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}})
	s.ErrorIs(err, runtime.ErrInvalidArgument)
}
func (s *RuntimeConfigSuite) TestConfigurationIsJSONAndReplacesWholeSpec() {
	for _, config := range []runtime.Config{nil, {"value": make(chan int)}} {
		_, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: config}})
		s.Require().ErrorIs(err, runtime.ErrInvalidArgument)
	}
	created, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{"old": true, "large": json.Number("9007199254740993")}}})
	s.Require().NoError(err)
	updated, err := s.svc.UpdateRuntime("agent", resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{"large": json.Number("9007199254740993")}})
	s.Require().NoError(err)
	s.Equal(uint64(2), updated.Metadata.Generation)
	s.Equal(created.Metadata.UID, updated.Metadata.UID)
	s.Equal("stopped", updated.Status.State)
	s.Zero(updated.Status.AppliedGeneration)
	s.NotContains(updated.Spec.Config, "old")
	s.Equal(json.Number("9007199254740993"), updated.Spec.Config["large"])
	same, err := s.svc.UpdateRuntime("agent", updated.Spec)
	s.Require().NoError(err)
	s.Equal(updated.Metadata.Generation, same.Metadata.Generation)
	got, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal(same, got)
	list, err := s.svc.ListRuntimes(resource.RuntimeQuery{Type: "fixture", State: "stopped"})
	s.Require().NoError(err)
	s.Equal([]resource.Runtime{got}, list)
}
func (s *RuntimeConfigSuite) TestConfigurationNamesFollowDeclaredSubdomainConstraint() {
	name := strings.Repeat("a", 64)
	_, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	s.Require().NoError(err)
}
func (s *RuntimeConfigSuite) TestDeepJSONAcceptedAndCyclesRejected() {
	var deep any = "leaf"
	for range 256 {
		deep = map[string]any{"nested": deep}
	}
	_, err := s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "deep"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{"value": deep}}})
	s.Require().NoError(err)
	cycle := map[string]any{}
	cycle["self"] = cycle
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "cycle"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{"value": cycle}}})
	s.ErrorIs(err, runtime.ErrInvalidArgument)
}
func TestRuntimeConfigSuite(t *testing.T) { suite.Run(t, new(RuntimeConfigSuite)) }
