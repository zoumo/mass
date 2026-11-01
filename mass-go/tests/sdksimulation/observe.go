package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Observe the public Runtime stream in-line, preserving Service's sole receive
// ownership. History intentionally does not serialize the execution-only Source.
type observations struct {
	mu     sync.Mutex
	opened runtime.DriverSession
	events []events.SessionEvent
}

func (o *observations) verify(id string, receipts []runtime.InputReceipt, settled bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.opened.SessionID != id {
		return errors.New("native identity mismatch")
	}
	for i, receipt := range receipts {
		if receipt.Submission == "" || receipt.Acceptance.Sent == nil || !*receipt.Acceptance.Sent {
			return errors.New("input receipt lacks sent submission")
		}
		if i > 0 && receipt.Submission == receipts[i-1].Submission {
			return errors.New("reused submission receipt")
		}
	}
	messages, completed := 0, 0
	for _, event := range o.events {
		if event.SessionID != id || event.Namespace != "simulation" || event.SessionName != "continuation" || !event.Source().SameRun(o.opened.Source) {
			return errors.New("runtime event native identity/routing/run mismatch")
		}
		switch value := event.Payload().(type) {
		case events.SessionError:
			return fmt.Errorf("MASS session error: %s", value.Message)
		case events.AgentMessage:
			if messages >= len(receipts) || messages != completed || event.Source().Work() != receipts[messages].Submission {
				return errors.New("reply work ownership/order mismatch")
			}
			messages++
		case events.StateUpdate:
			if value.State == nil || *value.State != events.Idle || event.Source().Work() == "" {
				continue
			}
			if completed >= len(receipts) || messages != completed+1 || event.Source().Work() != receipts[completed].Submission {
				return errors.New("completion work ownership/order mismatch")
			}
			completed++
		}
	}
	if settled && (messages != len(receipts) || completed != len(receipts)) {
		return errors.New("missing owned reply/completion")
	}
	return nil
}

func (o *observations) write(path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries := make([]map[string]any, 0, len(o.events))
	for _, event := range o.events {
		entries = append(entries, map[string]any{"event": event, "work": event.Source().Work(), "sameRun": event.Source().SameRun(o.opened.Source)})
	}
	return writeJSON(path, entries)
}

type observedFactory struct {
	runtime.Factory
	observations *observations
}

func (f observedFactory) New(name string, config runtime.Config) (runtime.Driver, error) {
	d, err := f.Factory.New(name, config)
	if err != nil {
		return nil, err
	}
	return &observedDriver{Driver: d, observations: f.observations}, nil
}

type observedDriver struct {
	runtime.Driver
	observations *observations
}

func (d *observedDriver) ValidateInitialInput(input runtime.Input) error {
	return d.Driver.(runtime.CreationDriver).ValidateInitialInput(input)
}

func (d *observedDriver) OpenSessionWithInput(ctx context.Context, address runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	session, err := d.Driver.(runtime.CreationDriver).OpenSessionWithInput(ctx, address, spec, input)
	d.observations.mu.Lock()
	d.observations.opened = session
	d.observations.mu.Unlock()
	return session, err
}

func (d *observedDriver) Events() (events.Stream, error) {
	stream, err := d.Driver.Events()
	if err != nil {
		return nil, err
	}
	return observedStream{Stream: stream, observations: d.observations}, nil
}

type observedStream struct {
	events.Stream
	observations *observations
}

func (s observedStream) Next(ctx context.Context) (events.SessionEvent, error) {
	event, err := s.Stream.Next(ctx)
	if err == nil {
		s.observations.mu.Lock()
		s.observations.events = append(s.observations.events, event)
		s.observations.mu.Unlock()
	}
	return event, err
}
