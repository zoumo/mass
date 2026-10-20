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
		r, err := s.meta.GetSession(ref)
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

func (s *Service) UpdateRuntime(name string, spec resource.RuntimeSpec) (resource.Runtime, error) {
	if err := s.registry.ValidateConfig(spec.Type, spec.Config); err != nil {
		return resource.Runtime{}, err
	}
	for {
		r, err := s.meta.GetRuntime(name)
		if err != nil {
			return r, err
		}
		r.Spec = spec
		r, err = s.meta.UpdateRuntime(r)
		if !errors.Is(err, store.ErrConflict) {
			return r, err
		}
	}
}

func (s *Service) ListRuntimes(q resource.RuntimeQuery) ([]resource.Runtime, error) {
	return s.meta.ListRuntimes(q)
}

func (s *Service) ListSessions(q resource.SessionQuery) (resource.SessionPage, error) {
	return s.meta.ListSessions(q)
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

func (s *Service) ArchiveSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return r, err
	}
	unlock := s.sessionOperation(r.Status.SessionID)
	defer unlock()
	r, err = s.meta.GetSession(ref)
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
	run.op.Lock()
	defer run.op.Unlock()
	r, err = s.meta.GetSession(ref)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
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
	if err := waitAdmission(ctx, &run.admission); err != nil {
		return r, err
	}
	if _, err = s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
		return r, err
	}
	if err := run.driver.CloseSession(ctx, r.Status.SessionID); err != nil {
		return r, err
	}
	if err := s.drainLane(ctx, l); err != nil {
		return r, err
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	if err == nil {
		s.mu.Lock()
		delete(run.lanes, r.Status.SessionID)
		if len(l.subscribers) == 0 {
			delete(s.observers, r.Status.SessionID)
		}
		close(l.queue)
		s.mu.Unlock()
		<-l.done
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
	run.drained = true
	s.registry.Remove(name)
	delete(s.runs, name)
	s.mu.Unlock()
	for _, l := range lanes {
		<-l.done
	}
	return r, nil
}

func (s *Service) resumeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return r, err
	}
	unlock := s.sessionOperation(r.Status.SessionID)
	defer unlock()
	r, err = s.meta.GetSession(ref)
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
	run.op.Lock()
	defer run.op.Unlock()
	s.mu.Lock()
	if s.closing || run.stopping || run.err != nil || !run.started || run.lanes[r.Status.SessionID] != nil {
		s.mu.Unlock()
		return r, runtime.FailedPrecondition("runtime unavailable or session already controlled")
	}
	s.mu.Unlock()
	r, err = s.meta.GetSession(ref)
	if err != nil {
		return r, err
	}
	if r.Status.State == "closing" || r.Status.AppliedGeneration != 0 {
		return r, runtime.FailedPrecondition("previous session release remains unsettled")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	op := &opening{address: a, done: make(chan struct{})}
	defer close(op.done)
	s.mu.Lock()
	if run.pending[k] != nil || len(run.pending)+len(run.lanes) >= queueCapacity {
		s.mu.Unlock()
		return r, runtime.FailedPrecondition("original restore responsibility remains or capacity exhausted")
	}
	run.pending[k] = op
	s.mu.Unlock()
	native, err := run.driver.ResumeSession(ctx, r.Status.SessionID, a, r.Spec.SessionSpec)
	if err != nil {
		s.mu.Lock()
		op.err = err
		if errors.Is(err, runtime.ErrResourcesReleased) {
			delete(run.pending, k)
		}
		s.mu.Unlock()
		return r, err
	}
	if native.SessionID != r.Status.SessionID {
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		s.mu.Lock()
		if cleanup == nil {
			delete(run.pending, k)
		}
		s.mu.Unlock()
		return r, errors.Join(errors.New("resume returned a replacement identity"), cleanup)
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) {
		r.Status = resource.SessionStatus{SessionID: r.Status.SessionID, State: events.Unknown, AppliedGeneration: r.Metadata.Generation, RuntimeGeneration: run.generation, Capabilities: native.Capabilities}
	})
	if err != nil {
		return r, errors.Join(err, run.driver.CloseSession(context.Background(), native.SessionID))
	}
	l := &lane{session: r, source: native.Source, queue: make(chan delivery, queueCapacity), retry: make(chan chan error), done: make(chan struct{}), subscribers: map[*Subscription]bool{}}
	s.mu.Lock()
	l.err = op.err
	if subs := s.observers[r.Status.SessionID]; subs != nil {
		l.subscribers = subs
	} else {
		s.observers[r.Status.SessionID] = l.subscribers
	}
	for _, e := range op.events {
		if !native.Source.SameRun(e.Source()) {
			l.err = errors.New("resume event source mismatch")
			break
		}
		l.queue <- delivery{event: e}
	}
	run.lanes[r.Status.SessionID] = l
	delete(run.pending, k)
	resumeErr := l.err
	s.mu.Unlock()
	go s.process(run, l)
	return r, resumeErr
}

// control holds the original Runtime operation lock through a native dispatch.
func (s *Service) control(r resource.Session) (*runBinding, error) {
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	var original *lane
	if run != nil {
		original = run.lanes[r.Status.SessionID]
	}
	s.mu.Unlock()
	if run == nil {
		return nil, runtime.FailedPrecondition("session unavailable")
	}
	run.admission.RLock()
	s.mu.Lock()
	l := run.lanes[r.Status.SessionID]
	available := !s.closing && !run.stopping && run.err == nil && run.started && l != nil && l == original && l.err == nil && !l.closing
	s.mu.Unlock()
	if !available {
		run.admission.RUnlock()
		return nil, runtime.FailedPrecondition("session unavailable")
	}
	return run, nil
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
	type result struct {
		value resource.Session
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-s.resumeSlots }()
		value, err := s.resumeSession(context.WithoutCancel(ctx), ref)
		done <- result{value, err}
	}()
	select {
	case value := <-done:
		return value.value, value.err
	case <-ctx.Done():
		return resource.Session{}, fmtUnknown(ctx.Err())
	}
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
