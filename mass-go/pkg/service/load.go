package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type historyLoad struct {
	mu             sync.Mutex
	id             string
	status         resource.HistoryLoadStatus
	complete       atomic.Bool
	bound          bool // guarded by Service.mu; only the first legal marker binds the run
	nativeTerminal bool // guarded by historyLoad.mu
	cleanupStarted bool
}

func (s *Service) LoadSession(ctx context.Context, input resource.SessionLoad) (resource.SessionLoadAccepted, error) {
	if err := ctx.Err(); err != nil {
		return resource.SessionLoadAccepted{}, err
	}
	m := input.Metadata
	if err := validateMetadata(m, true); err != nil {
		return resource.SessionLoadAccepted{}, err
	}
	if uuidSessionName.MatchString(m.Name) || m.UID != "" || m.Generation != 0 || m.ResourceVersion != "" || !m.CreatedAt.IsZero() || !validName(input.RuntimeRef) || !filepath.IsAbs(input.Cwd) {
		return resource.SessionLoadAccepted{}, runtime.InvalidArgument("invalid load metadata, runtime or absolute cwd")
	}
	if err := resource.ValidateSessionID(input.SessionID); err != nil {
		return resource.SessionLoadAccepted{}, err
	}
	s.mu.Lock()
	run := s.runs[input.RuntimeRef]
	s.mu.Unlock()
	if run == nil {
		return resource.SessionLoadAccepted{}, runtime.FailedPrecondition("runtime not started")
	}
	run.admission.RLock()
	defer run.admission.RUnlock()
	// MetaStore atomically reserves both the name and global ID before production.
	// No native resources exist yet, so an uncertain write is never reissued.
	s.mu.Lock()
	if s.closing || run.stopping || run.err != nil || !run.started || len(run.lanes)+len(run.pending) >= queueCapacity {
		s.mu.Unlock()
		return resource.SessionLoadAccepted{}, runtime.FailedPrecondition("runtime unavailable")
	}
	s.mu.Unlock()
	page, err := s.history.ListHistory(input.SessionID, store.HistoryQuery{Limit: 1})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return resource.SessionLoadAccepted{}, err
	}
	if len(page.Events) != 0 || page.Next != "" {
		return resource.SessionLoadAccepted{}, store.ErrConflict
	}
	load := &historyLoad{id: uuid.NewString()}
	load.status = resource.HistoryLoadStatus{LoadID: load.id, Phase: "accepted"}
	initial := load.status
	r := resource.Session{Metadata: m, Spec: resource.SessionSpec{RuntimeRef: input.RuntimeRef, SessionSpec: runtime.SessionSpec{Cwd: input.Cwd}}, Status: resource.SessionStatus{SessionID: input.SessionID, State: "opening", RuntimeGeneration: run.generation, HistoryLoad: &initial}}
	r, err = s.meta.CreateSession(r)
	if err != nil {
		return resource.SessionLoadAccepted{}, err
	}
	l := &lane{session: r, load: load, queue: make(chan delivery, laneRetentionCapacity), retry: make(chan chan error), done: make(chan struct{})}
	s.mu.Lock()
	run.lanes[input.SessionID] = l
	s.mu.Unlock()
	go s.process(run, l)
	go func() {
		if err := run.driver.LoadSession(context.WithoutCancel(ctx), input.SessionID, runtime.Address{Namespace: m.Namespace, SessionName: m.Name}, input.Cwd, load.id); err != nil {
			s.failLoad(run, l, "failed", err)
		}
	}()
	return resource.SessionLoadAccepted{Session: r, LoadID: load.id}, nil
}

// All load status transitions serialize with cancellation, including the durable
// completion write. Caller cancellation never revokes a background owner.
func (s *Service) writeLoad(l *lane) error {
	status := l.load.status
	_, err := s.sessionStatus(resource.SessionRef{SessionID: l.session.Status.SessionID}, func(r *resource.Session) {
		r.Status.HistoryLoad = &status
		if status.Phase == "completed" {
			r.Status.State = events.Unknown
			r.Status.AppliedGeneration = r.Metadata.Generation
			r.Status.RuntimeGeneration = l.session.Status.RuntimeGeneration
		} else {
			r.Status.State = "opening"
		}
	})
	return err
}
func loadError(err error) *runtime.OperationError {
	var public *runtime.OperationError
	if errors.As(err, &public) {
		v := *public
		return &v
	}
	return &runtime.OperationError{Code: "unavailable", Reason: "history_load_failed", Message: "History load did not settle; original ownership is retained"}
}
func (s *Service) failLoad(run *runBinding, l *lane, phase string, cause error) {
	h := l.load
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.complete.Load() {
		h.mu.Unlock()
		return
	}
	if h.status.Error == nil {
		h.status.Phase = phase
		h.status.Error = loadError(cause)
	}
	_ = s.writeLoad(l)
	launch := !h.cleanupStarted
	h.cleanupStarted = true
	h.mu.Unlock()
	if launch {
		go func() {
			err := run.driver.CancelHistoryLoad(context.Background(), l.session.Status.SessionID, h.id)
			h.mu.Lock()
			defer h.mu.Unlock()
			if err == nil {
				h.status.CleanupCompleted = true
			}
			_ = s.writeLoad(l)
		}()
	}
}
func (s *Service) CancelHistoryLoad(ctx context.Context, ref resource.SessionRef, id string) (resource.HistoryLoadStatus, error) {
	if err := ctx.Err(); err != nil {
		return resource.HistoryLoadStatus{}, err
	}
	r, err := s.GetSession(ref)
	if err != nil {
		return resource.HistoryLoadStatus{}, err
	}
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	var l *lane
	if run != nil {
		l = run.lanes[r.Status.SessionID]
	}
	s.mu.Unlock()
	if id == "" || l == nil || l.load == nil || l.load.id != id {
		return resource.HistoryLoadStatus{}, runtime.FailedPrecondition("original load unavailable")
	}
	h := l.load
	h.mu.Lock()
	if h.complete.Load() {
		h.mu.Unlock()
		return resource.HistoryLoadStatus{}, runtime.FailedPrecondition("history load already completed")
	}
	h.mu.Unlock()
	s.failLoad(run, l, "canceled", &runtime.OperationError{Code: "failed_precondition", Reason: "history_load_canceled", Message: "History load canceled by its owner"})
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, nil
}

func (s *Service) loadMarker(run *runBinding, l *lane, e events.SessionEvent) error {
	h := l.load
	if h == nil {
		return runtime.FailedPrecondition("unbound history marker")
	}
	h.mu.Lock()
	p := e.Payload().(events.HistoryReplay)
	var err error
	if p.LoadID != h.id || h.nativeTerminal {
		err = runtime.FailedPrecondition("history marker order or load identity mismatch")
	} else {
		switch p.Phase {
		case "started":
			if h.status.NativeResumed {
				err = runtime.FailedPrecondition("duplicate history start")
				break
			}
			h.status.NativeVerified = true
			h.status.NativeResumed = true
			if h.status.Error == nil {
				h.status.Phase = "replaying"
			}
		case "native_completed":
			if !h.status.NativeResumed {
				err = runtime.FailedPrecondition("history completion before start")
				break
			}
			h.nativeTerminal = true
			h.status.NativeCompleted = true
			if h.status.Error == nil {
				s.mu.Lock()
				healthy := run.err == nil && l.err == nil && !l.closing && !run.stopping
				s.mu.Unlock()
				if !healthy {
					err = runtime.FailedPrecondition("recovered binding unhealthy")
					break
				}
				h.status.Phase = "completed"
				h.status.DeliveryCompleted = true
			}
		case "failed", "canceled":
			h.nativeTerminal = true
			if h.status.Error == nil {
				h.status.Phase = p.Phase
				h.status.Error = &runtime.OperationError{Code: "unavailable", Reason: "native_history_failed", Message: "Native history read did not complete"}
			}
		}
		if err == nil {
			err = s.writeLoad(l)
		}
		if err == nil && h.status.Phase == "completed" {
			h.complete.Store(true)
		}
	}
	failed := h.status.Error != nil
	h.mu.Unlock()
	if err != nil {
		s.failLoad(run, l, "failed", err)
	} else if failed {
		s.failLoad(run, l, "failed", errors.New("native history failed"))
	}
	return err
}
