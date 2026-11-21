package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const sessionObservationLost = "session_observation_lost"

const (
	turnReconcileTimeout = 15 * time.Second
	turnReadTimeout      = 2 * time.Second
	turnReleaseTimeout   = 30 * time.Second
)

// Relay termination belongs to the original Session binding. It must not end
// the Driver stream or manufacture a provider state event.
func (s *Service) watchObservation(run *runBinding, l *lane, native runtime.DriverSession) {
	if native.Observation == nil {
		return
	}
	go func() {
		select {
		case <-native.Observation.Done():
		case <-run.done:
			return
		case <-l.done:
			return
		}
		cause := native.Observation.Err()
		if cause == nil {
			return
		}
		s.mu.Lock()
		if !s.observationCurrentLocked(run, l, native) {
			s.mu.Unlock()
			return
		}
		l.err = errors.Join(l.err, cause)
		s.invalidateIdleLocked(l)
		s.mu.Unlock()
		s.failLoad(run, l, "failed", cause)
		if s.observationLost(run, l, native) {
			if reader, ok := run.driver.(runtime.TurnReconciler); ok {
				ctx, cancel := context.WithTimeout(context.Background(), turnReconcileTimeout)
				defer cancel()
				s.reconcileObservation(ctx, run, l, native, reader)
			}
		}
	}()
}

func (s *Service) observationLost(run *runBinding, l *lane, native runtime.DriverSession) bool {
	unlock, err := s.sessionOperation(native.SessionID, false)
	if err != nil {
		return false
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.observationCurrentLocked(run, l, native) {
		return false
	}
	saved, err := s.sessionStatus(resource.SessionRef{SessionID: native.SessionID}, func(session *resource.Session) {
		if session.Metadata.UID != l.session.Metadata.UID || session.Status.State == "closing" || session.Status.State == "offline" {
			return
		}
		session.Status.State = events.Unknown
		session.Status.Reason = sessionObservationLost
	})
	if err != nil {
		slog.Warn("session observation loss status failed", "session_id", native.SessionID, "error", err)
	}
	return err == nil && saved.Metadata.UID == l.session.Metadata.UID && saved.Status.Reason == sessionObservationLost
}

func (s *Service) observationCurrentLocked(run *runBinding, l *lane, native runtime.DriverSession) bool {
	return !s.closing && !run.stopping && !l.closing && !l.releasing &&
		s.runs[l.session.Spec.RuntimeRef] == run && run.lanes[native.SessionID] == l && l.source.SameRun(native.Source)
}

func (s *Service) reconcileObservation(ctx context.Context, run *runBinding, l *lane, native runtime.DriverSession, reader runtime.TurnReconciler) {
	for delay := 250 * time.Millisecond; ctx.Err() == nil; delay = min(2*delay, 4*time.Second) {
		s.mu.Lock()
		current := s.observationCurrentLocked(run, l, native) && !l.source.SameRun(l.unknownInputSource)
		ready := current && s.turnReconcileReadyLocked(l)
		s.mu.Unlock()
		if !current {
			return
		}
		if ready {
			readCtx, cancel := context.WithTimeout(ctx, turnReadTimeout)
			result, err := reader.ReconcileTurn(readCtx, native.SessionID, native.Source)
			cancel()
			if err == nil && result.TurnID != "" && ctx.Err() == nil {
				switch result.Status {
				case "completed", "failed", "interrupted":
					if !result.Latest || s.releaseObservationOwner(run, l, native) {
						return
					}
				}
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-run.done:
		case <-l.done:
		case <-timer.C:
			continue
		}
		timer.Stop()
		return
	}
}

// An input may return its native Turn ID after observation has failed. Wait
// for admitted dispatch to settle and retain the fence on indeterminate input.
func (s *Service) turnReconcileReadyLocked(l *lane) bool {
	if l.err == nil || l.inputInFlight || l.source.SameRun(l.unknownInputSource) ||
		(l.load != nil && !l.load.complete.Load()) || !l.admission.TryLock() {
		return false
	}
	l.admission.Unlock()
	return true
}

// A conflicting admission can reject the guard before close starts. Retry the
// observation and guard within the original budget. A failed close has already
// taken ownership and is recorded by closeSessionWithGuard.
func (s *Service) releaseObservationOwner(run *runBinding, l *lane, native runtime.DriverSession) bool {
	ctx, cancel := context.WithTimeout(context.Background(), turnReleaseTimeout)
	defer cancel()
	_, err := s.closeSessionWithGuard(ctx, resource.SessionRef{SessionID: native.SessionID}, func(current resource.Session) bool {
		return current.Metadata.UID == l.session.Metadata.UID && current.Status.State == events.Unknown &&
			current.Status.Reason == sessionObservationLost &&
			s.observationCurrentLocked(run, l, native) && s.turnReconcileReadyLocked(l)
	})
	return !errors.Is(err, errSessionOperationGuardRejected)
}
