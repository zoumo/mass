package service

import (
	"context"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const DefaultSessionIdleTimeout = 10 * time.Minute

// Timer and Clock keep idle scheduling deterministic in tests.
type Timer interface {
	Stop() bool
}

type Clock interface {
	AfterFunc(time.Duration, func()) Timer
}

type systemClock struct{}

func (systemClock) AfterFunc(delay time.Duration, callback func()) Timer {
	return time.AfterFunc(delay, callback)
}

type Options struct {
	InteractionPolicy  InteractionPolicy
	SessionIdleTimeout time.Duration
	Clock              Clock
}

func (s *Service) invalidateIdleLocked(l *lane) {
	if l == nil {
		return
	}
	stopIdleTimerLocked(l)
}

func stopIdleTimerLocked(l *lane) {
	l.idleVersion++
	if l.idleTimer != nil {
		l.idleTimer.Stop()
		l.idleTimer = nil
	}
}

func (s *Service) scheduleIdleLocked(run *runBinding, l *lane) {
	if l == nil {
		return
	}
	stopIdleTimerLocked(l)
	if s.idleTimeout <= 0 || !s.idleCurrentLocked(run, l, l.idleVersion) {
		return
	}
	if !l.admission.TryLock() {
		return
	}
	l.admission.Unlock()

	version := l.idleVersion
	l.idleTimer = s.clock.AfterFunc(s.idleTimeout, func() {
		go s.expireIdle(run, l, version)
	})
}

func (s *Service) rearmIdleAfterActivity(run *runBinding, l *lane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleIdleLocked(run, l)
}

func (s *Service) expireIdle(run *runBinding, l *lane, version uint64) {
	s.mu.Lock()
	if !s.idleCurrentLocked(run, l, version) {
		s.mu.Unlock()
		return
	}
	l.idleTimer = nil
	s.mu.Unlock()

	ref := resource.SessionRef{SessionID: l.session.Status.SessionID}
	// A rejected guard means newer activity won the race. Close failures are
	// recorded as closing by the shared close path and are never retried here.
	_, _ = s.closeSessionWithGuard(context.Background(), ref, func(current resource.Session) bool {
		return current.Status.State == events.Idle && s.idleCurrentLocked(run, l, version)
	})
}

func (s *Service) idleCurrentLocked(run *runBinding, l *lane, version uint64) bool {
	if s.idleTimeout <= 0 || s.closing || run == nil || l == nil {
		return false
	}
	id := l.session.Status.SessionID
	key := addressKey(runtime.Address{Namespace: l.session.Metadata.Namespace, SessionName: l.session.Metadata.Name})
	if s.runs[l.session.Spec.RuntimeRef] != run || run.lanes[id] != l || run.pending[key] != nil {
		return false
	}
	if run.stopping || run.err != nil || !run.started || l.idleVersion != version {
		return false
	}
	if l.closing || l.releasing || l.overloaded || !l.stateObserved || l.observedState != events.Idle {
		return false
	}
	if len(l.activeTools) != 0 || len(l.interactions) != 0 {
		return false
	}
	if op := s.sessionOps[id]; op != nil && op.closing {
		return false
	}
	return true
}
