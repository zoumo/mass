package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// ReconcileOpening explicitly settles an uncertain identity write against its
// original native owner. It never opens another Agent or substitutes an ID.
func (s *Service) ReconcileOpening(ctx context.Context, name string, address runtime.Address) error {
	s.mu.Lock()
	run := s.runs[name]
	s.mu.Unlock()
	if run == nil {
		return errors.New("original runtime owner unavailable")
	}
	if err := lockOperation(ctx, &run.op); err != nil {
		return err
	}
	defer run.op.Unlock()
	key := addressKey(address)
	s.mu.Lock()
	op := run.pending[key]
	s.mu.Unlock()
	if op == nil {
		return errors.New("no original opening responsibility")
	}
	select {
	case <-op.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	native, released := op.native, op.released
	s.mu.Unlock()
	if native.SessionID == "" {
		return errors.New("outcome_unknown: native identity remains unknown")
	}
	if !released {
		if err := run.driver.CloseSession(ctx, native.SessionID); err != nil {
			return err
		}
		s.mu.Lock()
		op.released = true
		s.mu.Unlock()
	}
	// An illegal native identity was never eligible for persistence. Once the
	// original resource is released, discard its unpublished facts and reservation;
	// an existing persistent Session (on restore) remains untouched.
	if resource.ValidateSessionID(native.SessionID) != nil {
		s.mu.Lock()
		delete(run.pending, key)
		s.mu.Unlock()
		return nil
	}
	r, err := s.meta.GetSession(resource.SessionRef{Namespace: address.Namespace, Name: address.SessionName})
	if errors.Is(err, store.ErrNotFound) {
		s.mu.Lock()
		delete(run.pending, key)
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	if r.Status.SessionID != native.SessionID {
		return errors.New("identity write conflicts with original native owner")
	}
	a, err := json.Marshal(r.Spec)
	if err != nil {
		return err
	}
	b, err := json.Marshal(op.requested.Spec)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("identity spec differs from original create")
	}
	s.mu.Lock()
	l := run.lanes[native.SessionID]
	if l == nil {
		l = &lane{session: r, source: native.Source, queue: make(chan delivery, queueCapacity), done: make(chan struct{}), retry: make(chan chan error), closing: true, subscribers: map[*Subscription]bool{}}
		for _, event := range op.events {
			if !native.Source.SameRun(event.Source()) {
				s.mu.Unlock()
				return errors.New("opening source mismatch")
			}
			l.queue <- delivery{event: event}
		}
		run.lanes[native.SessionID] = l
		go s.process(run, l)
	} else if !l.source.SameRun(native.Source) {
		s.mu.Unlock()
		return errors.New("original lane replaced")
	}
	s.mu.Unlock()
	if _, err = s.sessionStatus(resource.SessionRef{SessionID: native.SessionID}, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
		return err
	}
	if err := s.drainLane(ctx, l); err != nil {
		return err
	}
	if _, err = s.sessionStatus(resource.SessionRef{SessionID: native.SessionID}, func(r *resource.Session) { offlineStatus(r, true) }); err != nil {
		return err
	}
	s.mu.Lock()
	delete(run.lanes, native.SessionID)
	delete(run.pending, key)
	close(l.queue)
	s.mu.Unlock()
	<-l.done
	return nil
}
