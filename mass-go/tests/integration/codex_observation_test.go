package integration_test

import (
	"context"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Observe the public Driver stream before Broadcast removes execution-only
// Source. UUID correlation leaves both the events and the sole consumer intact.
type codexTurnObservation struct {
	work           sync.Map
	initial        chan struct{}
	rest           chan struct{}
	initialRelease sync.Once
	restRelease    sync.Once
}

func newCodexTurnObservation() *codexTurnObservation {
	return &codexTurnObservation{initial: make(chan struct{}), rest: make(chan struct{})}
}

func (o *codexTurnObservation) releaseInitial() { o.initialRelease.Do(func() { close(o.initial) }) }
func (o *codexTurnObservation) releaseRest()    { o.restRelease.Do(func() { close(o.rest) }) }
func (o *codexTurnObservation) releaseAll() {
	o.releaseInitial()
	o.releaseRest()
}

type observedCodexFactory struct {
	runtime.Factory
	observation *codexTurnObservation
}

func (f observedCodexFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	driver, err := f.Factory.New(name, config)
	if err != nil {
		return nil, err
	}
	return observedCodexDriver{Driver: driver, observation: f.observation}, nil
}

type observedCodexDriver struct {
	runtime.Driver
	observation *codexTurnObservation
}

func (d observedCodexDriver) Events() (events.Stream, error) {
	stream, err := d.Driver.Events()
	if err != nil {
		return nil, err
	}
	return observedCodexStream{Stream: stream, observation: d.observation}, nil
}

type observedCodexStream struct {
	events.Stream
	observation *codexTurnObservation
}

func (s observedCodexStream) Next(ctx context.Context) (events.SessionEvent, error) {
	e, err := s.Stream.Next(ctx)
	if err != nil {
		return e, err
	}
	gate := s.observation.rest
	if state, ok := e.Payload().(events.StateUpdate); ok {
		s.observation.work.Store(e.EventID, e.Source().Work())
		if state.State != nil && *state.State == events.Idle && e.Source().Work() == "" {
			// Deliver the real opening idle only after SendPrompt returns; hold
			// later facts until the test has observed that it is not completion.
			gate = s.observation.initial
		}
	}
	select {
	case <-gate:
		return e, nil
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}
