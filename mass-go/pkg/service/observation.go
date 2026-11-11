package service

import (
	"errors"
	"log/slog"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const sessionObservationLost = "session_observation_lost"

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
		if s.runs[l.session.Spec.RuntimeRef] != run || run.lanes[native.SessionID] != l ||
			!l.source.SameRun(native.Source) || l.closing || run.stopping {
			s.mu.Unlock()
			return
		}
		l.err = errors.Join(l.err, cause)
		s.invalidateIdleLocked(l)
		s.mu.Unlock()
		s.failLoad(run, l, "failed", cause)
		s.observationLost(run, l, native)
	}()
}

func (s *Service) observationLost(run *runBinding, l *lane, native runtime.DriverSession) {
	unlock, err := s.sessionOperation(native.SessionID, false)
	if err != nil {
		return
	}
	defer unlock()
	s.mu.Lock()
	current := s.runs[l.session.Spec.RuntimeRef] == run && run.lanes[native.SessionID] == l &&
		l.source.SameRun(native.Source) && !l.closing && !run.stopping
	sessionUID := l.session.Metadata.UID
	s.mu.Unlock()
	if !current {
		return
	}
	_, err = s.sessionStatus(resource.SessionRef{SessionID: native.SessionID}, func(session *resource.Session) {
		if session.Metadata.UID != sessionUID || session.Status.State == "closing" || session.Status.State == "offline" {
			return
		}
		session.Status.State = events.Unknown
		session.Status.Reason = sessionObservationLost
	})
	if err != nil {
		slog.Warn("session observation loss status failed", "session_id", native.SessionID, "error", err)
	}
}
