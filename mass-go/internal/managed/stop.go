package managed

import (
	"context"
	"errors"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// StopWith closes admission synchronously. A single cleanup owner continues
// after a caller deadline; callers never acquire that owner's native resources.
func (r *Runtime) StopWith(ctx context.Context, options rt.StopOptions) (rt.StopReceipt, error) {
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 60*time.Second {
		timeout := rt.DefaultStopTimeout
		if ok {
			timeout = 60 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	receipt := rt.StopReceipt{RuntimeName: r.Status().Name, OwnedResources: rt.UnknownObservation(), Outcome: "rejected"}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if options.Policy != rt.StopAbort && options.Policy != rt.StopDrain {
		return receipt, rt.ErrInvalidInput
	}
	r.mu.Lock()
	a := r.stopping
	if a == nil {
		a = &attempt{done: make(chan struct{}), sessions: append(append([]rt.Session(nil), r.retiredSessions...), r.sessions...)}
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
		go r.finishStop(a, activation)
	}
	r.mu.Unlock()
	receipt.AdmissionClosed = true
	select {
	case <-a.done:
		receipt.Outcome = "stopped"
		if a.err == nil {
			receipt.OwnedResources = rt.Known(true)
		}
	case <-ctx.Done():
		receipt.Outcome = "stop_unknown"
		receipt.IncompleteFacts = true
		for _, session := range a.sessions {
			if session.Snapshot().Attachment != "closed" {
				receipt.RemainingSessions++
			}
		}
		r.mu.Lock()
		for _, op := range r.operations {
			if op.snapshot.Phase != "closed" && op.snapshot.Phase != "opened" {
				receipt.RemainingOperations++
			}
		}
		r.mu.Unlock()
		a.abandoned.Do(func() {
			go func() {
				<-a.done
				for _, session := range a.sessions {
					if s, ok := session.(*publishedSession); ok {
						s.discard()
					}
				}
			}()
		})
		return receipt, ctx.Err()
	}
	r.mu.Lock()
	sessions := a.sessions
	for _, op := range r.operations {
		if op.snapshot.Phase != "closed" && op.snapshot.Phase != "opened" {
			receipt.RemainingOperations++
			receipt.IncompleteFacts = true
		}
	}
	r.mu.Unlock()
	var err error = a.err
	for _, session := range sessions {
		if s, ok := session.(*publishedSession); ok {
			s.mu.Lock()
			receipt.IncompleteFacts = receipt.IncompleteFacts || s.incomplete
			s.mu.Unlock()
			if options.Policy == rt.StopDrain {
				if e := s.drain(ctx); e != nil {
					err = errors.Join(err, e)
				}
			}
			if options.Policy == rt.StopAbort || ctx.Err() != nil {
				s.discard()
				receipt.IncompleteFacts = true
			}
		}
	}
	if err != nil {
		receipt.Outcome = "stop_unknown"
	}
	return receipt, err
}
