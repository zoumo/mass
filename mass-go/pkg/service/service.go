// Package service owns persistent resource orchestration and commit-before-publish.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

const (
	queueCapacity             = 128
	laneRetentionCapacity     = 2 * queueCapacity
	sessionDeliveryOverloaded = "session_delivery_overloaded"
	overloadCloseTimeout      = 5 * time.Second
	// Assembly budget for an in-flight observational write. Expiry reports
	// incomplete cleanup and retains the Handler for an explicit retry.
	stateHandlerCloseTimeout = time.Second
)

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
	CreateSessionWithInput(context.Context, resource.SessionCreateInput) (resource.SessionCreated, error)
	GetSession(resource.SessionRef) (resource.Session, error)
	ListSessions(resource.SessionQuery) (resource.SessionPage, error)
	CloseSession(context.Context, resource.SessionRef) (resource.Session, error)
	ResumeSession(context.Context, resource.SessionRef) (resource.Session, error)
	ResumeSessionWithInput(context.Context, resource.SessionRef, runtime.Input) (resource.SessionResumed, error)
	LoadSession(context.Context, resource.SessionLoad) (resource.SessionLoadAccepted, error)
	CancelHistoryLoad(context.Context, resource.SessionRef, string) (resource.HistoryLoadStatus, error)
	SendPrompt(context.Context, resource.SessionRef, runtime.Input) (runtime.InputReceipt, error)
	Cancel(context.Context, resource.SessionRef) (runtime.CancelReceipt, error)
	AnswerInteraction(context.Context, resource.SessionRef, string, any) (runtime.InteractionReceipt, error)
	ListHistoryEvents(resource.SessionRef, store.HistoryQuery) (store.HistoryPage, error)
	SubscribeEvents(resource.SessionRef) (*Subscription, error)
}
type runBinding struct {
	runtimeName   string
	driver        runtime.Driver
	stream        events.Stream
	generation    uint64
	done          chan struct{}
	err           error
	pending       map[string]*opening
	lanes         map[string]*lane
	retired       map[events.Source]*lane
	op            sync.Mutex
	admission     sync.RWMutex
	started       bool
	stopping      bool
	stop          *runtimeStop
	driverStopped bool
	streamErr     error
	drained       bool
	blocked       *events.SessionEvent
}
type opening struct {
	cancel    context.CancelFunc // Stop requests cleanup; the opening owner still proves release.
	restoring bool
	buffering bool
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
	load           *historyLoad
	admission      sync.RWMutex
	releasing      bool
	nativeClosed   bool
	observedState  events.SessionState
	stateObserved  bool
	session        resource.Session
	source         events.Source
	queue          chan delivery
	done           chan struct{}
	err            error
	closing        bool
	overloaded     bool
	retry          chan chan error
	commitErr      error
	publicationErr error
	idleTimer      Timer
	idleVersion    uint64
	activeTools    map[string]struct{}
	interactions   map[string]struct{}
}
type InteractionPolicy func(events.InteractionRequested) (map[string]any, bool)

type sessionBroadcast struct {
	broadcast   *events.EventBroadcast
	runtimeName string
}

type Service struct {
	policy      InteractionPolicy
	meta        metastore.MetaStore
	history     eventstore.EventStore
	registry    *runtime.Registry
	startup     sync.Mutex
	mu          sync.Mutex
	runs        map[string]*runBinding
	broadcasts  map[string]*sessionBroadcast
	sessionOps  map[string]*sessionOperation
	closing     bool
	resumeSlots chan struct{}
	idleTimeout time.Duration
	clock       Clock
}

func New(meta metastore.MetaStore, history eventstore.EventStore, registry *runtime.Registry) *Service {
	return NewWithOptions(meta, history, registry, Options{SessionIdleTimeout: DefaultSessionIdleTimeout})
}

// NewWithPolicy explicitly enables an application-owned interaction policy.
func NewWithPolicy(meta metastore.MetaStore, history eventstore.EventStore, registry *runtime.Registry, policy InteractionPolicy) *Service {
	return NewWithOptions(meta, history, registry, Options{InteractionPolicy: policy, SessionIdleTimeout: DefaultSessionIdleTimeout})
}

// NewWithOptions assembles explicit application policy and time dependencies.
func NewWithOptions(meta metastore.MetaStore, history eventstore.EventStore, registry *runtime.Registry, options Options) *Service {
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Service{
		policy:      options.InteractionPolicy,
		meta:        meta,
		history:     history,
		registry:    registry,
		runs:        map[string]*runBinding{},
		broadcasts:  map[string]*sessionBroadcast{},
		sessionOps:  map[string]*sessionOperation{},
		resumeSlots: make(chan struct{}, queueCapacity),
		idleTimeout: options.SessionIdleTimeout,
		clock:       clock,
	}
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
	r, err := s.GetRuntime(name)
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
			return r, runtime.FailedPrecondition("runtime stopping")
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
	// A nil run binding means this process has no owner for the persisted
	// Runtime. Explicit start/resume may replace a cold unknown from a restart;
	// same-process unsettled owners retain their binding above.
	if r.Status.State != "stopped" && r.Status.State != "unknown" {
		s.mu.Unlock()
		return r, errors.New("previous runtime release not confirmed; explicit reconciliation required")
	}
	if len(s.runs) >= queueCapacity {
		s.mu.Unlock()
		return r, errors.New("runtime capacity exhausted")
	}
	// Reserve the original owner before constructing or starting any Driver.
	run := &runBinding{runtimeName: name, done: make(chan struct{}), pending: map[string]*opening{}, lanes: map[string]*lane{}}
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
	if _, err = s.runtimeStatus(name, func(status *resource.RuntimeStatus) {
		if status.State != "stopping" {
			*status = resource.RuntimeStatus{State: "starting"}
		}
	}); err != nil {
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
		_, statusErr := s.runtimeStatus(name, func(status *resource.RuntimeStatus) {
			if status.State != "stopping" {
				*status = resource.RuntimeStatus{State: state}
			}
		})
		return r, errors.Join(err, statusErr)
	}
	applied := r.Metadata.Generation
	s.mu.Lock()
	state := "running"
	if run.stopping || s.closing {
		state = "stopping"
	}
	s.mu.Unlock()
	r, err = s.runtimeStatus(name, func(status *resource.RuntimeStatus) {
		if status.State == "stopping" {
			state = "stopping"
		}
		*status = resource.RuntimeStatus{State: state, AppliedGeneration: applied, Capabilities: capabilities}
	})
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
	result, err := s.createSession(ctx, r, nil)
	return result.Session, err
}

// CreateSessionWithInput accepts transient caller input exactly once during open.
func (s *Service) CreateSessionWithInput(ctx context.Context, input resource.SessionCreateInput) (resource.SessionCreated, error) {
	return s.createSession(ctx, resource.Session{Metadata: input.Metadata, Spec: input.Spec}, input.InitialInput)
}

func (s *Service) createSession(ctx context.Context, r resource.Session, initial *runtime.Input) (resource.SessionCreated, error) {
	if err := ctx.Err(); err != nil {
		return resource.SessionCreated{}, err
	}
	if uuidSessionName.MatchString(r.Metadata.Name) {
		return resource.SessionCreated{}, &runtime.OperationError{Code: "invalid_argument", Reason: "session_name_is_uuid", Message: "Session creation name must not be a UUID"}
	}
	if err := validateMetadata(r.Metadata, true); err != nil {
		return resource.SessionCreated{Session: r}, err
	}
	if r.Status.SessionID != "" || r.Status.State != "" || r.Status.AppliedGeneration != 0 || r.Status.RuntimeGeneration != 0 || r.Status.Capabilities != nil || r.Status.EffectiveFeatures != nil {
		return resource.SessionCreated{}, runtime.InvalidArgument("system status is read-only")
	}
	if !validName(r.Spec.RuntimeRef) {
		return resource.SessionCreated{}, runtime.InvalidArgument("invalid runtime reference")
	}
	if !filepath.IsAbs(r.Spec.Cwd) {
		return resource.SessionCreated{Session: r}, runtime.InvalidArgument("absolute cwd required")
	}
	if err := validateSessionConfig(r.Spec.SessionSpec); err != nil {
		return resource.SessionCreated{Session: r}, err
	}
	if r.Metadata.UID != "" || r.Metadata.Generation != 0 || r.Metadata.ResourceVersion != "" || !r.Metadata.CreatedAt.IsZero() {
		return resource.SessionCreated{Session: r}, runtime.InvalidArgument("system metadata is read-only")
	}
	if _, err := s.meta.GetSession(resource.SessionRef{Namespace: r.Metadata.Namespace, Name: r.Metadata.Name}); !errors.Is(err, store.ErrNotFound) {
		if err == nil {
			err = store.ErrConflict
		}
		return resource.SessionCreated{Session: r}, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	startRuntime := run == nil || !run.started || run.stopping || run.err != nil
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return resource.SessionCreated{Session: r}, errors.New("service closing")
	}
	if startRuntime {
		if _, err := s.StartRuntime(ctx, r.Spec.RuntimeRef); err != nil {
			return resource.SessionCreated{Session: r}, err
		}
	}
	s.mu.Lock()
	run = s.runs[r.Spec.RuntimeRef]
	closing = s.closing
	s.mu.Unlock()
	if run == nil || closing {
		return resource.SessionCreated{Session: r}, errors.New("runtime unavailable")
	}
	resolvedSpec, err := s.resolveSessionSpec(run, r.Spec.SessionSpec)
	if err != nil {
		return resource.SessionCreated{Session: r}, err
	}
	var creator runtime.CreationDriver
	if initial != nil {
		copyInput, err := runtime.CopyInput(*initial)
		if err != nil {
			return resource.SessionCreated{}, err
		}
		initial = &copyInput
		var ok bool
		creator, ok = run.driver.(runtime.CreationDriver)
		if !ok {
			return resource.SessionCreated{}, runtime.Unsupported()
		}
		if err := creator.ValidateInitialInput(*initial); err != nil {
			return resource.SessionCreated{}, err
		}
	}
	s.mu.Lock()
	if s.closing || run.err != nil || !run.started || run.stopping {
		s.mu.Unlock()
		return resource.SessionCreated{Session: r}, errors.New("runtime unavailable")
	}
	pendingCount := 0
	for _, owner := range s.runs {
		pendingCount += len(owner.pending)
	}
	if len(s.broadcasts)+pendingCount >= queueCapacity*queueCapacity {
		s.mu.Unlock()
		return resource.SessionCreated{Session: r}, errors.New("observation ownership capacity exhausted")
	}
	a := runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name}
	k := addressKey(a)
	if run.pending[k] != nil || len(run.pending)+len(run.lanes) >= queueCapacity {
		s.mu.Unlock()
		return resource.SessionCreated{Session: r}, store.ErrConflict
	}
	owner, cancel := context.WithCancel(context.WithoutCancel(ctx))
	op := &opening{address: a, done: make(chan struct{}), cancel: cancel}
	run.pending[k] = op
	s.mu.Unlock()
	removePending := func() { s.mu.Lock(); delete(run.pending, k); s.mu.Unlock() }
	if _, err := s.meta.GetSession(resource.SessionRef{Namespace: a.Namespace, Name: a.SessionName}); !errors.Is(err, store.ErrNotFound) {
		removePending()
		cancel()
		close(op.done)
		if err == nil {
			err = store.ErrConflict
		}
		return resource.SessionCreated{Session: r}, err
	}
	type result struct {
		session resource.SessionCreated
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		defer close(op.done)
		defer cancel()
		var receipt *runtime.InputReceipt
		value, err := func() (resource.Session, error) {
			var native runtime.DriverSession
			var err error
			if initial == nil {
				native, err = run.driver.OpenSession(owner, a, resolvedSpec)
			} else {
				native, err = creator.OpenSessionWithInput(owner, a, resolvedSpec, *initial)
			}
			receipt = native.InitialInputReceipt
			if err != nil && native.SessionID != "" {
				// Native thread exists even when its first dispatch receipt is uncertain.
				// Keep its original address and buffered facts until explicit settlement.
				cleanup := run.driver.CloseSession(context.Background(), native.SessionID)
				s.mu.Lock()
				op.native = native
				op.released = cleanup == nil
				op.err = err
				s.mu.Unlock()
				return resource.Session{}, errors.Join(err, cleanup)
			}
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
			r.Status = resource.SessionStatus{SessionID: native.SessionID, State: events.Unknown, AppliedGeneration: 1, RuntimeGeneration: run.generation, Capabilities: native.Capabilities, EffectiveFeatures: &native.EffectiveFeatures}
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
			l := &lane{session: r, source: native.Source, queue: make(chan delivery, laneRetentionCapacity), retry: make(chan chan error), done: make(chan struct{})}
			s.mu.Lock()
			l.err = op.err
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
				return resource.Session{}, errors.Join(errors.New("outcome_unknown: late creation closed by original owner"), cleanup)
			}
			if creationErr != nil {
				return resource.Session{}, creationErr
			}
			return r, nil
		}()
		if err != nil {
			receipt = nil
		}
		resultCh <- result{resource.SessionCreated{Session: value, InitialInputReceipt: receipt}, err}
	}()
	select {
	case value := <-resultCh:
		return value.session, value.err
	case <-ctx.Done():
		s.mu.Lock()
		op.abandoned = true
		s.mu.Unlock()
		return resource.SessionCreated{}, fmt.Errorf("outcome_unknown: creation continues under original reservation: %w", ctx.Err())
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

// EventBroadcast resolves both Session references to the same realtime object.
// Resolving an offline Session never starts its Runtime.
func (s *Service) EventBroadcast(ref resource.SessionRef) (*events.EventBroadcast, error) {
	r, err := s.GetSession(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, runtime.Unavailable("service_closing")
	}
	return s.sessionBroadcastLocked(r)
}

func (s *Service) SubscribeEvents(ref resource.SessionRef) (*Subscription, error) {
	b, err := s.EventBroadcast(ref)
	if err != nil {
		return nil, err
	}
	return b.Subscribe()
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
				run.streamErr = err
				unresolved := make([]*lane, 0, len(run.lanes))
				for _, l := range run.lanes {
					if l.load != nil && !l.load.complete.Load() {
						unresolved = append(unresolved, l)
					}
				}
				s.mu.Unlock()
				for _, l := range unresolved {
					s.failLoad(run, l, "failed", err)
				}
			}
			return
		}
		s.mu.Lock()
		pending := run.pending[addressKey(runtime.Address{Namespace: e.Namespace, SessionName: e.SessionName})]
		if pending != nil && pending.buffering {
			if len(pending.events) >= queueCapacity {
				pending.err = errors.New("restore event buffer exhausted; stream responsibility retained")
				run.err = pending.err
				run.blocked = &e
				s.mu.Unlock()
				return
			}
			pending.events = append(pending.events, e)
			s.mu.Unlock()
			continue
		}
		l := run.lanes[e.SessionID]
		if original := run.retired[e.Source().WithWork("")]; original != nil {
			l = original
		}
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
		oldSource := run.retired[e.Source().WithWork("")] == l
		if l.load != nil && !l.load.bound {
			p, marker := e.Payload().(events.HistoryReplay)
			if marker && p.LoadID == l.load.id && (p.Phase == "started" || p.Phase == "failed" || p.Phase == "canceled") && e.Source().SameRun(e.Source()) && e.Namespace == l.session.Metadata.Namespace && e.SessionName == l.session.Metadata.Name {
				l.source = e.Source().WithWork("")
				l.load.bound = true
			}
		}
		if !oldSource && !l.source.SameRun(e.Source()) || e.Namespace != l.session.Metadata.Namespace || e.SessionName != l.session.Metadata.Name {
			// Old run facts cannot mutate or poison the current lane.
			s.mu.Unlock()
			continue
		}
		s.invalidateIdleLocked(l)
		// Normal delivery has 128 queued slots. The same channel reserves a
		// further 128 for the original closing source, never a forwarding queue.
		// Retired/already-closing owners retain the pre-existing fallback policy.
		if len(l.queue) >= queueCapacity && !l.overloaded && !l.closing && !oldSource {
			l.overloaded = true
			l.closing = true
			l.err = runtime.Exhausted(sessionDeliveryOverloaded)
			diagnostic, diagnosticErr := events.NewEvent(events.EventBase{
				EventID: uuid.NewString(), Namespace: e.Namespace, SessionName: e.SessionName,
				SessionID: e.SessionID, Time: time.Now(),
			}, events.SessionError{Reason: sessionDeliveryOverloaded, Message: "Session delivery exceeded 128 queued events; owned close started; cleanup remains unsettled until confirmed"}, e.Source())
			if diagnosticErr == nil {
				l.queue <- delivery{event: diagnostic}
			}
			if l.load != nil && !l.load.complete.Load() {
				go s.failLoad(run, l, "failed", l.err)
			} else {
				go s.closeOverloaded(run, l.session.Status.SessionID, l.source)
			}
		}
		limit := queueCapacity
		if l.overloaded {
			limit = laneRetentionCapacity
		}
		if len(l.queue) >= limit {
			l.err = errors.New("session event queue exhausted; stream responsibility retained")
			run.err = l.err
			run.blocked = &e
			s.mu.Unlock()
			return
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
		if marker, ok := e.Payload().(events.HistoryReplay); ok && marker.Phase == "native_completed" && l.load != nil && marker.LoadID == l.load.id && e.Validate() == nil {
			// The reader may run ahead of a blocked append or required handler.
			// Record native exhaustion without granting local delivery authority.
			h := l.load
			h.mu.Lock()
			if !h.nativeTerminal && !h.complete.Load() {
				h.status.NativeCompleted = true
				if h.status.Error == nil {
					h.status.Phase = "native_completed"
				}
				_ = s.writeLoad(l)
			}
			h.mu.Unlock()
		}
	}
}

// One bounded cleanup attempt per overloaded owner, outside the Runtime reader.
// A timeout/unknown close leaves closing plus original delivery responsibility;
// CloseSession/StopRuntime can explicitly settle it without replacing a Driver.
func (s *Service) closeOverloaded(run *runBinding, id string, source events.Source) {
	ctx, cancel := context.WithTimeout(context.Background(), overloadCloseTimeout)
	defer cancel()
	if _, err := s.closeOverloadedSession(ctx, resource.SessionRef{SessionID: id}, run, source); err != nil {
		slog.Warn("overloaded Session cleanup unsettled", "session_id", id, "error", err)
	}
}

func (s *Service) fail(l *lane, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.err = err
}

func (s *Service) process(run *runBinding, l *lane) {
	defer close(l.done)
	publicationFailed := false
	for entry := range l.queue {
		if entry.barrier != nil {
			close(entry.barrier)
			continue
		}
		e := entry.event
		if err := e.Validate(); err != nil {
			s.fail(l, err)
			s.failLoad(run, l, "failed", err)
			continue
		}
		if e.Replay != nil && (l.load == nil || e.Replay.LoadID != l.load.id || !e.Persistent() && e.Type() != "session.history_replay") {
			s.failLoad(run, l, "failed", runtime.FailedPrecondition("invalid replay source"))
			continue
		}
		if err := s.validateLoadEvent(l, e); err != nil {
			s.failLoad(run, l, "failed", err)
			continue
		}
		if e.Persistent() {
			if _, err := s.history.Append(e.SessionID, []events.SessionEvent{e}); err != nil {
				s.fail(l, fmt.Errorf("history commit failed: %w", err))
				s.failLoad(run, l, "failed", err)
				s.mu.Lock()
				l.commitErr = err
				if l.load != nil {
					close(l.load.deliveryFailed)
				}
				s.mu.Unlock()
				for {
					response := <-l.retry
					err = s.reconcileEvent(e)
					if err == nil {
						s.mu.Lock()
						l.commitErr = nil
						if l.load != nil {
							l.load.deliveryFailed = make(chan struct{})
						}
						s.mu.Unlock()
						response <- nil
						break
					}
					response <- err
				}
			}
		}
		s.mu.Lock()
		if state, ok := e.Payload().(events.StateUpdate); ok && e.Replay == nil {
			if !l.closing && l.source.SameRun(e.Source()) {
				l.stateObserved = true
				l.observedState = events.Unknown
				if state.State != nil {
					l.observedState = *state.State
				}
			}
		}
		if e.Replay == nil {
			switch payload := e.Payload().(type) {
			case events.ToolChunk:
				if l.activeTools == nil {
					l.activeTools = map[string]struct{}{}
				}
				l.activeTools[payload.ID] = struct{}{}
			case events.ToolCall:
				switch payload.Status {
				case "completed", "failed", "declined":
					delete(l.activeTools, payload.ID)
				default:
					if l.activeTools == nil {
						l.activeTools = map[string]struct{}{}
					}
					l.activeTools[payload.ID] = struct{}{}
				}
			case events.ToolResult:
				delete(l.activeTools, payload.ID)
			case events.InteractionRequested:
				if payload.Blocking {
					if l.interactions == nil {
						l.interactions = map[string]struct{}{}
					}
					l.interactions[payload.Interaction] = struct{}{}
				}
			case events.InteractionClosed:
				delete(l.interactions, payload.Interaction)
			}
		}
		s.scheduleIdleLocked(run, l)
		s.mu.Unlock()
		if !publicationFailed {
			b, err := s.broadcastForSession(l.session)
			if err == nil {
				err = b.Publish(e)
			}
			// A required handler may have partially delivered this UUID. Never
			// retry it or let later load facts overtake its failed publication.
			if err != nil {
				slog.Warn("realtime publication failed", "error", err, "session_id", e.SessionID)
				s.mu.Lock()
				l.publicationErr = errors.Join(l.publicationErr, err)
				s.mu.Unlock()
				if l.load != nil && !l.load.complete.Load() {
					publicationFailed = true
				}
				s.failLoad(run, l, "failed", err)
			}
		}
		if l.load != nil && e.Persistent() && e.Replay != nil {
			l.load.mu.Lock()
			l.load.status.LastCommittedEventID = e.EventID
			statusErr := s.writeLoad(l)
			l.load.mu.Unlock()
			if statusErr != nil {
				s.failLoad(run, l, "failed", statusErr)
			}
		}
		if _, marker := e.Payload().(events.HistoryReplay); marker {
			_ = s.loadMarker(run, l, e)
		}

		if request, ok := e.Payload().(events.InteractionRequested); ok && s.policy != nil {
			run.admission.RLock()
			s.mu.Lock()
			allowed := !s.closing && !run.stopping && !l.closing && !s.sessionCloseAcceptedLocked(e.SessionID) && l.err == nil && (l.load == nil || l.load.complete.Load()) && l.source.SameRun(e.Source())
			if allowed {
				l.admission.RLock()
			}
			s.mu.Unlock()
			if allowed {
				if answer, handled := s.policy(request); handled {
					if _, err := run.driver.AnswerInteraction(context.Background(), e.SessionID, request.Interaction, answer); err != nil {
						s.fail(l, err)
					}
				}
				l.admission.RUnlock()
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
	for name, run := range s.runs {
		for _, l := range run.lanes {
			stopIdleTimerLocked(l)
		}
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
	s.mu.Lock()
	broadcasts := make([]*events.EventBroadcast, 0, len(s.broadcasts))
	for _, owned := range s.broadcasts {
		// Failed Runtime stops retain their live delivery owners. Independently
		// settled upstreams can still release their own realtime resources.
		if s.runs[owned.runtimeName] == nil {
			broadcasts = append(broadcasts, owned.broadcast)
		}
	}
	s.mu.Unlock()
	for _, b := range broadcasts {
		result = errors.Join(result, b.Close())
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
		if err == nil && l.load != nil {
			// Explicit local reconciliation does not repeat native cleanup.
			// Re-evaluate its delivery proof after the original queue drains.
			go func() {
				<-l.load.cleanupDone
				if s.drainLane(context.Background(), l) != nil {
					return
				}
				l.load.mu.Lock()
				defer l.load.mu.Unlock()
				if l.load.cleanupErr == nil {
					l.load.status.CleanupCompleted = true
					_ = s.writeLoad(l)
				}
			}()
		}
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
	if original := run.retired[e.Source().WithWork("")]; original != nil {
		l = original
	} else if l != nil && !l.source.SameRun(e.Source()) {
		l = nil
	}
	if l == nil {
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
	if _, err := s.sessionStatus(ref, func(r *resource.Session) { offlineStatus(r) }); err != nil {
		return err
	}
	s.mu.Lock()
	retireLane(run, l)
	s.mu.Unlock()
	return nil
}

func (s *Service) broadcastForSession(r resource.Session) (*events.EventBroadcast, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionBroadcastLocked(r)
}

// sessionBroadcastLocked requires s.mu; public admission and ongoing publication
// share construction without sealing final events during shutdown.
func (s *Service) sessionBroadcastLocked(r resource.Session) (*events.EventBroadcast, error) {
	if owned := s.broadcasts[r.Status.SessionID]; owned != nil {
		return owned.broadcast, nil
	}
	b, err := events.NewBroadcast(r.Metadata.Namespace, r.Metadata.Name, r.Status.SessionID, queueCapacity)
	if err != nil {
		return nil, err
	}
	h := newStateHandler(s.meta, r, queueCapacity, stateHandlerCloseTimeout)
	if err := b.AddHandler(h); err != nil {
		return nil, errors.Join(err, h.Close())
	}
	s.broadcasts[r.Status.SessionID] = &sessionBroadcast{broadcast: b, runtimeName: r.Spec.RuntimeRef}
	return b, nil
}
