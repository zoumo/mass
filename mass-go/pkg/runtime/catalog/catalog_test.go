package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type catalogSuite struct{ suite.Suite }

func TestCatalog(t *testing.T) { suite.Run(t, new(catalogSuite)) }
func (s *catalogSuite) TestRegistrationIsSideEffectFree() {
	c := catalog.New()
	s.Len(c.Types(), 2)
	for _, description := range c.Types() {
		described, err := c.Describe(description.Name)
		s.Require().NoError(err)
		s.Equal(description, described)
		s.Equal(128, description.MaxNameBytes)
		s.Len(description.ConfigFields, 3)
	}
	_, err := c.Describe("plugin")
	s.ErrorIs(err, rt.ErrNotFound)
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			registration := rt.Registration{Name: kind + "-local", Type: kind, Config: rt.Config{Executable: "/nonexistent/synthetic-cli", Env: []string{}}}
			snapshot, err := c.Register(registration)
			s.Require().NoError(err)
			s.Equal("registered", snapshot.Phase)
			s.Equal(uint64(1), snapshot.ConfigRevision)
			s.Equal(uint64(0), snapshot.ObservedRevision)
			s.Equal("unknown", snapshot.Health.Connected.State)
			_, err = c.Register(registration)
			s.ErrorIs(err, rt.ErrConflict)
			runtime, err := c.Get(registration.Name)
			s.Require().NoError(err)
			s.Empty(runtime.ListSessions())
			s.Error(runtime.Start(context.Background()))
		})
	}
	for _, registration := range []rt.Registration{
		{Name: "unknown", Type: "plugin", Config: rt.Config{Executable: "/synthetic", Env: []string{}}},
		{Name: "", Type: "codex", Config: rt.Config{Executable: "/synthetic", Env: []string{}}},
		{Name: "bad", Type: "claude"},
		{Name: "relative", Type: "codex", Config: rt.Config{Executable: "codex", Env: []string{}}},
		{Name: "bad-env", Type: "codex", Config: rt.Config{Executable: "/synthetic", Env: []string{"INVALID"}}},
		{Name: "bad-dir", Type: "claude", Config: rt.Config{Executable: "/synthetic", Env: []string{}, Dir: "relative"}},
	} {
		_, err := c.Register(registration)
		s.Error(err)
	}
	s.Len(c.List(), 2)
}

func (s *catalogSuite) TestConfigurationRevisionAndRetiredHandles() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			c := catalog.New()
			config := rt.Config{Executable: "/synthetic/original", Env: []string{}}
			_, err := c.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			old, err := c.Get("worker")
			s.Require().NoError(err)
			config.Executable = "/synthetic/updated"
			_, err = c.UpdateConfig("worker", config, 2)
			s.ErrorIs(err, rt.ErrConflict)
			snapshot, err := c.UpdateConfig("worker", config, 1)
			s.Require().NoError(err)
			s.Equal(uint64(2), snapshot.ConfigRevision)
			s.Equal(uint64(0), snapshot.ObservedRevision)
			s.ErrorIs(old.Start(context.Background()), rt.ErrConflict)
			s.NoError(c.Unregister("worker"))
			_, err = c.Get("worker")
			s.ErrorIs(err, rt.ErrNotFound)
		})
	}
}
