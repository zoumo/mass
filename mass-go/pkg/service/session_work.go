package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type sessionResumeRequiredError struct {
	state events.SessionState
}

func (e *sessionResumeRequiredError) Error() string {
	return fmt.Sprintf("conflict: session unavailable in state %q; explicitly resume before dispatching work", e.state)
}

func (e *sessionResumeRequiredError) Unwrap() []error {
	return []error{
		runtime.ErrFailedPrecondition,
		&runtime.OperationError{
			Code:    "conflict",
			Reason:  "session_resume_required",
			Message: fmt.Sprintf("Session is unavailable in state %q; explicitly resume before dispatching work", e.state),
		},
	}
}

func sessionResumeRequired(state events.SessionState) error {
	return &sessionResumeRequiredError{state: state}
}

// Admission pins the resolved identity and original run until dispatch settles.
// Caller cancellation ends only the wait; the owner retains the admission gate.
func sessionWork[T any](ctx context.Context, s *Service, ref resource.SessionRef, canceling, rearmIdle bool, dispatch func(context.Context, *runBinding, string) (T, error)) (T, error) {
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
		value, err := func() (T, error) {
			defer run.admission.RUnlock()
			defer l.admission.RUnlock()
			return dispatch(context.WithoutCancel(ctx), run, r.Status.SessionID)
		}()
		if rearmIdle {
			s.rearmIdleAfterActivity(run, l)
		}
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

// Both input operations share an original-binding gate. Rejected overlap is
// never queued, and an uncertain native dispatch seals further input.
func sessionInputWork[T any](ctx context.Context, s *Service, ref resource.SessionRef, rearmIdle bool, dispatch func(context.Context, *runBinding, string) (T, error)) (T, error) {
	return sessionWork(ctx, s, ref, false, rearmIdle, func(owner context.Context, run *runBinding, id string) (T, error) {
		var zero T
		s.mu.Lock()
		l := run.lanes[id]
		source := l.source
		s.mu.Unlock()
		if !l.input.TryLock() {
			return zero, &runtime.OperationError{Code: "conflict", Reason: "input_in_flight", Message: "Another input is in flight; input was not sent"}
		}
		defer l.input.Unlock()
		// Resume may reuse the delivery lane for history continuity. Its new
		// native source has independent input admission after verified close.
		if source.SameRun(l.unknownInputSource) {
			return zero, &runtime.OperationError{Code: "conflict", Reason: "input_outcome_unknown", Message: "Previous input outcome is unknown; reconcile before sending input"}
		}
		result, err := dispatch(owner, run, id)
		var problem *runtime.OperationError
		if errors.As(err, &problem) && problem.Code == "outcome_unknown" {
			l.unknownInputSource = source
		}
		return result, err
	})
}

// Inject appends context only to the currently bound native Session.
func (s *Service) Inject(ctx context.Context, ref resource.SessionRef, input runtime.Input) (runtime.InjectReceipt, error) {
	notSent := runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}}
	copied, err := runtime.CopyInput(input)
	if err != nil {
		return notSent, err
	}
	type dispatchResult struct {
		receipt    runtime.InjectReceipt
		dispatched bool
	}
	result, err := sessionInputWork(ctx, s, ref, true, func(owner context.Context, run *runBinding, id string) (dispatchResult, error) {
		driver, ok := run.driver.(runtime.InjectDriver)
		if !ok {
			return dispatchResult{notSent, true}, runtime.UnsupportedOption("inject_unsupported", "Driver does not support context injection")
		}
		receipt, err := driver.Inject(owner, id, copied)
		return dispatchResult{receipt, true}, err
	})
	// Caller wait loss has no dispatch receipt and must retain unknown evidence.
	var problem *runtime.OperationError
	if !result.dispatched && errors.As(err, &problem) && problem.Code == "outcome_unknown" {
		return runtime.InjectReceipt{}, err
	}
	if !result.dispatched {
		return notSent, err
	}
	return result.receipt, err
}
