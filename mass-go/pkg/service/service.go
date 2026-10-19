// Package service owns persistent resource orchestration and commit-before-publish.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

const queueCapacity = 128

// RuntimeService and SessionService share an owner, without transport objects.
type RuntimeService interface {
	CreateRuntime(resource.Runtime) (resource.Runtime, error)
	GetRuntime(string) (resource.Runtime, error)
	StartRuntime(context.Context, string) (resource.Runtime, error)
}
type SessionService interface {
	CreateSession(context.Context, resource.Session) (resource.Session, error)
	GetSession(resource.SessionRef) (resource.Session, error)
	SendPrompt(context.Context, resource.SessionRef, runtime.Input) (runtime.InputReceipt, error)
	AnswerInteraction(context.Context, resource.SessionRef, string, map[string]any) (runtime.Acceptance, error)
	ListHistoryEvents(resource.SessionRef, store.HistoryQuery) (store.HistoryPage, error)
	SubscribeEvents(resource.SessionRef) (*Subscription, error)
}
type runBinding struct {
	driver     runtime.Driver
	generation uint64
	done       chan struct{}
	err        error
	pending    map[string]*opening
	lanes      map[string]*lane
	op         sync.Mutex
	started    bool
	drained    bool
}
type opening struct {
	address runtime.Address
	events  []runtime.SessionEvent
	err     error
}
type lane struct {
	session     resource.Session
	source      runtime.Source
	queue       chan runtime.SessionEvent
	done        chan struct{}
	err         error
	work        string
	subscribers map[*Subscription]bool
}
type InteractionPolicy func(runtime.InteractionRequested) (map[string]any, bool)

type Service struct {
	policy   InteractionPolicy
	meta     store.MetaStore
	history  store.HistoryStore
	registry *runtime.Registry
	mu       sync.Mutex
	runs     map[string]*runBinding
	closing  bool
}

func New(meta store.MetaStore, history store.HistoryStore, registry *runtime.Registry) *Service {
	return &Service{meta: meta, history: history, registry: registry, runs: map[string]*runBinding{}}
}

// NewWithPolicy explicitly enables an application-owned interaction policy.
func NewWithPolicy(meta store.MetaStore, history store.HistoryStore, registry *runtime.Registry, policy InteractionPolicy) *Service {
	s := New(meta, history, registry)
	s.policy = policy
	return s
}
func (s *Service) AnswerInteraction(ctx context.Context, ref resource.SessionRef, interaction string, answer map[string]any) (runtime.Acceptance, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	if s.closing || run == nil || run.err != nil {
		s.mu.Unlock()
		return runtime.Acceptance{}, errors.New("runtime unavailable")
	}
	l := run.lanes[r.Status.SessionID]
	if l == nil || l.err != nil {
		s.mu.Unlock()
		return runtime.Acceptance{}, errors.New("session unavailable")
	}
	d := run.driver
	s.mu.Unlock()
	return d.AnswerInteraction(ctx, r.Status.SessionID, interaction, answer)
}

var label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, p := range strings.Split(name, ".") {
		if len(p) > 63 || !label.MatchString(p) {
			return false
		}
	}
	return true
}
func validateMetadata(m resource.Metadata, session bool) error {
	if !validName(m.Name) {
		return errors.New("invalid DNS resource name")
	}
	if session {
		if len(m.Namespace) > 63 || !label.MatchString(m.Namespace) {
			return errors.New("invalid DNS namespace")
		}
	} else if m.Namespace != "" {
		return errors.New("runtime namespace must be empty")
	}
	return nil
}
func addressKey(a runtime.Address) string { return a.Namespace + "/" + a.SessionName }
func (s *Service) CreateRuntime(r resource.Runtime) (resource.Runtime, error) {
	if err := validateMetadata(r.Metadata, false); err != nil {
		return r, err
	}
	if err := s.registry.ValidateConfig(r.Spec.Type, r.Spec.Config); err != nil {
		return r, err
	}
	r.Status = resource.RuntimeStatus{State: "stopped"}
	return s.meta.CreateRuntime(r)
}
func (s *Service) GetRuntime(name string) (resource.Runtime, error) { return s.meta.GetRuntime(name) }
func (s *Service) StartRuntime(ctx context.Context, name string) (resource.Runtime, error) {
	r, err := s.meta.GetRuntime(name)
	if err != nil {
		return r, err
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return r, errors.New("service closing")
	}
	if existing := s.runs[name]; existing != nil {
		s.mu.Unlock()
		existing.op.Lock()
		defer existing.op.Unlock()
		s.mu.Lock()
		err := existing.err
		started := existing.started
		s.mu.Unlock()
		if err != nil {
			return r, err
		}
		if !started {
			return r, errors.New("runtime start not confirmed")
		}
		return s.meta.GetRuntime(name)
	}
	// Reserve the original owner before constructing or starting any Driver.
	run := &runBinding{done: make(chan struct{}), pending: map[string]*opening{}, lanes: map[string]*lane{}}
	run.op.Lock()
	s.runs[name] = run
	s.mu.Unlock()
	defer run.op.Unlock()
	d, err := s.registry.Create(r.Spec.Type, name, r.Spec.Config)
	if err != nil {
		s.mu.Lock()
		run.err = err
		s.mu.Unlock()
		close(run.done)
		return r, err
	}
	run.driver = d
	stream, err := d.Events()
	if err != nil {
		s.mu.Lock()
		run.err = err
		s.mu.Unlock()
		close(run.done)
		return r, err
	}
	go s.consume(run, stream)
	capabilities, err := d.Start(ctx)
	if err != nil {
		s.mu.Lock()
		run.err = err
		s.mu.Unlock()
		return r, err
	}
	r.Status = resource.RuntimeStatus{State: "running", AppliedGeneration: r.Metadata.Generation, Capabilities: capabilities}
	r, err = s.meta.UpdateRuntime(r)
	s.mu.Lock()
	if err != nil {
		run.err = err
	} else {
		run.generation = r.Status.AppliedGeneration
		run.started = true
	}
	s.mu.Unlock()
	return r, err
}
func (s *Service) CreateSession(ctx context.Context, r resource.Session) (resource.Session, error) {
	if err := validateMetadata(r.Metadata, true); err != nil {
		return r, err
	}
	if !filepath.IsAbs(r.Spec.Cwd) {
		return r, errors.New("absolute cwd required")
	}
	if r.Metadata.UID != "" || r.Metadata.Generation != 0 || r.Metadata.ResourceVersion != "" || !r.Metadata.CreatedAt.IsZero() {
		return r, errors.New("system metadata is read-only")
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	closing := s.closing
	s.mu.Unlock()
	if run == nil || closing {
		return r, errors.New("runtime not started or service closing")
	}
	run.op.Lock()
	defer run.op.Unlock()
	s.mu.Lock()
	if s.closing || run.err != nil || !run.started {
		s.mu.Unlock()
		return r, errors.New("runtime unavailable")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	if run.pending[k] != nil {
		s.mu.Unlock()
		return r, store.ErrConflict
	}
	op := &opening{address: a}
	run.pending[k] = op
	s.mu.Unlock()
	removePending := func() { s.mu.Lock(); delete(run.pending, k); s.mu.Unlock() }
	if _, err := s.meta.GetSession(resource.SessionRef{Namespace: a.Namespace, Name: a.SessionName}); !errors.Is(err, store.ErrNotFound) {
		removePending()
		if err == nil {
			err = store.ErrConflict
		}
		return r, err
	}
	native, err := run.driver.OpenSession(ctx, a, r.Spec.SessionSpec)
	if err != nil {
		removePending()
		return r, err
	}
	r.Status = resource.SessionStatus{SessionID: native.SessionID, State: runtime.Unknown, AppliedGeneration: 1, RuntimeGeneration: run.generation, Capabilities: native.Capabilities}
	r, err = s.meta.CreateSession(r)
	if err != nil {
		cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
		removePending()
		return resource.Session{}, errors.Join(err, cleanup)
	}
	l := &lane{session: r, source: native.Source, queue: make(chan runtime.SessionEvent, queueCapacity), done: make(chan struct{}), subscribers: map[*Subscription]bool{}}
	s.mu.Lock()
	l.err = op.err
	run.lanes[native.SessionID] = l
	for _, e := range op.events {
		if !native.Source.SameRun(e.Source()) {
			l.err = errors.New("creation event source mismatch")
			break
		}
		l.queue <- e
	}
	delete(run.pending, k)
	creationErr := l.err
	s.mu.Unlock()
	go s.process(run, l)
	if creationErr != nil {
		return resource.Session{}, creationErr
	}
	return r, nil
}
func (s *Service) GetSession(ref resource.SessionRef) (resource.Session, error) {
	return s.meta.GetSession(ref)
}
func (s *Service) SendPrompt(ctx context.Context, ref resource.SessionRef, input runtime.Input) (runtime.InputReceipt, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	var l *lane
	if run != nil {
		l = run.lanes[r.Status.SessionID]
	}
	if s.closing || run == nil || run.err != nil || l == nil || l.err != nil {
		s.mu.Unlock()
		return runtime.InputReceipt{}, errors.New("session is not available in this process")
	}
	d := run.driver
	s.mu.Unlock()
	return d.SendPrompt(ctx, r.Status.SessionID, input)
}
func (s *Service) ListHistoryEvents(ref resource.SessionRef, q store.HistoryQuery) (store.HistoryPage, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return store.HistoryPage{}, err
	}
	return s.history.ListHistory(r.Status.SessionID, q)
}
func (s *Service) SubscribeEvents(ref resource.SessionRef) (*Subscription, error) {
	r, err := s.meta.GetSession(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[r.Spec.RuntimeRef]
	if s.closing || run == nil || run.err != nil {
		return nil, errors.New("runtime not observed")
	}
	l := run.lanes[r.Status.SessionID]
	if l == nil || l.err != nil {
		return nil, errors.New("session not observed")
	}
	sub := newSubscription(func(sub *Subscription) { s.mu.Lock(); delete(l.subscribers, sub); s.mu.Unlock() })
	l.subscribers[sub] = true
	return sub, nil
}
func (s *Service) consume(run *runBinding, stream runtime.Stream) {
	defer close(run.done)
	for {
		e, err := stream.Next(context.Background())
		if err != nil {
			if err != io.EOF {
				s.mu.Lock()
				run.err = err
				for _, l := range run.lanes {
					for sub := range l.subscribers {
						sub.finish(err)
					}
				}
				s.mu.Unlock()
			}
			return
		}
		s.mu.Lock()
		l := run.lanes[e.SessionID]
		if l == nil {
			op := run.pending[addressKey(runtime.Address{Namespace: e.Namespace, SessionName: e.SessionName})]
			if op != nil {
				if len(op.events) == queueCapacity {
					op.err = errors.New("creation event buffer exhausted")
				} else {
					op.events = append(op.events, e)
				}
			} else {
				run.err = errors.New("unbound session event")
			}
			s.mu.Unlock()
			continue
		}
		if !l.source.SameRun(e.Source()) || e.Namespace != l.session.Metadata.Namespace || e.SessionName != l.session.Metadata.Name {
			l.err = errors.New("event source or name mismatch")
			s.mu.Unlock()
			continue
		}
		select {
		case l.queue <- e:
		default:
			l.err = errors.New("session event queue exhausted")
		}
		s.mu.Unlock()
	}
}
func (s *Service) fail(l *lane, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.err = err
	for sub := range l.subscribers {
		sub.finish(err)
	}
}
func (s *Service) process(run *runBinding, l *lane) {
	defer close(l.done)
	for e := range l.queue {
		s.mu.Lock()
		failed := l.err
		s.mu.Unlock()
		if failed != nil {
			continue
		}
		if e.Persistent() {
			if _, err := s.history.Append(e.SessionID, []runtime.SessionEvent{e}); err != nil {
				s.fail(l, fmt.Errorf("history commit failed: %w", err))
				continue
			}
		}
		if state, ok := e.Payload().(runtime.StateUpdate); ok {
			work := e.Source().Work()
			if state.State != nil && *state.State == runtime.Running {
				l.work = work
			}
			if work == "" || l.work == "" || work == l.work {
				r, err := s.meta.GetSession(resource.SessionRef{SessionID: e.SessionID})
				if err == nil {
					if r.Status.State != "closing" && r.Status.State != "archived" {
						r.Status.State = runtime.Unknown
						if state.State != nil {
							r.Status.State = *state.State
						}
						_, err = s.meta.UpdateSession(r)
					}
				}
				if err != nil {
					s.fail(l, err)
					continue
				}
			}
		}
		s.mu.Lock()
		for sub := range l.subscribers {
			copy, err := e.Clone()
			if err != nil {
				sub.finish(err)
				continue
			}
			sub.publish(copy)
		}
		s.mu.Unlock()
		if request, ok := e.Payload().(runtime.InteractionRequested); ok && s.policy != nil {
			if answer, handled := s.policy(request); handled {
				if _, err := run.driver.AnswerInteraction(context.Background(), e.SessionID, request.Interaction, answer); err != nil {
					s.fail(l, err)
				}
			}
		}
	}
}

// Shutdown is the normal-path application cleanup for this vertical slice.
// Full stop/recovery, cancellation and unknown-operation settlement are later tickets.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	runs := make([]*runBinding, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	s.mu.Unlock()
	var result error
	for _, run := range runs {
		run.op.Lock()
		if run.drained {
			run.op.Unlock()
			continue
		}
		if run.driver == nil {
			run.op.Unlock()
			continue
		}
		s.mu.Lock()
		ids := make([]string, 0, len(run.lanes))
		for id := range run.lanes {
			ids = append(ids, id)
		}
		s.mu.Unlock()
		for _, id := range ids {
			if err := run.driver.CloseSession(ctx, id); err != nil {
				result = errors.Join(result, err)
			}
		}
		if err := run.driver.Stop(ctx); err != nil {
			result = errors.Join(result, err)
			run.op.Unlock()
			continue
		}
		select {
		case <-run.done:
		case <-ctx.Done():
			run.op.Unlock()
			return errors.Join(result, ctx.Err())
		}
		s.mu.Lock()
		for _, l := range run.lanes {
			close(l.queue)
		}
		s.mu.Unlock()
		for _, l := range run.lanes {
			select {
			case <-l.done:
			case <-ctx.Done():
				run.op.Unlock()
				return errors.Join(result, ctx.Err())
			}
			s.mu.Lock()
			result = errors.Join(result, l.err)
			for sub := range l.subscribers {
				sub.finish(io.EOF)
			}
			s.mu.Unlock()
		}
		run.drained = true
		run.op.Unlock()
	}
	return result
}
