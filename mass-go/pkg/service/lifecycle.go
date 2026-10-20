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
func (s *Service) runtimeStatus(name string, status resource.RuntimeStatus) (resource.Runtime, error) {
	for {
		r, err := s.meta.GetRuntime(name)
		if err != nil {
			return r, err
		}
		r.Status = status
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
	switch q.State {
	case "", "offline", "opening", "idle", "running", "requires_action", "closing", "archived", "unknown":
	default:
		return resource.SessionPage{}, runtime.InvalidArgument("invalid session state")
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
	b := make(chan struct{})
	select {
	case l.queue <- delivery{barrier: b}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return l.commitErr
}

func offlineStatus(r *resource.Session, archived bool) {
	r.Status.State = events.Unknown
	if archived {
		r.Status.State = "archived"
	}
	r.Status.AppliedGeneration = 0
	r.Status.RuntimeGeneration = 0
	r.Status.Capabilities = nil
}

func (s *Service) archiveSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return r, err
	}
	ref = resource.SessionRef{SessionID: r.Status.SessionID}
	unlock := s.sessionOperation(r.Status.SessionID)
	defer unlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	s.mu.Unlock()
	if run == nil {
		if r.Status.State == "archived" {
			return r, nil
		}
		if r.Status.AppliedGeneration != 0 {
			return r, runtime.FailedPrecondition("session release not confirmed in this process")
		}
		return s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	}
	run.admission.RLock()
	defer run.admission.RUnlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	if s.runs[r.Spec.RuntimeRef] != run || run.stopping || run.pending[addressKey(runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name})] != nil {
		s.mu.Unlock()
		return r, runtime.FailedPrecondition("original lifecycle responsibility remains")
	}
	l := run.lanes[r.Status.SessionID]
	s.mu.Unlock()
	if l == nil {
		if r.Status.AppliedGeneration != 0 {
			return r, runtime.FailedPrecondition("session release not confirmed")
		}
		return s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	}
	s.mu.Lock()
	l.closing = true
	s.mu.Unlock()
	if _, err = s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
		return r, err
	}
	if err := s.sealLane(ctx, l); err != nil {
		return r, err
	}
	if err := run.driver.CloseSession(ctx, r.Status.SessionID); err != nil {
		s.mu.Lock()
		l.releasing = false // Explicit cancel can still settle the original work.
		s.mu.Unlock()
		return r, err
	}
	if err := s.drainLane(ctx, l); err != nil {
		return r, err
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	if err == nil {
		s.mu.Lock()
		retireLane(run, l)
		if len(l.subscribers) == 0 {
			delete(s.observers, r.Status.SessionID)
		}
		s.mu.Unlock()
	}
	return r, err
}

func (s *Service) StopRuntime(ctx context.Context, name string) (resource.Runtime, error) {
	r, err := s.meta.GetRuntime(name)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	run := s.runs[name]
	first := run != nil && !run.stopping
	if first {
		run.stopping = true
		run.stopStarted = make(chan struct{})
	}
	s.mu.Unlock()
	if run == nil {
		if r.Status.State == "stopped" {
			return r, nil
		}
		return r, runtime.FailedPrecondition("runtime release not confirmed in this process")
	}
	if first {
		_, statusErr := s.runtimeStatus(name, resource.RuntimeStatus{State: "stopping", AppliedGeneration: r.Status.AppliedGeneration, Capabilities: r.Status.Capabilities})
		s.mu.Lock()
		run.stopStatusErr = statusErr
		close(run.stopStarted)
		s.mu.Unlock()
	}
	select {
	case <-run.stopStarted:
	case <-ctx.Done():
		return r, fmtUnknown(ctx.Err())
	}
	s.mu.Lock()
	statusErr := run.stopStatusErr
	s.mu.Unlock()
	if first && statusErr != nil {
		return r, statusErr
	}
	if err := lockOperation(ctx, &run.op); err != nil {
		return r, err
	}
	defer run.op.Unlock()
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
	if _, err = s.runtimeStatus(name, resource.RuntimeStatus{State: "stopping", AppliedGeneration: r.Status.AppliedGeneration, Capabilities: r.Status.Capabilities}); err != nil {
		return r, err
	}
	s.mu.Lock()
	pending := make([]*opening, 0, len(run.pending))
	for _, op := range run.pending {
		pending = append(pending, op)
	}
	s.mu.Unlock()
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
		lanes[id] = l
	}
	s.mu.Unlock()
	for id, l := range lanes {
		if _, err = s.sessionStatus(resource.SessionRef{SessionID: id}, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
			return r, err
		}
		if err := s.closeForStop(ctx, run, id); err != nil {
			return r, err
		}
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
	}
	if err := run.driver.Stop(ctx); err != nil {
		return r, err
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return r, ctx.Err()
	}
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
	for id, l := range lanes {
		if err := s.drainLane(ctx, l); err != nil {
			return r, err
		}
		if _, err = s.sessionStatus(resource.SessionRef{SessionID: id}, func(r *resource.Session) { offlineStatus(r, false) }); err != nil {
			return r, err
		}
	}
	r, err = s.runtimeStatus(name, resource.RuntimeStatus{State: "stopped"})
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	for id, l := range lanes {
		if len(l.subscribers) == 0 {
			delete(s.observers, id)
		}
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

func (s *Service) resumeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return r, err
	}
	ref = resource.SessionRef{SessionID: r.Status.SessionID}
	unlock := s.sessionOperation(r.Status.SessionID)
	defer unlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return r, err
	}
	// Read every committed page before touching the adapter or native process.
	q := store.HistoryQuery{}
	for {
		page, e := s.history.ListHistory(r.Status.SessionID, q)
		if e != nil {
			return r, e
		}
		for _, event := range page.Events {
			if e := event.Validate(); e != nil {
				return r, e
			}
			if event.SessionID != r.Status.SessionID || event.Namespace != r.Metadata.Namespace || event.SessionName != r.Metadata.Name || !event.Persistent() {
				return r, errors.New("invalid committed history identity")
			}
		}
		if page.Next == "" {
			break
		}
		q.After = page.Next
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	s.mu.Unlock()
	if run == nil {
		return r, runtime.FailedPrecondition("start original runtime before resume")
	}
	run.admission.RLock()
	defer run.admission.RUnlock()
	s.mu.Lock()
	if s.closing || run.stopping || run.err != nil || !run.started || run.lanes[r.Status.SessionID] != nil {
		s.mu.Unlock()
		return r, runtime.FailedPrecondition("runtime unavailable or session already controlled")
	}
	s.mu.Unlock()
	r, err = s.GetSession(ref)
	if err != nil {
		return r, err
	}
	if r.Status.State == "closing" || r.Status.AppliedGeneration != 0 {
		return r, runtime.FailedPrecondition("previous session release remains unsettled")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	op := &opening{address: a, done: make(chan struct{}), requested: r, restoring: true, buffering: true}
	defer close(op.done)
	s.mu.Lock()
	if run.pending[k] != nil || len(run.pending)+len(run.lanes) >= queueCapacity {
		s.mu.Unlock()
		return r, runtime.FailedPrecondition("original restore responsibility remains or capacity exhausted")
	}
	run.pending[k] = op
	s.mu.Unlock()
	if _, err = s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "opening" }); err != nil {
		s.mu.Lock()
		delete(run.pending, k)
		s.mu.Unlock()
		return r, err
	}
	native, err := run.driver.ResumeSession(ctx, r.Status.SessionID, a, r.Spec.SessionSpec)
	if err != nil {
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
		return r, errors.Join(err, statusErr, deliveryErr)
	}
	s.mu.Lock()
	op.native = native
	s.mu.Unlock()
	identityErr := resource.ValidateSessionID(native.SessionID)
	if identityErr == nil && native.SessionID != r.Status.SessionID {
		identityErr = errors.New("resume returned a replacement identity")
	}
	if identityErr != nil {
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
			return r, errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_identity_cleanup_unknown", Message: "Unexpected resume identity cleanup remains unsettled"}, cleanup, statusErr)
		}
		var deliveryErr error
		if statusErr == nil {
			deliveryErr = s.settleFailedRestore(ctx, run, op, k)
		}
		return r, errors.Join(identityErr, statusErr, deliveryErr)
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) {
		r.Status = resource.SessionStatus{SessionID: r.Status.SessionID, State: events.Unknown, AppliedGeneration: r.Metadata.Generation, RuntimeGeneration: run.generation, Capabilities: native.Capabilities}
	})
	if err != nil {
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		s.mu.Lock()
		op.released = cleanup == nil
		s.mu.Unlock()
		return r, errors.Join(err, cleanup)
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
		l = &lane{session: r, queue: make(chan delivery, queueCapacity), retry: make(chan chan error), done: make(chan struct{}), subscribers: map[*Subscription]bool{}}
	}
	l.source = native.Source
	l.closing = false
	l.releasing = false
	l.err = errors.Join(l.err, op.err)
	if subs := s.observers[r.Status.SessionID]; subs != nil {
		l.subscribers = subs
	} else {
		s.observers[r.Status.SessionID] = l.subscribers
	}
	run.lanes[r.Status.SessionID] = l
	s.mu.Unlock()
	if fresh {
		go s.process(run, l)
	}
	if err := s.flushOpening(ctx, run, l, op); err != nil {
		return r, err
	}
	s.mu.Lock()
	delete(run.pending, k)
	resumeErr := l.err
	s.mu.Unlock()
	return r, resumeErr
}

// control holds the original Runtime operation lock through a native dispatch.
func (s *Service) controlWork(r resource.Session, cancelling bool) (*runBinding, *lane, error) {
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	var original *lane
	if run != nil {
		original = run.lanes[r.Status.SessionID]
	}
	s.mu.Unlock()
	if run == nil {
		return nil, nil, runtime.FailedPrecondition("session unavailable")
	}
	run.admission.RLock()
	s.mu.Lock()
	l := run.lanes[r.Status.SessionID]
	available := run.started && l != nil && l == original && !l.releasing
	if !cancelling {
		available = available && !s.closing && !run.stopping && run.err == nil && l.err == nil && !l.closing
	}
	if available {
		l.admission.RLock()
	}
	s.mu.Unlock()
	if !available {
		run.admission.RUnlock()
		return nil, nil, runtime.FailedPrecondition("session unavailable")
	}
	return run, l, nil
}

type sessionOperation struct {
	sync.Mutex
	users int
}

func (s *Service) sessionOperation(id string) func() {
	s.mu.Lock()
	op := s.sessionOps[id]
	if op == nil {
		op = &sessionOperation{}
		s.sessionOps[id] = op
	}
	op.users++
	s.mu.Unlock()
	op.Lock()
	return func() {
		op.Unlock()
		s.mu.Lock()
		op.users--
		if op.users == 0 {
			delete(s.sessionOps, id)
		}
		s.mu.Unlock()
	}
}

func (s *Service) closeForStop(ctx context.Context, run *runBinding, id string) error {
	err := run.driver.CloseSession(ctx, id)
	if !errors.Is(err, runtime.ErrWorkUnsettled) {
		return err
	}
	if _, err = run.driver.Cancel(ctx, id); err != nil {
		return err
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmtUnknown(ctx.Err())
		case <-ticker.C:
		}
		err = run.driver.CloseSession(ctx, id)
		if !errors.Is(err, runtime.ErrWorkUnsettled) {
			return err
		}
	}
}

func fmtUnknown(err error) error {
	return errors.Join(errors.New("outcome_unknown: drain incomplete"), err)
}

// Caller cancellation abandons only the wait. A bounded original restore owner
// keeps settling its SDK operation; no replacement resume is dispatched.
func (s *Service) ResumeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	if err := ctx.Err(); err != nil {
		return resource.Session{}, err
	}
	select {
	case s.resumeSlots <- struct{}{}:
	default:
		return resource.Session{}, runtime.FailedPrecondition("resume capacity exhausted")
	}
	return awaitSessionLifecycle(ctx, func(owner context.Context) (resource.Session, error) {
		defer func() { <-s.resumeSlots }()
		return s.resumeSession(owner, ref)
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
func awaitSessionLifecycle(ctx context.Context, operation func(context.Context) (resource.Session, error)) (resource.Session, error) {
	type result struct {
		value resource.Session
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
		return resource.Session{}, &runtime.OperationError{Code: "outcome_unknown", Reason: "lifecycle_wait_abandoned", Message: "Lifecycle operation remains owned by its original Session; inspect state before further action"}
	}
}
func (s *Service) ArchiveSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	if err := ctx.Err(); err != nil {
		return resource.Session{}, err
	}
	return awaitSessionLifecycle(ctx, func(owner context.Context) (resource.Session, error) { return s.archiveSession(owner, ref) })
}

// Keep the original delivery owner until its Driver stream ends. Close confirms
// native release, but facts already in the shared stream may still arrive here.
// Caller holds s.mu; state projection is sealed by l.closing.
func retireLane(run *runBinding, l *lane) {
	if run.retired == nil {
		run.retired = map[events.Source]*lane{}
	}
	run.retired[l.source.WithWork("")] = l
	delete(run.lanes, l.session.Status.SessionID)
}
