package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const sessionCloseFailed = "session_close_failed"

func closeOperationError() error {
	return &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  sessionCloseFailed,
		Message: "Session close did not complete; inspect state before retrying",
	}
}

func (s *Service) CloseSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	if err := ctx.Err(); err != nil {
		return resource.Session{}, err
	}
	return awaitSessionLifecycle(ctx, func(owner context.Context) (resource.Session, error) {
		return s.closeSession(owner, ref)
	})
}

func (s *Service) closeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	return s.closeSessionWithGuard(ctx, ref, nil)
}

func (s *Service) closeSessionWithGuard(ctx context.Context, ref resource.SessionRef, guard func(resource.Session) bool) (resource.Session, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return r, err
	}
	id := r.Status.SessionID
	ref = resource.SessionRef{SessionID: id}
	var eligible func() bool
	if guard != nil {
		eligible = func() bool { return guard(r) }
	}
	unlock, err := s.sessionOperationGuarded(id, true, eligible)
	if err != nil {
		return r, err
	}
	defer unlock()

	r, err = s.GetSession(ref)
	if err != nil {
		return r, s.failClose(ref, err)
	}
	prior := r.Status.State
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	if run != nil {
		s.invalidateIdleLocked(run.lanes[id])
	}
	s.mu.Unlock()
	if run == nil {
		return s.closeWithoutHandle(ref, r, prior, false, false)
	}
	run.admission.RLock()
	defer run.admission.RUnlock()

	r, err = s.GetSession(ref)
	if err != nil {
		return r, s.failClose(ref, err)
	}
	address := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	key := addressKey(address)
	s.mu.Lock()
	if s.runs[r.Spec.RuntimeRef] != run {
		s.mu.Unlock()
		return r, s.failClose(ref, runtime.FailedPrecondition("runtime ownership changed during close"))
	}
	l := run.lanes[id]
	pending := run.pending[key]
	if l != nil {
		l.closing = true
		l.releasing = true
	}
	s.mu.Unlock()
	if l == nil && pending == nil {
		return s.closeWithoutHandle(ref, r, prior, true, false)
	}
	if _, err = s.sessionStatus(ref, func(r *resource.Session) {
		r.Status.State = "closing"
		r.Status.Reason = ""
	}); err != nil {
		return r, s.failClose(ref, err)
	}

	if l == nil {
		select {
		case <-pending.done:
		case <-ctx.Done():
			return r, s.failClose(ref, ctx.Err())
		}
		r, err = s.GetSession(ref)
		if err != nil {
			return r, s.failClose(ref, err)
		}
		if len(pending.events) != 0 {
			if _, err = s.settlePendingEvents(ctx, run, pending, key, r); err != nil {
				return r, s.failClose(ref, err)
			}
		}
		if err := s.closePendingTarget(ctx, run, pending); err != nil {
			return r, s.failClose(ref, err)
		}
		s.mu.Lock()
		l = run.lanes[id]
		if l != nil {
			l.nativeClosed = pending.released
		}
		settled := openingSettled(pending)
		if l != nil {
			l.closing = true
			l.releasing = true
		}
		s.mu.Unlock()
		if l == nil {
			if !settled {
				return r, s.failClose(ref, runtime.FailedPrecondition("original session responsibility remains unsettled"))
			}
			closed, closeErr := s.closeWithoutHandle(ref, r, prior, true, true)
			if closeErr == nil {
				s.mu.Lock()
				if run.pending[key] == pending {
					delete(run.pending, key)
				}
				s.mu.Unlock()
			}
			return closed, closeErr
		}
	}

	if err := s.sealLane(ctx, l); err != nil {
		return r, s.failClose(ref, err)
	}
	loadClosed, err := s.closeHistoryLoad(ctx, run, l)
	if err != nil {
		return r, s.failClose(ref, err)
	}
	if loadClosed {
		s.mu.Lock()
		l.nativeClosed = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	nativeClosed := l.nativeClosed
	s.mu.Unlock()
	if !nativeClosed {
		if err := run.driver.CloseSession(ctx, r.Status.SessionID); err != nil {
			return r, s.failClose(ref, err)
		}
		s.mu.Lock()
		l.nativeClosed = true
		s.mu.Unlock()
	}
	if err := s.drainLane(ctx, l); err != nil {
		return r, s.failClose(ref, err)
	}
	s.mu.Lock()
	publicationErr := l.publicationErr
	s.mu.Unlock()
	if publicationErr != nil {
		return r, s.failClose(ref, publicationErr)
	}
	closed, err := s.closeOffline(ref)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	retireLane(run, l)
	if run.pending[key] == pending {
		delete(run.pending, key)
	}
	s.mu.Unlock()
	return closed, nil
}

// A pending owner can outlive its reservation with a verified native target but
// without a lane. Close must release that target before settling local state.
func (s *Service) closePendingTarget(ctx context.Context, run *runBinding, pending *opening) error {
	s.mu.Lock()
	native := pending.native
	released := pending.released
	s.mu.Unlock()
	if native.SessionID == "" || released {
		return nil
	}
	if err := run.driver.CloseSession(ctx, native.SessionID); err != nil {
		return err
	}
	s.mu.Lock()
	pending.released = true
	s.mu.Unlock()
	return nil
}

// A pending opening can have accepted events before its delivery lane exists.
// Register a closing lane and move that bounded prefix into it before releasing
// the native target, so close-time facts still have their original owner.
func (s *Service) settlePendingEvents(ctx context.Context, run *runBinding, pending *opening, key string, r resource.Session) (*lane, error) {
	s.mu.Lock()
	if existing := run.lanes[r.Status.SessionID]; existing != nil {
		s.mu.Unlock()
		return existing, nil
	}
	native := pending.native
	if native.SessionID == "" || native.SessionID != r.Status.SessionID || resource.ValidateSessionID(native.SessionID) != nil || !native.Source.SameRun(native.Source) {
		s.mu.Unlock()
		return nil, runtime.FailedPrecondition("original session responsibility remains unsettled")
	}
	l := &lane{session: r, source: native.Source.WithWork(""), queue: make(chan delivery, laneRetentionCapacity), retry: make(chan chan error), done: make(chan struct{})}
	l.closing = true
	l.releasing = true
	pending.buffering = true
	s.mu.Unlock()

	for {
		s.mu.Lock()
		if len(pending.events) == 0 {
			pending.buffering = false
			l.err = pending.err
			run.lanes[r.Status.SessionID] = l
			if run.pending[key] == pending {
				delete(run.pending, key)
			}
			s.mu.Unlock()
			go s.process(run, l)
			return l, nil
		}
		batch := pending.events
		pending.events = nil
		s.mu.Unlock()
		for i, e := range batch {
			if !l.source.SameRun(e.Source()) {
				err := errors.New("pending event source mismatch")
				s.mu.Lock()
				pending.events = append(batch[i:], pending.events...)
				pending.err = err
				l.err = err
				s.mu.Unlock()
				return nil, err
			}
			select {
			case l.queue <- delivery{event: e}:
			case <-ctx.Done():
				s.mu.Lock()
				pending.events = append(batch[i:], pending.events...)
				pending.err = ctx.Err()
				l.err = ctx.Err()
				s.mu.Unlock()
				return nil, ctx.Err()
			}
		}
	}
}

func (s *Service) closeWithoutHandle(ref resource.SessionRef, r resource.Session, prior events.SessionState, allowOpening, allowClosing bool) (resource.Session, error) {
	if unresolvedHistoryLoad(r) {
		return r, s.failClose(ref, runtime.FailedPrecondition("unresolved history load remains owned"))
	}
	switch prior {
	case "offline":
		return s.closeOffline(ref)
	case events.Unknown:
		return s.closeOffline(ref)
	case "opening":
		if allowOpening || r.Status.AppliedGeneration == 0 {
			return s.closeOffline(ref)
		}
	case "closing":
		if allowClosing || r.Status.AppliedGeneration == 0 {
			return s.closeOffline(ref)
		}
	}
	return r, s.failClose(ref, runtime.FailedPrecondition("session release not confirmed in this process"))
}

func unresolvedHistoryLoad(r resource.Session) bool {
	h := r.Status.HistoryLoad
	return h != nil && h.Phase != "completed" && !h.CleanupCompleted
}

func (s *Service) closeOffline(ref resource.SessionRef) (resource.Session, error) {
	closed, err := s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r) })
	if err != nil {
		return closed, s.failClose(ref, err)
	}
	return closed, nil
}

// A pending owner has settled only when it either produced no native target or
// proved that the target was released. A nil error without a handle is also
// settled for reservations that were removed after local-only acceptance.
func openingSettled(op *opening) bool {
	if len(op.events) != 0 {
		return false
	}
	if op.native.SessionID == "" {
		return op.released || op.err == nil || errors.Is(op.err, runtime.ErrResourcesReleased)
	}
	return op.released
}

// A close during history load terminates that operation's local responsibility
// instead of opening another native target. A successful load cleanup has
// already released its own connection, so no second SDK close is required.
func (s *Service) closeHistoryLoad(ctx context.Context, run *runBinding, l *lane) (bool, error) {
	h := l.load
	if h == nil || h.complete.Load() {
		return false, nil
	}
	s.failLoad(run, l, "canceled", runtime.FailedPrecondition("session closed during history load"))
	h.mu.Lock()
	launched := h.cleanupStarted
	h.mu.Unlock()
	if launched {
		select {
		case <-h.cleanupDone:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	h.mu.Lock()
	cleanupErr := h.cleanupErr
	cleanupStatusErr := h.cleanupStatusErr
	loadID := h.id
	h.mu.Unlock()
	if cleanupErr == nil {
		if cleanupStatusErr != nil {
			if err := s.writeLoad(l); err != nil {
				h.mu.Lock()
				h.cleanupStatusErr = err
				h.mu.Unlock()
				return false, err
			}
			h.mu.Lock()
			h.cleanupStatusErr = nil
			h.mu.Unlock()
		}
		return true, nil
	}

	// A prior close attempt may have left only this idempotent load cleanup.
	// Repeating it is the explicit retry; it never launches a new load or open.
	if err := run.driver.CancelHistoryLoad(ctx, l.session.Status.SessionID, loadID); err != nil {
		return false, err
	}
	if err := s.drainLane(ctx, l); err != nil {
		return false, err
	}
	h.mu.Lock()
	h.cleanupErr = nil
	h.status.CleanupCompleted = true
	h.mu.Unlock()
	if err := s.writeLoad(l); err != nil {
		h.mu.Lock()
		h.cleanupStatusErr = err
		h.mu.Unlock()
		return false, err
	}
	h.mu.Lock()
	h.cleanupStatusErr = nil
	h.mu.Unlock()
	return true, nil
}

func (s *Service) failClose(ref resource.SessionRef, cause error) error {
	if cause != nil {
		slog.Warn("Session close unsuccessful", "session_id", ref.SessionID, "reason", closeFailureReason(cause))
	}
	if _, err := s.sessionStatus(ref, func(r *resource.Session) {
		r.Status.State = "closing"
		r.Status.Reason = sessionCloseFailed
	}); err != nil {
		slog.Warn("Session close failure status was not committed", "session_id", ref.SessionID, "reason", "status_commit_failed")
	}
	return closeOperationError()
}

func closeFailureReason(cause error) string {
	var public *runtime.OperationError
	if errors.As(cause, &public) {
		return public.Code + ":" + public.Reason
	}
	switch {
	case errors.Is(cause, context.Canceled):
		return "context_canceled"
	case errors.Is(cause, context.DeadlineExceeded):
		return "context_deadline_exceeded"
	default:
		return "internal_error"
	}
}
