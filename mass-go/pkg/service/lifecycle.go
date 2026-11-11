package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// Retry only a known conditional-write conflict, always from a fresh snapshot.
// Never retry a possibly committed file mutation or an Agent operation.
func (s *Service) runtimeStatus(name string, change func(*resource.RuntimeStatus)) (resource.Runtime, error) {
	for {
		r, err := s.meta.GetRuntime(name)
		if err != nil {
			return r, err
		}
		change(&r.Status)
		r, err = s.meta.UpdateRuntime(r)
		if !errors.Is(err, store.ErrConflict) {
			return r, err
		}
	}
}

func (s *Service) sessionStatus(ref resource.SessionRef, change func(*resource.Session)) (resource.Session, error) {
	for {
		r, err := s.GetSession(ref)
		if err != nil {
			return r, err
		}
		change(&r)
		r, err = s.meta.UpdateSession(r)
		if !errors.Is(err, store.ErrConflict) {
			return r, err
		}
	}
}

func (s *Service) ListSessions(q resource.SessionQuery) (resource.SessionPage, error) {
	if q.Namespace != "" && !validNamespace(q.Namespace) || q.RuntimeRef != "" && !validName(q.RuntimeRef) || q.Limit < 0 {
		return resource.SessionPage{}, runtime.InvalidArgument("invalid session query")
	}
	if err := resource.ValidateSessionState(q.State); err != nil {
		return resource.SessionPage{}, err
	}
	if q.SessionID != "" {
		if err := resource.ValidateSessionID(q.SessionID); err != nil {
			return resource.SessionPage{}, err
		}
	}
	page, err := s.meta.ListSessions(q)
	if err != nil {
		return resource.SessionPage{}, err
	}
	for _, r := range page.Items {
		if err := resource.ValidateSessionID(r.Status.SessionID); err != nil {
			return resource.SessionPage{}, err
		}
	}
	return page, nil
}

func (s *Service) DeleteRuntime(name string) error {
	if !validName(name) {
		return runtime.InvalidArgument("invalid runtime name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[name] != nil {
		return runtime.FailedPrecondition("runtime still owns running or unsettled resources")
	}
	page, err := s.meta.ListSessions(resource.SessionQuery{RuntimeRef: name, Limit: 1})
	if err != nil {
		return err
	}
	if len(page.Items) != 0 {
		return runtime.FailedPrecondition("runtime has persistent session references")
	}
	r, err := s.meta.GetRuntime(name)
	if err != nil {
		return err
	}
	// A cold process cannot infer release from an old running status.
	if r.Status.State != "stopped" {
		return runtime.FailedPrecondition("runtime release not confirmed")
	}
	return s.meta.DeleteRuntime(name, r.Metadata.ResourceVersion)
}

func (s *Service) drainLane(ctx context.Context, l *lane) error {
	var failed <-chan struct{}
	if l.load != nil {
		s.mu.Lock()
		failed = l.load.deliveryFailed
		s.mu.Unlock()
	}
	b := make(chan struct{})
	select {
	case l.queue <- delivery{barrier: b}:
	case <-failed:
		return runtime.FailedPrecondition("original load history commit requires explicit reconciliation")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b:
	case <-failed:
		return runtime.FailedPrecondition("original load history commit requires explicit reconciliation")
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return l.commitErr
}

func offlineStatus(r *resource.Session) {
	r.Status.State = "offline"
	r.Status.Reason = ""
	r.Status.AppliedGeneration = 0
	r.Status.RuntimeGeneration = 0
	r.Status.Capabilities = nil
	r.Status.EffectiveFeatures = nil
}

// closeOverloadedSession targets only the over-capacity owner observed by the
// stream reader. A later resume or explicit operation cannot become its victim.
func (s *Service) closeOverloadedSession(ctx context.Context, ref resource.SessionRef, expected *runBinding, source events.Source) (resource.Session, error) {
	return s.closeSessionWithGuard(ctx, ref, func(r resource.Session) bool {
		l := expected.lanes[r.Status.SessionID]
		return s.runs[r.Spec.RuntimeRef] == expected && l != nil && l.overloaded && l.source.SameRun(source)
	})
}

// One attempt belongs to the original run; callers share its result and may
// abandon only their wait. Failed attempts retain stopping and can be explicitly
// settled by a later call without replacing the Driver.
type runtimeStop struct {
	done  chan struct{}
	value resource.Runtime
	err   error
}

func (s *Service) StopRuntime(ctx context.Context, name string) (resource.Runtime, error) {
	if err := ctx.Err(); err != nil {
		return resource.Runtime{}, err
	}
	r, err := s.GetRuntime(name)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	run := s.runs[name]
	if run == nil {
		r, err = s.meta.GetRuntime(name)
		s.mu.Unlock()
		if err != nil {
			return r, err
		}
		if r.Status.State == "stopped" {
			return r, nil
		}
		return r, runtime.FailedPrecondition("runtime release not confirmed in this process")
	}
	attempt := run.stop
	if attempt != nil {
		select {
		case <-attempt.done:
			attempt = nil
		default:
		}
	}
	if attempt == nil {
		attempt = &runtimeStop{done: make(chan struct{})}
		run.stop = attempt
		run.stopping = true
		go func() {
			attempt.value, attempt.err = s.stopRuntime(context.WithoutCancel(ctx), name, run)
			close(attempt.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-attempt.done:
		return attempt.value, attempt.err
	case <-ctx.Done():
		return r, &runtime.OperationError{Code: "outcome_unknown", Reason: "runtime_stop_wait_abandoned", Message: "Stop remains owned by the original Runtime; inspect state before further action"}
	}
}

func (s *Service) stopRuntime(ctx context.Context, name string, run *runBinding) (resource.Runtime, error) {
	r, err := s.meta.GetRuntime(name)
	if err != nil {
		return r, err
	}
	// Publish the admission seal even while an original start is still settling.
	if _, err = s.runtimeStatus(name, func(status *resource.RuntimeStatus) { status.State = "stopping" }); err != nil {
		return r, err
	}
	if err := lockOperation(ctx, &run.op); err != nil {
		return r, err
	}
	defer run.op.Unlock()
	// Restores retain admission until their original native call settles. Signal
	// opening owners before waiting for that gate, not after it has drained.
	s.mu.Lock()
	var cancels []context.CancelFunc
	for _, op := range run.pending {
		if op.cancel != nil {
			cancels = append(cancels, op.cancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if err := waitAdmission(ctx, &run.admission); err != nil {
		return r, err
	}
	if run.drained {
		return s.meta.GetRuntime(name)
	}
	r, err = s.meta.GetRuntime(name)
	if err != nil {
		return r, err
	}
	if run.driver == nil {
		return r, errors.New("runtime start failed without confirmed cleanup")
	}
	if _, err = s.runtimeStatus(name, func(status *resource.RuntimeStatus) { status.State = "stopping" }); err != nil {
		return r, err
	}
	s.mu.Lock()
	pending := make([]*opening, 0, len(run.pending))
	for _, op := range run.pending {
		pending = append(pending, op)
	}
	s.mu.Unlock()
	// Cancellation only requests cleanup; original owners must still confirm
	// release and settle their facts before their reservations can be removed.
	for _, op := range pending {
		select {
		case <-op.done:
		case <-ctx.Done():
			return r, ctx.Err()
		}
	}
	s.mu.Lock()
	if len(run.pending) != 0 || run.blocked != nil {
		s.mu.Unlock()
		return r, errors.New("outcome_unknown: original opening or stream responsibility remains")
	}
	s.mu.Unlock()
	s.mu.Lock()
	lanes := make(map[string]*lane, len(run.lanes))
	for id, l := range run.lanes {
		l.closing = true
		stopIdleTimerLocked(l)
		lanes[id] = l
	}
	s.mu.Unlock()
	for id, l := range lanes {
		if l.load != nil && !l.load.complete.Load() {
			s.failLoad(run, l, "canceled", runtime.FailedPrecondition("runtime stopped during history load"))
			select {
			case <-l.load.cleanupDone:
			case <-ctx.Done():
				return r, ctx.Err()
			}
			l.load.mu.Lock()
			cleanupErr := l.load.cleanupErr
			l.load.mu.Unlock()
			if cleanupErr != nil {
				return r, cleanupErr
			}
			l.nativeClosed = true
		}
		if _, err = s.sessionStatus(resource.SessionRef{SessionID: id}, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
			return r, err
		}
		if err := s.sealLane(ctx, l); err != nil {
			return r, err
		}
		if !l.nativeClosed {
			if err := s.closeForStop(ctx, run, id); err != nil {
				s.mu.Lock()
				l.releasing = false
				s.mu.Unlock()
				return r, err
			}
			l.nativeClosed = true
		}
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
	}
	// Retired delivery owners still carry accepted facts. Admission is sealed,
	// so this unique lane set is stable through both pre-release and EOF drains.
	s.mu.Lock()
	retired := make([]*lane, 0, len(run.retired))
	seen := map[*lane]bool{}
	for _, l := range run.retired {
		if !seen[l] && run.lanes[l.session.Status.SessionID] != l {
			retired = append(retired, l)
			seen[l] = true
		}
	}
	s.mu.Unlock()
	for _, l := range retired {
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
	}
	if !run.driverStopped {
		if err := run.driver.Stop(ctx); err != nil {
			return r, err
		}
		run.driverStopped = true
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	s.mu.Lock()
	if run.blocked != nil || run.streamErr != nil {
		s.mu.Unlock()
		return r, &runtime.OperationError{Code: "outcome_unknown", Reason: "runtime_delivery_unsettled", Message: "Original stream delivery remains unsettled after Driver release"}
	}
	s.mu.Unlock()
	for _, l := range retired {
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
	}
	for id, l := range lanes {
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
		if _, err = s.sessionStatus(resource.SessionRef{SessionID: id}, func(r *resource.Session) { offlineStatus(r) }); err != nil {
			return r, err
		}
	}
	r, err = s.runtimeStatus(name, func(status *resource.RuntimeStatus) { *status = resource.RuntimeStatus{State: "stopped"} })
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	for _, l := range lanes {
		close(l.queue)
	}
	for _, l := range retired {
		close(l.queue)
	}
	run.drained = true
	s.registry.Remove(name)
	delete(s.runs, name)
	s.mu.Unlock()
	for _, l := range lanes {
		<-l.done
	}
	for _, l := range retired {
		<-l.done
	}
	return r, nil
}

func (s *Service) resumeSession(ctx context.Context, ref resource.SessionRef, initial *runtime.Input) (resource.SessionResumed, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	ref = resource.SessionRef{SessionID: r.Status.SessionID}
	unlock, err := s.sessionOperation(r.Status.SessionID, false)
	if err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	defer unlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	if err := validateSessionConfig(r.Spec.SessionSpec); err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	switch r.Status.State {
	case "offline", events.Unknown:
	default:
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("previous session release remains unsettled")
	}
	// Read every committed page before touching the adapter or native process.
	if r.Status.HistoryLoad != nil && r.Status.HistoryLoad.Phase != "completed" {
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("unresolved history load cannot be resumed or reimported")
	}
	q := store.HistoryQuery{}
	for {
		page, e := s.history.ListHistory(r.Status.SessionID, q)
		if e != nil {
			return resource.SessionResumed{Session: r}, e
		}
		for _, event := range page.Events {
			if e := event.Validate(); e != nil {
				return resource.SessionResumed{Session: r}, e
			}
			if event.SessionID != r.Status.SessionID || event.Namespace != r.Metadata.Namespace || event.SessionName != r.Metadata.Name || !event.Persistent() {
				return resource.SessionResumed{Session: r}, errors.New("invalid committed history identity")
			}
		}
		if page.Next == "" {
			break
		}
		q.After = page.Next
	}
	if _, err := s.StartRuntime(ctx, r.Spec.RuntimeRef); err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	s.mu.Unlock()
	if run == nil {
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("start original runtime before resume")
	}
	resolvedSpec, err := s.resolveSessionSpec(run, r.Spec.SessionSpec)
	if err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	var resumer runtime.ResumptionDriver
	if initial != nil {
		var ok bool
		resumer, ok = run.driver.(runtime.ResumptionDriver)
		if !ok {
			return resource.SessionResumed{Session: r}, runtime.Unsupported()
		}
		if err := resumer.ValidateInitialInput(*initial); err != nil {
			return resource.SessionResumed{Session: r}, err
		}
	}
	run.admission.RLock()
	defer run.admission.RUnlock()
	s.mu.Lock()
	if s.closing || run.stopping || run.err != nil || !run.started || run.lanes[r.Status.SessionID] != nil {
		s.mu.Unlock()
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("runtime unavailable or session already controlled")
	}
	s.mu.Unlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	switch r.Status.State {
	case "offline", events.Unknown:
	default:
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("previous session release remains unsettled")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	nativeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := &opening{address: a, done: make(chan struct{}), requested: r, restoring: true, buffering: true, cancel: cancel}
	defer op.finish()
	s.mu.Lock()
	if s.closing || run.stopping || run.pending[k] != nil || len(run.pending)+len(run.lanes) >= queueCapacity {
		s.mu.Unlock()
		return resource.SessionResumed{Session: r}, runtime.FailedPrecondition("original restore responsibility remains or capacity exhausted")
	}
	run.pending[k] = op
	s.mu.Unlock()
	if _, err = s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "opening" }); err != nil {
		s.mu.Lock()
		delete(run.pending, k)
		s.mu.Unlock()
		return resource.SessionResumed{Session: r}, err
	}
	var native runtime.DriverSession
	if initial == nil {
		native, err = run.driver.ResumeSession(nativeCtx, r.Status.SessionID, a, resolvedSpec)
	} else {
		native, err = resumer.ResumeSessionWithInput(nativeCtx, r.Status.SessionID, a, resolvedSpec, *initial)
	}
	if err != nil {
		// A failed input dispatch can still return a verified native connection.
		// Retain its source and settle only that original connection's resources.
		if native.SessionID != "" {
			cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
			s.mu.Lock()
			op.native = native
			s.mu.Unlock()
			err = errors.Join(err, cleanup)
			if cleanup == nil {
				err = errors.Join(err, runtime.ErrResourcesReleased)
			}
		}
		s.mu.Lock()
		op.err = err
		op.released = errors.Is(err, runtime.ErrResourcesReleased)
		s.mu.Unlock()
		_, statusErr := s.sessionStatus(ref, func(r *resource.Session) {
			r.Status.State = events.Unknown
			if errors.Is(err, runtime.ErrResourcesReleased) {
				r.Status = op.requested.Status
			}
		})
		var deliveryErr error
		if errors.Is(err, runtime.ErrResourcesReleased) && statusErr == nil {
			deliveryErr = s.settleFailedRestore(ctx, run, op, k)
		}
		return resource.SessionResumed{Session: r}, errors.Join(err, statusErr, deliveryErr)
	}
	s.mu.Lock()
	op.native = native
	s.mu.Unlock()
	identityErr := resource.ValidateSessionID(native.SessionID)
	if identityErr == nil && native.SessionID != r.Status.SessionID {
		identityErr = errors.New("resume returned a replacement identity")
	}
	if identityErr != nil {
		if initial != nil {
			identityErr = errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_input_unverified", Message: "New input may have been submitted without verified original identity; do not resend"}, identityErr)
		}
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		s.mu.Lock()
		op.released = cleanup == nil
		s.mu.Unlock()
		_, statusErr := s.sessionStatus(ref, func(r *resource.Session) {
			r.Status.State = events.Unknown
			if cleanup == nil {
				r.Status = op.requested.Status
			}
		})
		if cleanup != nil {
			return resource.SessionResumed{Session: r}, errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_identity_cleanup_unknown", Message: "Unexpected resume identity cleanup remains unsettled"}, cleanup, statusErr)
		}
		var deliveryErr error
		if statusErr == nil {
			deliveryErr = s.settleFailedRestore(ctx, run, op, k)
		}
		return resource.SessionResumed{Session: r}, errors.Join(identityErr, statusErr, deliveryErr)
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) {
		r.Status = resource.SessionStatus{SessionID: r.Status.SessionID, State: events.Unknown, AppliedGeneration: r.Metadata.Generation, RuntimeGeneration: run.generation, Capabilities: native.Capabilities, EffectiveFeatures: &native.EffectiveFeatures}
	})
	if err != nil {
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		s.mu.Lock()
		op.released = cleanup == nil
		s.mu.Unlock()
		if initial != nil {
			err = errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_input_commit_unknown", Message: "New input was submitted but resume binding commit failed; do not resend"}, err)
		}
		return resource.SessionResumed{Session: r}, errors.Join(err, cleanup)
	}
	s.mu.Lock()
	var l *lane
	for _, previous := range run.retired {
		if previous.session.Status.SessionID == r.Status.SessionID {
			l = previous
			break
		}
	}
	fresh := l == nil
	if fresh {
		l = &lane{session: r, queue: make(chan delivery, laneRetentionCapacity), retry: make(chan chan error), done: make(chan struct{})}
	}
	l.source = native.Source
	l.nativeClosed = false
	l.closing = false
	l.releasing = false
	l.observedState = ""
	l.stateObserved = false
	l.activeTools = nil
	l.interactions = nil
	l.err = errors.Join(l.err, op.err)
	run.lanes[r.Status.SessionID] = l
	s.mu.Unlock()
	if fresh {
		go s.process(run, l)
	}
	s.watchObservation(run, l, native)
	if err := s.flushOpening(ctx, run, l, op); err != nil {
		return resource.SessionResumed{Session: r}, err
	}
	s.mu.Lock()
	delete(run.pending, k)
	resumeErr := l.err
	s.mu.Unlock()
	result := resource.SessionResumed{Session: r}
	if resumeErr == nil {
		result.InitialInputReceipt = native.InitialInputReceipt
	}
	return result, resumeErr
}

// control holds the original Runtime operation lock through a native dispatch.
func (s *Service) controlWork(r resource.Session, canceling bool) (*runBinding, *lane, error) {
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	var original *lane
	if run != nil {
		original = run.lanes[r.Status.SessionID]
	}
	s.mu.Unlock()
	if run == nil {
		return nil, nil, sessionResumeRequired(r.Status.State)
	}
	run.admission.RLock()
	s.mu.Lock()
	closeAccepted := s.sessionCloseAcceptedLocked(r.Status.SessionID)
	l := run.lanes[r.Status.SessionID]
	if closeAccepted {
		s.mu.Unlock()
		run.admission.RUnlock()
		return nil, nil, sessionResumeRequired(r.Status.State)
	}
	if !canceling && l != nil && l.overloaded {
		s.mu.Unlock()
		run.admission.RUnlock()
		return nil, nil, runtime.Exhausted(sessionDeliveryOverloaded)
	}
	state := r.Status.State
	live := l != nil && l == original
	if live && l.stateObserved {
		state = l.observedState
	}
	if !canceling {
		switch state {
		case events.Idle, events.Running, events.RequiresAction:
		case events.Unknown:
			// Creation and resume can return before their initial asynchronous
			// observation. The current live lane and Driver dispatch still prove
			// the connection; a detached unknown has no lane.
			if !live || l.stateObserved {
				s.mu.Unlock()
				run.admission.RUnlock()
				return nil, nil, sessionResumeRequired(state)
			}
		default:
			s.mu.Unlock()
			run.admission.RUnlock()
			return nil, nil, sessionResumeRequired(state)
		}
	}
	pending := run.pending[addressKey(runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name})]
	available := run.started && live && !l.releasing && pending == nil
	if l != nil && l.load != nil && !l.load.complete.Load() {
		available = false
	}
	if !canceling {
		available = available && !s.closing && !run.stopping && run.err == nil && l.err == nil && !l.closing
	}
	if available {
		l.admission.RLock()
		s.invalidateIdleLocked(l)
	}
	s.mu.Unlock()
	if !available {
		run.admission.RUnlock()
		return nil, nil, sessionResumeRequired(r.Status.State)
	}
	return run, l, nil
}

type sessionOperation struct {
	sync.Mutex
	users   int
	closing bool
}

var errSessionOperationGuardRejected = errors.New("session operation guard rejected")

// Caller holds s.mu, which makes close acceptance atomic with work admission.
func (s *Service) sessionCloseAcceptedLocked(id string) bool {
	op := s.sessionOps[id]
	return op != nil && op.closing
}

func (s *Service) sessionOperation(id string, closing bool) (func(), error) {
	return s.sessionOperationGuarded(id, closing, nil)
}

func (s *Service) sessionOperationGuarded(id string, closing bool, guard func() bool) (func(), error) {
	s.mu.Lock()
	op := s.sessionOps[id]
	if op == nil {
		op = &sessionOperation{}
		s.sessionOps[id] = op
	}
	op.users++
	s.mu.Unlock()
	op.Lock()
	s.mu.Lock()
	if !closing && op.closing {
		op.users--
		if op.users == 0 {
			delete(s.sessionOps, id)
		}
		s.mu.Unlock()
		op.Unlock()
		return nil, runtime.FailedPrecondition("session close already accepted")
	}
	if closing && guard != nil && (op.closing || op.users != 1 || !guard()) {
		op.users--
		if op.users == 0 {
			delete(s.sessionOps, id)
		}
		s.mu.Unlock()
		op.Unlock()
		return nil, errSessionOperationGuardRejected
	}
	if closing {
		op.closing = true
	}
	s.mu.Unlock()
	return func() {
		op.Unlock()
		s.mu.Lock()
		op.users--
		if op.users == 0 {
			delete(s.sessionOps, id)
		}
		s.mu.Unlock()
	}, nil
}

func (s *Service) closeForStop(ctx context.Context, run *runBinding, id string) error {
	// Manual close hands active work directly to SDK close. Stop additionally
	// cancels and waits for work to settle so final facts precede release.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt, err := run.driver.Cancel(ctx, id)
		if err != nil && !errors.Is(err, runtime.ErrFailedPrecondition) {
			return errors.Join(err, run.driver.CloseSession(ctx, id))
		}
		if err != nil || receipt.Result == "noActiveWork" {
			return run.driver.CloseSession(ctx, id)
		}
		select {
		case <-ctx.Done():
			return fmtUnknown(ctx.Err())
		case <-ticker.C:
		}
	}
}

func fmtUnknown(err error) error {
	return errors.Join(errors.New("outcome_unknown: drain incomplete"), err)
}

// Caller cancellation abandons only the wait. A bounded original restore owner
// keeps settling its SDK operation; no replacement resume is dispatched.
func (s *Service) ResumeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	result, err := s.resumeSessionOwned(ctx, ref, nil)
	return result.Session, err
}

// ResumeSessionWithInput snapshots new caller input; it is never recovery config.
func (s *Service) ResumeSessionWithInput(ctx context.Context, ref resource.SessionRef, input runtime.Input) (resource.SessionResumed, error) {
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return resource.SessionResumed{}, err
	}
	return s.resumeSessionOwned(ctx, ref, &copied)
}

func (s *Service) resumeSessionOwned(ctx context.Context, ref resource.SessionRef, initial *runtime.Input) (resource.SessionResumed, error) {
	if err := ctx.Err(); err != nil {
		return resource.SessionResumed{}, err
	}
	select {
	case s.resumeSlots <- struct{}{}:
	default:
		return resource.SessionResumed{}, runtime.FailedPrecondition("resume capacity exhausted")
	}
	return awaitSessionLifecycle(ctx, func(owner context.Context) (resource.SessionResumed, error) {
		defer func() { <-s.resumeSlots }()
		return s.resumeSession(owner, ref, initial)
	})
}

func lockOperation(ctx context.Context, op *sync.Mutex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if op.TryLock() {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmtUnknown(ctx.Err())
		case <-ticker.C:
			if op.TryLock() {
				return nil
			}
		}
	}
}

func waitAdmission(ctx context.Context, gate *sync.RWMutex) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if gate.TryLock() {
			gate.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmtUnknown(ctx.Err())
		case <-ticker.C:
		}
	}
}

// Closing seals inputs/answers first. Cancels admitted during drain still belong
// to this lane; seal their admission atomically once all dispatches have settled.
func (s *Service) sealLane(ctx context.Context, l *lane) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		ready := l.admission.TryLock()
		if ready {
			l.releasing = true
			l.admission.Unlock()
		}
		s.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmtUnknown(ctx.Err())
		case <-ticker.C:
		}
	}
}

// Accepted lifecycle operations retain their original owner after the caller
// stops waiting. A late result remains observable through the persistent resource.
func awaitSessionLifecycle[T any](ctx context.Context, operation func(context.Context) (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() { r, err := operation(context.WithoutCancel(ctx)); done <- result{r, err} }()
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		select {
		case r := <-done:
			return r.value, r.err
		default:
		}
		var zero T
		return zero, &runtime.OperationError{Code: "outcome_unknown", Reason: "lifecycle_wait_abandoned", Message: "Lifecycle operation remains owned by its original Session; inspect state before further action"}
	}
}

// Keep the original delivery owner until its Driver stream ends. Close confirms
// native release, but facts already in the shared stream may still arrive here.
// Caller holds s.mu; state projection is sealed by l.closing.
func retireLane(run *runBinding, l *lane) {
	stopIdleTimerLocked(l)
	if run.retired == nil {
		run.retired = map[events.Source]*lane{}
	}
	run.retired[l.source.WithWork("")] = l
	delete(run.lanes, l.session.Status.SessionID)
}
