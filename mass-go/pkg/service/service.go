// Package service owns persistent resource orchestration and commit-before-publish.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

const queueCapacity = 128

// RuntimeService and SessionService share an owner, without transport objects.
type RuntimeService interface {
	CreateRuntime(resource.Runtime) (resource.Runtime, error)
	GetRuntime(string) (resource.Runtime, error)
	ListRuntimes(resource.RuntimeQuery) ([]resource.Runtime, error)
	UpdateRuntime(string, resource.RuntimeSpec) (resource.Runtime, error)
	DeleteRuntime(string) error
	StopRuntime(context.Context, string) (resource.Runtime, error)
	StartRuntime(context.Context, string) (resource.Runtime, error)
}
type SessionService interface {
	ResolveSession(namespace, nameOrID string) (resource.SessionRef, error)
	CreateSession(context.Context, resource.Session) (resource.Session, error)
	GetSession(resource.SessionRef) (resource.Session, error)
	ListSessions(resource.SessionQuery) (resource.SessionPage, error)
	ArchiveSession(context.Context, resource.SessionRef) (resource.Session, error)
	ResumeSession(context.Context, resource.SessionRef) (resource.Session, error)
	SendPrompt(context.Context, resource.SessionRef, runtime.Input) (runtime.InputReceipt, error)
	Cancel(context.Context, resource.SessionRef) (runtime.CancelReceipt, error)
	AnswerInteraction(context.Context, resource.SessionRef, string, any) (runtime.InteractionReceipt, error)
	ListHistoryEvents(resource.SessionRef, store.HistoryQuery) (store.HistoryPage, error)
	SubscribeEvents(resource.SessionRef) (*Subscription, error)
}
type runBinding struct {
	driver        runtime.Driver
	stream        events.Stream
	generation    uint64
	done          chan struct{}
	err           error
	pending       map[string]*opening
	lanes         map[string]*lane
	op            sync.Mutex
	admission     sync.RWMutex
	started       bool
	stopping      bool
	stopStarted   chan struct{}
	stopStatusErr error
	drained       bool
	blocked       *events.SessionEvent
}
type opening struct {
	address   runtime.Address
	events    []events.SessionEvent
	err       error
	done      chan struct{}
	abandoned bool
	native    runtime.DriverSession
	requested resource.Session
	released  bool
}
type delivery struct {
	event   events.SessionEvent
	barrier chan struct{}
}
type lane struct {
	session     resource.Session
	source      events.Source
	queue       chan delivery
	done        chan struct{}
	err         error
	work        string
	closing     bool
	retry       chan chan error
	commitErr   error
	subscribers map[*Subscription]bool
}
type InteractionPolicy func(events.InteractionRequested) (map[string]any, bool)

type Service struct {
	policy      InteractionPolicy
	meta        store.MetaStore
	history     store.HistoryStore
	registry    *runtime.Registry
	mu          sync.Mutex
	runs        map[string]*runBinding
	observers   map[string]map[*Subscription]bool
	sessionOps  map[string]*sessionOperation
	closing     bool
	resumeSlots chan struct{}
}

func New(meta store.MetaStore, history store.HistoryStore, registry *runtime.Registry) *Service {
	return &Service{meta: meta, history: history, registry: registry, runs: map[string]*runBinding{}, observers: map[string]map[*Subscription]bool{}, sessionOps: map[string]*sessionOperation{}, resumeSlots: make(chan struct{}, queueCapacity)}
}

// NewWithPolicy explicitly enables an application-owned interaction policy.
func NewWithPolicy(meta store.MetaStore, history store.HistoryStore, registry *runtime.Registry, policy InteractionPolicy) *Service {
	s := New(meta, history, registry)
	s.policy = policy
	return s
}

func (s *Service) AnswerInteraction(ctx context.Context, ref resource.SessionRef, interaction string, answer any) (runtime.InteractionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return runtime.InteractionReceipt{}, err
	}
	r, resolveErr := s.GetSession(ref)
	if resolveErr != nil {
		return runtime.InteractionReceipt{}, resolveErr
	}
	ref = resource.SessionRef{SessionID: r.Status.SessionID}
	if interaction == "" || !utf8.ValidString(interaction) {
		return runtime.InteractionReceipt{}, runtime.InvalidArgument("invalid interaction reference")
	}
	if err := events.CheckDynamicJSON(answer); err != nil {
		return runtime.InteractionReceipt{}, runtime.InvalidArgument("invalid JSON answer")
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return runtime.InteractionReceipt{}, runtime.InvalidArgument("invalid JSON answer")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&answer); err != nil {
		return runtime.InteractionReceipt{}, runtime.InvalidArgument("invalid JSON answer")
	}
	return sessionWork(ctx, s, ref, false, func(owner context.Context, run *runBinding, id string) (runtime.InteractionReceipt, error) {
		evidence, err := run.driver.AnswerInteraction(owner, id, interaction, answer)
		return runtime.InteractionReceipt{Interaction: interaction, Evidence: evidence}, err
	})
}

var label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for p := range strings.SplitSeq(name, ".") {
		if !label.MatchString(p) {
			return false
		}
	}
	return true
}

func validNamespace(namespace string) bool {
	return len(namespace) <= 63 && label.MatchString(namespace)
}

func validateMetadata(m resource.Metadata, session bool) error {
	if !validName(m.Name) {
		return runtime.InvalidArgument("invalid DNS resource name")
	}
	if session {
		if !validNamespace(m.Namespace) {
			return runtime.InvalidArgument("invalid DNS namespace")
		}
	} else if m.Namespace != "" {
		return runtime.InvalidArgument("runtime namespace must be empty")
	}
	return nil
}
func addressKey(a runtime.Address) string { return a.Namespace + "/" + a.SessionName }
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
		if existing.stopping {
			s.mu.Unlock()
			return r, errors.New("runtime stopping")
		}
		s.mu.Unlock()
		if err := lockOperation(ctx, &existing.op); err != nil {
			return r, err
		}
		defer existing.op.Unlock()
		s.mu.Lock()
		err := existing.err
		started := existing.started && !existing.stopping
		s.mu.Unlock()
		if err != nil {
			return r, err
		}
		if !started {
			return r, errors.New("runtime start not confirmed")
		}
		return s.meta.GetRuntime(name)
	}
	if r.Status.State != "stopped" {
		s.mu.Unlock()
		return r, errors.New("previous runtime release not confirmed; explicit reconciliation required")
	}
	if len(s.runs) >= queueCapacity {
		s.mu.Unlock()
		return r, errors.New("runtime capacity exhausted")
	}
	// Reserve the original owner before constructing or starting any Driver.
	run := &runBinding{done: make(chan struct{}), pending: map[string]*opening{}, lanes: map[string]*lane{}}
	run.op.Lock()
	s.runs[name] = run
	s.mu.Unlock()
	defer run.op.Unlock()
	r, err = s.meta.GetRuntime(name)
	if err != nil {
		close(run.done)
		s.mu.Lock()
		run.err = err
		s.mu.Unlock()
		return r, err
	}
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
	run.stream = stream
	go s.consume(run, stream)
	if _, err = s.runtimeStatus(name, resource.RuntimeStatus{State: "starting"}); err != nil {
		s.mu.Lock()
		run.err = err
		s.mu.Unlock()
		return r, err
	}
	capabilities, err := d.Start(ctx)
	if err != nil {
		s.mu.Lock()
		run.err = err
		state := "unknown"
		if run.stopping {
			state = "stopping"
		}
		s.mu.Unlock()
		_, statusErr := s.runtimeStatus(name, resource.RuntimeStatus{State: state})
		return r, errors.Join(err, statusErr)
	}
	applied := r.Metadata.Generation
	s.mu.Lock()
	state := "running"
	if run.stopping || s.closing {
		state = "stopping"
	}
	s.mu.Unlock()
	r, err = s.runtimeStatus(name, resource.RuntimeStatus{State: state, AppliedGeneration: applied, Capabilities: capabilities})
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

var uuidSessionName = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Service) CreateSession(ctx context.Context, r resource.Session) (resource.Session, error) {
	if uuidSessionName.MatchString(r.Metadata.Name) {
		return resource.Session{}, &runtime.OperationError{Code: "invalid_argument", Reason: "session_name_is_uuid", Message: "Session creation name must not be a UUID"}
	}
	if err := validateMetadata(r.Metadata, true); err != nil {
		return r, err
	}
	if r.Status.SessionID != "" || r.Status.State != "" || r.Status.AppliedGeneration != 0 || r.Status.RuntimeGeneration != 0 || r.Status.Capabilities != nil {
		return resource.Session{}, runtime.InvalidArgument("system status is read-only")
	}
	if !validName(r.Spec.RuntimeRef) {
		return resource.Session{}, runtime.InvalidArgument("invalid runtime reference")
	}
	if !filepath.IsAbs(r.Spec.Cwd) {
		return r, runtime.InvalidArgument("absolute cwd required")
	}
	if r.Metadata.UID != "" || r.Metadata.Generation != 0 || r.Metadata.ResourceVersion != "" || !r.Metadata.CreatedAt.IsZero() {
		return r, runtime.InvalidArgument("system metadata is read-only")
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	closing := s.closing
	s.mu.Unlock()
	if run == nil || closing {
		return r, errors.New("runtime not started or service closing")
	}
	s.mu.Lock()
	if s.closing || run.err != nil || !run.started || run.stopping {
		s.mu.Unlock()
		return r, errors.New("runtime unavailable")
	}
	pendingCount := 0
	for _, owner := range s.runs {
		pendingCount += len(owner.pending)
	}
	if len(s.observers)+pendingCount >= queueCapacity*queueCapacity {
		s.mu.Unlock()
		return r, errors.New("observation ownership capacity exhausted")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	if run.pending[k] != nil || len(run.pending)+len(run.lanes) >= queueCapacity {
		s.mu.Unlock()
		return r, store.ErrConflict
	}
	op := &opening{address: a, done: make(chan struct{})}
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
	type result struct {
		session resource.Session
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		defer close(op.done)
		value, err := func() (resource.Session, error) {
			native, err := run.driver.OpenSession(context.WithoutCancel(ctx), a, r.Spec.SessionSpec)
			if err != nil {
				if errors.Is(err, runtime.ErrResourcesReleased) {
					removePending()
					return r, err
				}
				// Unknown opens retain the original reservation.
				s.mu.Lock()
				op.err = err
				s.mu.Unlock()
				return r, err
			}
			s.mu.Lock()
			op.native = native
			abandoned := op.abandoned || run.stopping
			s.mu.Unlock()
			if invalid := resource.ValidateSessionID(native.SessionID); invalid != nil {
				cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
				s.mu.Lock()
				op.released = cleanup == nil
				if cleanup == nil {
					delete(run.pending, k)
				}
				s.mu.Unlock()
				if cleanup != nil {
					return resource.Session{}, errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "invalid_session_cleanup_unknown", Message: "Invalid native identity cleanup remains unsettled"}, cleanup)
				}
				return resource.Session{}, invalid
			}
			if abandoned {
				cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
				if cleanup == nil {
					removePending()
				}
				return resource.Session{}, errors.Join(errors.New("outcome_unknown: abandoned create settled against original owner"), cleanup)
			}
			r.Status = resource.SessionStatus{SessionID: native.SessionID, State: events.Unknown, AppliedGeneration: 1, RuntimeGeneration: run.generation, Capabilities: native.Capabilities}
			s.mu.Lock()
			op.requested = r
			s.mu.Unlock()
			r, err = s.meta.CreateSession(r)
			if err != nil {
				cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
				s.mu.Lock()
				op.released = cleanup == nil
				s.mu.Unlock()
				// Metadata may already be committed: retain reservation until explicit readback.
				return resource.Session{}, errors.Join(err, cleanup)
			}
			l := &lane{session: r, source: native.Source, queue: make(chan delivery, queueCapacity), retry: make(chan chan error), done: make(chan struct{}), subscribers: map[*Subscription]bool{}}
			s.mu.Lock()
			l.err = op.err
			s.observers[native.SessionID] = l.subscribers
			run.lanes[native.SessionID] = l
			for _, e := range op.events {
				if !native.Source.SameRun(e.Source()) {
					l.err = errors.New("creation event source mismatch")
					break
				}
				l.queue <- delivery{event: e}
			}
			abandoned = op.abandoned || run.stopping
			if !abandoned {
				delete(run.pending, k)
			}
			creationErr := l.err
			s.mu.Unlock()
			go s.process(run, l)
			if abandoned {
				cleanup := s.settleAbandoned(run, l)
				if cleanup == nil {
					removePending()
				}
				return resource.Session{}, errors.Join(errors.New("outcome_unknown: late creation archived by original owner"), cleanup)
			}
			if creationErr != nil {
				return resource.Session{}, creationErr
			}
			return r, nil
		}()
		resultCh <- result{value, err}
	}()
	select {
	case value := <-resultCh:
		return value.session, value.err
	case <-ctx.Done():
		s.mu.Lock()
		op.abandoned = true
		s.mu.Unlock()
		return resource.Session{}, fmt.Errorf("outcome_unknown: creation continues under original reservation: %w", ctx.Err())
	}
}

func (s *Service) GetSession(ref resource.SessionRef) (resource.Session, error) {
	if ref.SessionID != "" {
		if ref.Name != "" || ref.Namespace != "" {
			return resource.Session{}, runtime.InvalidArgument("session reference forms are mutually exclusive")
		}
		if err := resource.ValidateSessionID(ref.SessionID); err != nil {
			return resource.Session{}, err
		}
	} else if !validNamespace(ref.Namespace) || !validName(ref.Name) {
		return resource.Session{}, runtime.InvalidArgument("invalid session reference")
	}
	r, err := s.meta.GetSession(ref)
	if err == nil {
		err = resource.ValidateSessionID(r.Status.SessionID)
	}
	return r, err
}

func (s *Service) SendPrompt(ctx context.Context, ref resource.SessionRef, input runtime.Input) (runtime.InputReceipt, error) {
	if err := ctx.Err(); err != nil {
		return runtime.InputReceipt{}, err
	}
	r, resolveErr := s.GetSession(ref)
	if resolveErr != nil {
		return runtime.InputReceipt{}, resolveErr
	}
	ref = resource.SessionRef{SessionID: r.Status.SessionID}
	if len(input.Content) == 0 {
		return runtime.InputReceipt{}, runtime.InvalidArgument("empty input")
	}
	content := make([]events.ContentBlock, len(input.Content))
	for i, block := range input.Content {
		if err := block.Validate(); err != nil {
			return runtime.InputReceipt{}, runtime.InvalidArgument("invalid content block")
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return runtime.InputReceipt{}, runtime.InvalidArgument("invalid content block")
		}
		if err = json.Unmarshal(raw, &content[i]); err != nil {
			return runtime.InputReceipt{}, runtime.InvalidArgument("invalid content block")
		}
	}
	input.Content = content
	return sessionWork(ctx, s, ref, false, func(owner context.Context, run *runBinding, id string) (runtime.InputReceipt, error) {
		return run.driver.SendPrompt(owner, id, input)
	})
}

func (s *Service) ListHistoryEvents(ref resource.SessionRef, q store.HistoryQuery) (store.HistoryPage, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return store.HistoryPage{}, err
	}
	return s.history.ListHistory(r.Status.SessionID, q)
}

func (s *Service) SubscribeEvents(ref resource.SessionRef) (*Subscription, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[r.Spec.RuntimeRef]
	if s.closing || run == nil || run.err != nil || run.stopping {
		return nil, runtime.Unavailable("runtime_not_observed")
	}
	l := run.lanes[r.Status.SessionID]
	if l == nil || l.err != nil || l.closing {
		return nil, runtime.Unavailable("session_not_observed")
	}
	if len(l.subscribers) >= queueCapacity {
		return nil, runtime.Exhausted("subscription_limit")
	}
	sub := newSubscription(func(sub *Subscription) {
		s.mu.Lock()
		delete(l.subscribers, sub)
		if len(l.subscribers) == 0 {
			delete(s.observers, r.Status.SessionID)
		}
		s.mu.Unlock()
	})
	l.subscribers[sub] = true
	s.observers[r.Status.SessionID] = l.subscribers
	return sub, nil
}

func (s *Service) consume(run *runBinding, stream events.Stream) {
	defer close(run.done)
	for {
		e, err := stream.Next(context.Background())
		if err != nil {
			s.mu.Lock()
			stopping := run.stopping
			s.mu.Unlock()
			if !errors.Is(err, io.EOF) || !stopping {
				s.mu.Lock()
				if errors.Is(err, io.EOF) {
					err = errors.New("outcome_unknown: unexpected driver EOF")
				}
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
					op.err = errors.New("creation event buffer exhausted; stream responsibility retained")
					run.err = op.err
					run.blocked = &e
					s.mu.Unlock()
					return
				} else {
					op.events = append(op.events, e)
				}
			} else {
				run.err = errors.New("unbound session event; original delivery retained")
				run.blocked = &e
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			continue
		}
		if !l.source.SameRun(e.Source()) || e.Namespace != l.session.Metadata.Namespace || e.SessionName != l.session.Metadata.Name {
			// Old run facts cannot mutate or poison the current lane.
			s.mu.Unlock()
			continue
		}
		select {
		case l.queue <- delivery{event: e}:
		default:
			l.err = errors.New("session event queue exhausted; stream responsibility retained")
			run.err = l.err
			run.blocked = &e
			s.mu.Unlock()
			return
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
	for entry := range l.queue {
		if entry.barrier != nil {
			close(entry.barrier)
			continue
		}
		e := entry.event
		if e.Persistent() {
			if _, err := s.history.Append(e.SessionID, []events.SessionEvent{e}); err != nil {
				s.fail(l, fmt.Errorf("history commit failed: %w", err))
				s.mu.Lock()
				l.commitErr = err
				s.mu.Unlock()
				for {
					response := <-l.retry
					err = s.reconcileEvent(e)
					response <- err
					if err == nil {
						s.mu.Lock()
						l.commitErr = nil
						s.mu.Unlock()
						break
					}
				}
			}
		}
		if state, ok := e.Payload().(events.StateUpdate); ok {
			work := e.Source().Work()
			if state.State != nil && *state.State == events.Running && (l.work == "" || l.work == work) {
				l.work = work
			}
			if work == "" || l.work == "" || work == l.work {
				_, err := s.sessionStatus(resource.SessionRef{SessionID: e.SessionID}, func(r *resource.Session) {
					if r.Status.State != "closing" && r.Status.State != "archived" && r.Status.AppliedGeneration != 0 {
						r.Status.State = events.Unknown
						if state.State != nil {
							r.Status.State = *state.State
						}
					}
				})
				if err != nil {
					s.fail(l, err)
				}
				if state.State != nil && *state.State == events.Idle && l.work == work {
					l.work = ""
				}
			}
		}
		s.mu.Lock()
		for sub := range l.subscribers {
			cloned, err := e.Clone()
			if err != nil {
				sub.finish(err)
				continue
			}
			sub.publish(cloned)
			if sub.ended() {
				delete(l.subscribers, sub)
				if len(l.subscribers) == 0 {
					delete(s.observers, e.SessionID)
				}
			}
		}
		s.mu.Unlock()
		if request, ok := e.Payload().(events.InteractionRequested); ok && s.policy != nil {
			run.admission.RLock()
			s.mu.Lock()
			allowed := !s.closing && !run.stopping && !l.closing && l.err == nil
			s.mu.Unlock()
			if answer, handled := s.policy(request); handled && allowed {
				if _, err := run.driver.AnswerInteraction(context.Background(), e.SessionID, request.Interaction, answer); err != nil {
					s.fail(l, err)
				}
			}
			run.admission.RUnlock()
		}
	}
}

// Shutdown seals application admission and uses the same normal stop coordinator.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	names := make([]string, 0, len(s.runs))
	for name := range s.runs {
		names = append(names, name)
	}
	s.mu.Unlock()
	// Independent runtimes drain concurrently within the bounded runtime limit.
	results := make(chan error, len(names))
	for _, name := range names {
		go func(name string) { _, err := s.StopRuntime(ctx, name); results <- err }(name)
	}
	var result error
	for range names {
		result = errors.Join(result, <-results)
	}
	return result
}

// Cancel targets the original lane and remains available during drain. A receipt
// records only dispatch/acknowledgment; native termination must still be observed.
func (s *Service) Cancel(ctx context.Context, ref resource.SessionRef) (runtime.CancelReceipt, error) {
	return sessionWork(ctx, s, ref, true, func(owner context.Context, run *runBinding, id string) (runtime.CancelReceipt, error) {
		return run.driver.Cancel(owner, id)
	})
}

// ReconcileHistory is an explicit experiment-only settlement operation. It does
// not reopen input admission. No original UUID or event content is regenerated.
func (s *Service) ReconcileHistory(ctx context.Context, ref resource.SessionRef) error {
	r, err := s.GetSession(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	if run == nil {
		s.mu.Unlock()
		return errors.New("original owner unavailable")
	}
	l := run.lanes[r.Status.SessionID]
	if l == nil || l.commitErr == nil {
		s.mu.Unlock()
		return errors.New("no pending history commit")
	}
	s.mu.Unlock()
	response := make(chan error, 1)
	select {
	case l.retry <- response:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) reconcileEvent(e events.SessionEvent) error {
	// Validate the whole committed prefix before retrying, including partial writes.
	q := store.HistoryQuery{}
	for {
		page, err := s.history.ListHistory(e.SessionID, q)
		if err != nil {
			return err
		}
		for _, saved := range page.Events {
			if saved.SessionID != e.SessionID || saved.Namespace != e.Namespace || saved.SessionName != e.SessionName || !saved.Persistent() {
				return errors.New("committed history identity mismatch")
			}
			if err := saved.Validate(); err != nil {
				return err
			}
			if saved.EventID == e.EventID {
				a, err := json.Marshal(saved)
				if err != nil {
					return err
				}
				b, err := json.Marshal(e)
				if err != nil {
					return err
				}
				if !bytes.Equal(a, b) {
					return errors.New("history UUID content conflict")
				}
			}
		}
		if page.Next == "" {
			break
		}
		q.After = page.Next
	}
	_, err := s.history.Append(e.SessionID, []events.SessionEvent{e})
	return err
}

// ReconcileDelivery resumes the original sole consumer after explicit overload
// settlement. It does not reacquire Events or reopen runtime/input admission.
func (s *Service) ReconcileDelivery(ctx context.Context, name string) error {
	s.mu.Lock()
	run := s.runs[name]
	s.mu.Unlock()
	if run == nil {
		return errors.New("original owner unavailable")
	}
	run.op.Lock()
	defer run.op.Unlock()
	s.mu.Lock()
	retained := run.blocked != nil
	s.mu.Unlock()
	if !retained {
		return errors.New("no retained delivery")
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	e := run.blocked
	if e == nil {
		s.mu.Unlock()
		return errors.New("no retained delivery")
	}
	l := run.lanes[e.SessionID]
	if l == nil || !l.source.SameRun(e.Source()) {
		s.mu.Unlock()
		return errors.New("original lane not settled")
	}
	s.mu.Unlock()
	select {
	case l.queue <- delivery{event: *e}:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	run.blocked = nil
	run.done = make(chan struct{})
	s.mu.Unlock()
	go s.consume(run, run.stream)
	return nil
}

// Called only by the original opening owner. Stop waits op.done before touching
// this lane, so late cleanup must not wait on Stop's runtime operation mutex.
func (s *Service) settleAbandoned(run *runBinding, l *lane) error {
	s.mu.Lock()
	l.closing = true
	s.mu.Unlock()
	ref := resource.SessionRef{SessionID: l.session.Status.SessionID}
	if _, err := s.sessionStatus(ref, func(r *resource.Session) { r.Status.State = "closing" }); err != nil {
		return err
	}
	if err := run.driver.CloseSession(context.Background(), ref.SessionID); err != nil {
		return err
	}
	if err := s.drainLane(context.Background(), l); err != nil {
		return err
	}
	if _, err := s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r, true) }); err != nil {
		return err
	}
	s.mu.Lock()
	delete(run.lanes, ref.SessionID)
	close(l.queue)
	s.mu.Unlock()
	<-l.done
	return nil
}
