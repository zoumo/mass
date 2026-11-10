package claude

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
)

type lifecycleProvider struct {
	agent.Provider
	stops int
	err   error
}

func (p *lifecycleProvider) Stop(context.Context) error { p.stops++; return p.err }

func TestStopCachesAdmittedReleaseFailure(t *testing.T) {
	provider := &lifecycleProvider{err: &agent.Error{Code: agent.ErrorUnavailable, Reason: "release_failed"}}
	driver := &Driver{provider: provider, pipe: events.NewEventPipe(128), sessions: map[string]*session{}, cancel: func() {}}
	first := driver.Stop(t.Context())
	require.Error(t, first)
	require.Same(t, first, driver.Stop(t.Context()))
	require.Equal(t, 1, provider.stops)
	_, err := driver.Start(t.Context())
	require.Error(t, err, "admitted stop is terminal even when release fails")
}

func TestStopRejectsOpenOrResumeInFlightWithoutReleasing(t *testing.T) {
	provider := &lifecycleProvider{}
	driver := &Driver{provider: provider, pipe: events.NewEventPipe(128), sessions: map[string]*session{}, cancel: func() {}}
	driver.opening = 1
	require.Error(t, driver.Stop(t.Context()))
	require.Zero(t, provider.stops)
	driver.opening = 0
	require.NoError(t, driver.Stop(t.Context()))
	require.Equal(t, 1, provider.stops)
}

func TestStopRetainsOwnershipWhenSDKRejectsAdmission(t *testing.T) {
	provider := &lifecycleProvider{err: &agent.Error{Code: agent.ErrorFailedPrecondition, Reason: "provider_has_open_sessions"}}
	driver := &Driver{provider: provider, pipe: events.NewEventPipe(128), sessions: map[string]*session{}, cancel: func() {}}
	require.Error(t, driver.Stop(t.Context()))
	require.False(t, driver.stopped)
	require.False(t, driver.stopping)
	provider.err = nil
	require.NoError(t, driver.Stop(t.Context()))
	require.Equal(t, 2, provider.stops)
}

type closeResultSession struct {
	agent.Session
	closes int
	err    error
}

func (s *closeResultSession) Close(context.Context) error { s.closes++; return s.err }

func TestConcurrentCloseCachesSDKFailure(t *testing.T) {
	native := &closeResultSession{err: &agent.Error{Code: agent.ErrorUnavailable, Reason: "close_failed"}}
	provider := &lifecycleProvider{}
	driver := &Driver{provider: provider, pipe: events.NewEventPipe(128), sessions: map[string]*session{}, cancel: func() {}}
	driver.sessions["native"] = &session{native: native}
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Go(func() { results <- driver.CloseSession(t.Context(), "native") })
	}
	group.Wait()
	close(results)
	var first error
	for err := range results {
		require.Error(t, err)
		if first == nil {
			first = err
		}
		require.Same(t, first, err)
	}
	require.Equal(t, 1, native.closes)
	require.Error(t, driver.Stop(t.Context()), "failed close retains the original binding")
	require.Zero(t, provider.stops)
}

func (s *closeResultSession) Events() (agent.EventStream, error)        { return s, nil }
func (s *closeResultSession) Next(context.Context) (agent.Event, error) { return agent.Event{}, io.EOF }
func (s *closeResultSession) Err() error                                { return errors.New("old observation failure") }

func TestCloseAfterNaturalEOFSettlesDrainedOwnership(t *testing.T) {
	native := &closeResultSession{}
	provider := &lifecycleProvider{}
	driver := &Driver{provider: provider, pipe: events.NewEventPipe(128), sessions: map[string]*session{}, cancel: func() {}}
	relay, err := agentrelay.Start(t.Context(), agentrelay.Config{Session: native, Address: runtime.Address{Namespace: "one", SessionName: "ended"}, Sink: func(context.Context, events.SessionEvent) error { return nil }})
	require.NoError(t, err)
	<-relay.Done()
	driver.sessions["native"] = &session{native: native, relay: relay}
	require.NoError(t, driver.CloseSession(t.Context(), "native"), "successful SDK close settles ownership independently of observation health")
	require.NoError(t, driver.CloseSession(t.Context(), "native"))
	require.Equal(t, 1, native.closes)
	require.NoError(t, driver.Stop(t.Context()))
	require.Equal(t, 1, provider.stops)
	require.NoError(t, driver.CloseSession(t.Context(), "native"))
}
