package service

import (
	"context"
	"errors"
	"sync"

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
		return errors.New("runtime still owns running or unsettled resources")
	}
	page, err := s.meta.ListSessions(resource.SessionQuery{RuntimeRef: name, Limit: 1})
	if err != nil {
		return err
	}
	if len(page.Items) != 0 {
		return errors.New("runtime has persistent session references")
	}
	r, err := s.meta.GetRuntime(name)
	if err != nil {
		return err
	}
	// A cold process cannot infer release from an old running status.
	if r.Status.State != "stopped" {
		return errors.New("runtime release not confirmed")
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
	return l.err
}

func offlineStatus(r *resource.Session, archived bool) {
	r.Status.State = runtime.Unknown
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
			return r, errors.New("session release not confirmed in this process")
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
			return r, errors.New("session release not confirmed")
		}
		return s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	}
	s.mu.Lock()
	l.closing = true
	s.mu.Unlock()
	if _, err = s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
		return r, err
	}
	if err = run.driver.CloseSession(ctx, r.Status.SessionID); err != nil {
		return r, err
	}
	if err = s.drainLane(ctx, l); err != nil {
		return r, err
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) })
	if err == nil {
		s.mu.Lock()
		delete(run.lanes, r.Status.SessionID)
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
	if run != nil {
		run.stopping = true
	}
	s.mu.Unlock()
	if run == nil {
		if r.Status.State == "stopped" {
			return r, nil
		}
		return r, errors.New("runtime release not confirmed in this process")
	}
	run.op.Lock()
	defer run.op.Unlock()
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
	lanes := make(map[string]*lane, len(run.lanes))
	for id, l := range run.lanes {
		l.closing = true
		lanes[id] = l
	}
	s.mu.Unlock()
	for id, l := range lanes {
		if err = run.driver.CloseSession(ctx, id); err != nil {
			return r, err
		}
		if err = s.drainLane(ctx, l); err != nil {
			return r, err
		}
	}
	if err = run.driver.Stop(ctx); err != nil {
		return r, err
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return r, ctx.Err()
	}
	for id, l := range lanes {
		if err = s.drainLane(ctx, l); err != nil {
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
	for _, l := range lanes {
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

func (s *Service) ResumeSession(ctx context.Context, ref resource.SessionRef) (resource.Session, error) {
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
			if e = event.Validate(); e != nil {
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
		return r, errors.New("start original runtime before resume")
	}
	run.op.Lock()
	defer run.op.Unlock()
	s.mu.Lock()
	if s.closing || run.stopping || run.err != nil || !run.started || run.lanes[r.Status.SessionID] != nil {
		s.mu.Unlock()
		return r, errors.New("runtime unavailable or session already controlled")
	}
	s.mu.Unlock()
	r, err = s.meta.GetSession(ref)
	if err != nil {
		return r, err
	}
	if r.Status.State == "closing" {
		return r, errors.New("session close remains unsettled")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	op := &opening{address: a}
	s.mu.Lock()
	run.pending[k] = op
	s.mu.Unlock()
	native, err := run.driver.ResumeSession(ctx, r.Status.SessionID, a, r.Spec.SessionSpec)
	if err != nil {
		s.mu.Lock()
		delete(run.pending, k)
		s.mu.Unlock()
		return r, err
	}
	if native.SessionID != r.Status.SessionID {
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		s.mu.Lock()
		delete(run.pending, k)
		s.mu.Unlock()
		return r, errors.Join(errors.New("resume returned a replacement identity"), cleanup)
	}
	r, err = s.sessionStatus(ref, func(r *resource.Session) {
		r.Status = resource.SessionStatus{SessionID: r.Status.SessionID, State: runtime.Unknown, AppliedGeneration: r.Metadata.Generation, RuntimeGeneration: run.generation, Capabilities: native.Capabilities}
	})
	if err != nil {
		return r, errors.Join(err, run.driver.CloseSession(context.Background(), native.SessionID))
	}
	l := &lane{session: r, source: native.Source, queue: make(chan delivery, queueCapacity), done: make(chan struct{}), subscribers: map[*Subscription]bool{}}
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
func (s *Service) control(r resource.Session) (*runBinding, *lane, error) {
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	s.mu.Unlock()
	if run == nil {
		return nil, nil, errors.New("session unavailable")
	}
	run.op.Lock()
	s.mu.Lock()
	l := run.lanes[r.Status.SessionID]
	available := !s.closing && !run.stopping && run.err == nil && run.started && l != nil && l.err == nil && !l.closing
	s.mu.Unlock()
	if !available {
		run.op.Unlock()
		return nil, nil, errors.New("session unavailable")
	}
	return run, l, nil
}

func (s *Service) sessionOperation(id string) func() {
	s.mu.Lock()
	op := s.sessionOps[id]
	if op == nil {
		op = &sync.Mutex{}
		s.sessionOps[id] = op
	}
	s.mu.Unlock()
	op.Lock()
	return op.Unlock
}
