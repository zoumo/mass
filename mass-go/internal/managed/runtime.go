// Package managed coordinates protocol-owned Runtime lifecycles. I/O is always
// performed outside the lifecycle lock.
package managed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Driver interface {
	Start(context.Context) (rt.Health, error)
	CreateSession(context.Context, rt.SessionOptions) (rt.Session, error)
	ResumeSession(context.Context, rt.SessionKey) (rt.Session, error)
	RequiresInitialInput() bool
	Stop() error
}
type attempt struct {
	done chan struct{}
	err  error
}
type creation struct {
	snapshot rt.CreationOperation
	canceled bool
}
type Runtime struct {
	bindings     rt.BindingStore
	scope        string
	resuming     map[rt.SessionKey]bool
	closing      map[rt.SessionKey]bool
	closeUnknown map[rt.SessionKey]bool
	operations   map[string]*creation
	limits       rt.CreationLimits
	mu           sync.Mutex
	driver       Driver
	snapshot     rt.Snapshot
	activation   *attempt
	stopping     *attempt
	sessions     []rt.Session
	creates      sync.WaitGroup
	retired      bool
	unsettled    bool
}

func New(name, kind string, revision uint64, driver Driver, limits rt.CreationLimits, config rt.Config, bindings rt.BindingStore) *Runtime {
	now := time.Now().UTC()
	return &Runtime{bindings: bindings, scope: recoveryScope(name, kind, config), resuming: map[rt.SessionKey]bool{}, closing: map[rt.SessionKey]bool{}, closeUnknown: map[rt.SessionKey]bool{}, operations: make(map[string]*creation), limits: limits.Effective(), driver: driver, snapshot: rt.Snapshot{Name: name, Type: kind, ConfigRevision: revision, Phase: "registered", ObservedAt: now, Health: rt.Health{Configured: rt.Known(true), Connected: rt.UnknownObservation(), ProtocolResponsive: rt.UnknownObservation(), ProbeScope: "configuration", ObservedAt: now}}}
}
func (r *Runtime) Status() rt.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.snapshot
	for _, observation := range []*rt.Observation{&snapshot.Health.Configured, &snapshot.Health.Connected, &snapshot.Health.ProtocolResponsive} {
		if observation.Value != nil {
			value := *observation.Value
			observation.Value = &value
		}
	}
	return snapshot
}
func (r *Runtime) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.retired || r.unsettled {
		r.mu.Unlock()
		return rt.ErrConflict
	}
	if r.snapshot.Phase == "ready" {
		r.mu.Unlock()
		return nil
	}
	if r.snapshot.Phase == "stopping" {
		r.mu.Unlock()
		return rt.ErrConflict
	}
	if a := r.activation; a != nil {
		r.mu.Unlock()
		select {
		case <-a.done:
			return a.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a := &attempt{done: make(chan struct{})}
	r.activation = a
	r.snapshot.Phase = "starting"
	r.mu.Unlock()
	health, err := r.driver.Start(ctx)
	r.mu.Lock()
	a.err = err
	if r.snapshot.Phase != "stopping" {
		r.snapshot.Phase = "ready"
		if err != nil {
			r.snapshot.Phase = "unavailable"
		} else {
			r.snapshot.ObservedRevision = r.snapshot.ConfigRevision
			r.snapshot.Health = health
		}
	}
	r.snapshot.ObservedAt = time.Now().UTC()
	r.activation = nil
	close(a.done)
	r.mu.Unlock()
	return err
}

// CreateSession reserves ownership before starting native I/O. The caller context
// controls admission only; GetOperation observes the independently owned result.
func (r *Runtime) CreateSession(ctx context.Context, options rt.SessionOptions) (rt.CreationOperation, error) {
	if err := ctx.Err(); err != nil {
		return rt.CreationOperation{}, err
	}
	if options.InitialInput != nil {
		input := *options.InitialInput
		options.InitialInput = &input
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot.Phase != "ready" {
		return rt.CreationOperation{}, rt.ErrUnavailable
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return rt.CreationOperation{}, rt.ErrUnavailable
	}
	ref := "create:" + hex.EncodeToString(token[:])
	op := &creation{snapshot: rt.CreationOperation{OperationRef: ref, RuntimeName: r.snapshot.Name, Phase: "preparing", CleanupState: "not_required", Evidence: rt.CreationEvidence{NativeIdentity: rt.UnknownObservation(), InitialInputWritten: rt.UnknownObservation()}}}
	if !r.driver.RequiresInitialInput() && options.InitialInput == nil {
		op.snapshot.Evidence.InitialInputWritten = rt.Observation{State: "not_applicable"}
	}
	r.operations[ref] = op
	if r.driver.RequiresInitialInput() && options.InitialInput == nil {
		op.snapshot.Phase = "awaiting_input"
		op.snapshot.InputRequired = true
	} else {
		r.launchCreation(op, options)
	}
	return copyOperation(op.snapshot), nil
}
func copyOperation(op rt.CreationOperation) rt.CreationOperation {
	if op.Key != nil {
		key := *op.Key
		op.Key = &key
	}
	for _, observation := range []*rt.Observation{&op.Evidence.NativeIdentity, &op.Evidence.InitialInputWritten} {
		if observation.Value != nil {
			value := *observation.Value
			observation.Value = &value
		}
	}
	return op
}
func (r *Runtime) GetOperation(ref string) (rt.CreationOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op := r.operations[ref]
	if op == nil {
		return rt.CreationOperation{}, rt.ErrNotFound
	}
	return copyOperation(op.snapshot), nil
}
func (r *Runtime) ContinueCreation(ctx context.Context, ref, input string) (rt.CreationOperation, error) {
	if err := ctx.Err(); err != nil {
		return rt.CreationOperation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	op := r.operations[ref]
	if op == nil {
		return rt.CreationOperation{}, rt.ErrNotFound
	}
	if r.snapshot.Phase != "ready" || op.snapshot.Phase != "awaiting_input" {
		return rt.CreationOperation{}, rt.ErrConflict
	}
	op.snapshot.InputRequired = false
	r.launchCreation(op, rt.SessionOptions{InitialInput: &input})
	return copyOperation(op.snapshot), nil
}
func (r *Runtime) CancelOperation(ctx context.Context, ref string) (rt.CreationOperation, error) {
	if err := ctx.Err(); err != nil {
		return rt.CreationOperation{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	op := r.operations[ref]
	if op == nil {
		return rt.CreationOperation{}, rt.ErrNotFound
	}
	switch op.snapshot.Phase {
	case "awaiting_input":
		op.canceled = true
		op.snapshot.Phase = "closed"
		op.snapshot.InputRequired = false
		op.snapshot.CleanupState = "local_resources_released"
	case "preparing", "awaiting_native_identity", "cleaning_up":
		op.canceled = true
		op.snapshot.Phase = "cleaning_up"
		op.snapshot.CleanupState = "pending"
	default:
		return copyOperation(op.snapshot), rt.ErrConflict
	}
	return copyOperation(op.snapshot), nil
}

// launchCreation is called under mu, but performs all SDK work outside it.
func (r *Runtime) launchCreation(op *creation, options rt.SessionOptions) {
	op.snapshot.Phase = "awaiting_native_identity"
	r.creates.Add(1)
	go func() {
		defer r.creates.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.limits.Timeout)
		defer cancel()
		session, err := r.driver.CreateSession(ctx, options)
		if err == nil {
			if saveErr := r.saveBinding(ctx, session.Key()); saveErr != nil {
				err = saveErr
				if closeErr := session.Close(); closeErr != nil {
					err = errors.Join(err, rt.ErrCleanupIncomplete)
				}
			}
		}
		r.mu.Lock()
		if err != nil {
			op.snapshot.Phase = "outcome_unknown"
			op.snapshot.Failure = "outcome_unknown"
			if errors.Is(err, rt.ErrIncomplete) {
				op.snapshot.Failure = "incomplete_stream"
			}
			op.snapshot.CleanupState = "local_resources_released_native_unconfirmed"
			if errors.Is(err, rt.ErrCleanupIncomplete) {
				op.snapshot.CleanupState = "unconfirmed"
			}
			r.mu.Unlock()
			return
		}
		key := session.Key()
		op.snapshot.Evidence.NativeIdentity = rt.Known(true)
		if options.InitialInput != nil {
			op.snapshot.Evidence.InitialInputWritten = rt.Known(true)
		}
		if r.resuming[key] {
			err = rt.ErrConflict
		}
		if key.SessionID == "" || key.RuntimeName != r.snapshot.Name {
			err = rt.ErrInvalidInput
		}
		for _, existing := range r.sessions {
			if existing.Key() == key {
				err = rt.ErrConflict
			}
		}
		if op.canceled || r.snapshot.Phase != "ready" || err != nil {
			op.snapshot.Phase = "cleaning_up"
			op.snapshot.CleanupState = "pending"
			r.mu.Unlock()
			closeErr := session.Close()
			r.mu.Lock()
			op.snapshot.Phase = "closed"
			op.snapshot.CleanupState = "local_resources_released_native_unconfirmed"
			if closeErr != nil {
				op.snapshot.CleanupState = "unconfirmed"
			}
			if err != nil || closeErr != nil {
				op.snapshot.Phase = "outcome_unknown"
				op.snapshot.Failure = "outcome_unknown"
			}
			r.mu.Unlock()
			return
		}
		r.sessions = append(r.sessions, session)
		op.snapshot.Key = &key
		op.snapshot.Phase = "opened"
		r.mu.Unlock()
	}()
}
func (r *Runtime) GetSession(key rt.SessionKey) (rt.Session, error) {
	r.mu.Lock()
	sessions := append([]rt.Session(nil), r.sessions...)
	name := r.snapshot.Name
	_, operationRef := r.operations[key.SessionID]
	r.mu.Unlock()
	if key.RuntimeName != name || key.SessionID == "" || operationRef {
		return nil, rt.ErrNotFound
	}
	for _, session := range sessions {
		if session.Key() == key {
			return session, nil
		}
	}
	return nil, rt.ErrNotFound
}
func (r *Runtime) ListSessions() []rt.SessionSnapshot {
	r.mu.Lock()
	sessions := append([]rt.Session(nil), r.sessions...)
	r.mu.Unlock()
	snapshots := make([]rt.SessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		snapshot := session.Snapshot()
		if snapshot.Key.SessionID != "" {
			snapshot.Freshness = "cached"
			if r.Status().Phase != "ready" {
				snapshot.Availability = "unavailable"
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots
}

func (r *Runtime) Stop() error {
	r.mu.Lock()
	if a := r.stopping; a != nil {
		r.mu.Unlock()
		<-a.done
		return a.err
	}
	if r.snapshot.Phase == "stopped" {
		r.mu.Unlock()
		return nil
	}
	a := &attempt{done: make(chan struct{})}
	r.stopping = a
	r.snapshot.Phase = "stopping"
	for _, op := range r.operations {
		if op.snapshot.Phase == "awaiting_input" {
			op.canceled = true
			op.snapshot.Phase = "closed"
			op.snapshot.InputRequired = false
			op.snapshot.CleanupState = "local_resources_released"
		}
	}
	activation := r.activation
	r.mu.Unlock()
	// Driver Stop interrupts outstanding SDK calls before waiting for creates.
	err := r.driver.Stop()
	r.mu.Lock()
	sessions := append([]rt.Session(nil), r.sessions...)
	r.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	if activation != nil {
		<-activation.done
		if second := r.driver.Stop(); err == nil {
			err = second
		}
	}
	r.creates.Wait()
	r.mu.Lock()
	a.err = err
	r.snapshot.Phase = "stopped"
	r.unsettled = err != nil
	if err != nil {
		r.snapshot.Phase = "unavailable"
		r.snapshot.Health.Connected = rt.UnknownObservation()
	} else {
		r.snapshot.Health.Connected = rt.Known(false)
	}
	r.snapshot.Health.ProtocolResponsive = rt.UnknownObservation()
	r.snapshot.Health.ProbeScope = "owned_resource_shutdown"
	r.snapshot.ObservedAt = time.Now().UTC()
	r.snapshot.Health.ObservedAt = r.snapshot.ObservedAt
	close(a.done)
	r.stopping = nil
	r.mu.Unlock()
	return err
}

// Retire atomically prevents old handles from activating after a catalog update.
// Retained closed sessions still own their original execution configuration.
func (r *Runtime) Retire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, op := range r.operations {
		if op.snapshot.Phase != "closed" && op.snapshot.Phase != "rejected" {
			return false
		}
	}
	if r.unsettled || len(r.resuming) > 0 || len(r.sessions) > 0 || r.snapshot.Phase == "starting" || r.snapshot.Phase == "ready" || r.snapshot.Phase == "stopping" {
		return false
	}
	r.retired = true
	return true
}
