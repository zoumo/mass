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
	work          sync.Map
	release       chan struct{}
	releaseEvents sync.Once
}

func newCodexTurnObservation() *codexTurnObservation {
	return &codexTurnObservation{release: make(chan struct{})}
}

func (o *codexTurnObservation) releaseFacts() { o.releaseEvents.Do(func() { close(o.release) }) }

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
	if _, ok := e.Payload().(events.StateUpdate); ok {
		s.observation.work.Store(e.EventID, e.Source().Work())
	}
	select {
	case <-s.observation.release:
		return e, nil
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}
