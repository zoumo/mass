package service

import (
	"context"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Admission pins the resolved identity and original run until dispatch settles.
// Caller cancellation ends only the wait; the owner retains the admission gate.
func sessionWork[T any](ctx context.Context, s *Service, ref resource.SessionRef, canceling bool, dispatch func(context.Context, *runBinding, string) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	r, err := s.GetSession(ref)
	if err != nil {
		return zero, err
	}
	run, l, err := s.controlWork(r, canceling)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		l.admission.RUnlock()
		run.admission.RUnlock()
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer run.admission.RUnlock()
		defer l.admission.RUnlock()
		value, err := dispatch(context.WithoutCancel(ctx), run, r.Status.SessionID)
		done <- result{value, err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		select {
		case result := <-done:
			return result.value, result.err
		default:
		}
		return zero, &runtime.OperationError{Code: "outcome_unknown", Reason: "work_wait_abandoned", Message: "Operation remains owned by its original run; do not resend"}
	}
}
