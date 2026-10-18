// Package managed coordinates protocol-owned Runtime lifecycles. I/O is always
// performed outside the lifecycle lock.
package managed

import (
	"context"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Driver interface {
	Start(context.Context) (rt.Health, error)
	CreateSession(context.Context) (rt.Session, error)
	Stop() error
}
type attempt struct {
	done chan struct{}
	err  error
}
type Runtime struct {
	mu         sync.Mutex
	driver     Driver
	snapshot   rt.Snapshot
	activation *attempt
	stopping   *attempt
	sessions   []rt.Session
	creates    sync.WaitGroup
	retired    bool
	unsettled  bool
}

func New(name, kind string, revision uint64, driver Driver) *Runtime {
	now := time.Now().UTC()
	return &Runtime{driver: driver, snapshot: rt.Snapshot{Name: name, Type: kind, ConfigRevision: revision, Phase: "registered", ObservedAt: now, Health: rt.Health{Configured: rt.Known(true), Connected: rt.UnknownObservation(), ProtocolResponsive: rt.UnknownObservation(), ProbeScope: "configuration", ObservedAt: now}}}
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
func (r *Runtime) CreateSession(ctx context.Context) (rt.Session, error) {
	r.mu.Lock()
	if r.snapshot.Phase != "ready" {
		r.mu.Unlock()
		return nil, rt.ErrUnavailable
	}
	r.creates.Add(1)
	r.mu.Unlock()
	defer r.creates.Done()
	session, err := r.driver.CreateSession(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.snapshot.Phase != "ready" {
		r.mu.Unlock()
		_ = session.Close()
		return nil, rt.ErrUnavailable
	}
	r.sessions = append(r.sessions, session)
	r.mu.Unlock()
	return session, nil
}
func (r *Runtime) GetSession(key rt.SessionKey) (rt.Session, error) {
	r.mu.Lock()
	sessions := append([]rt.Session(nil), r.sessions...)
	name := r.snapshot.Name
	r.mu.Unlock()
	if key.RuntimeName != name || key.SessionID == "" {
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
	activation := r.activation
	r.mu.Unlock()
	// Driver Stop interrupts outstanding SDK calls before waiting for creates.
	err := r.driver.Stop()
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
	if r.unsettled || len(r.sessions) > 0 || r.snapshot.Phase == "starting" || r.snapshot.Phase == "ready" || r.snapshot.Phase == "stopping" {
		return false
	}
	r.retired = true
	return true
}
