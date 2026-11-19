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

type sessionWorkOptions struct {
	input     bool
	canceling bool
	rearmIdle bool
}

// Admission pins the resolved identity and original run until dispatch settles.
// Caller cancellation ends only the wait after dispatch begins.
func sessionWork[T any](ctx context.Context, s *Service, ref resource.SessionRef, opts sessionWorkOptions, dispatch func(context.Context, *runBinding, string) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	r, err := s.GetSession(ref)
	if err != nil {
		return zero, err
	}
	run, l, err := s.controlWork(r, opts)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		// Admission paused the timer, but there is no native operation to
		// produce the next idle observation.
		opts.rearmIdle = true
		s.settleWork(run, l, opts, err)
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := dispatch(context.WithoutCancel(ctx), run, r.Status.SessionID)
		s.settleWork(run, l, opts, err)
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

func (s *Service) settleWork(run *runBinding, l *lane, opts sessionWorkOptions, err error) {
	var problem *runtime.OperationError
	uncertain := opts.input && errors.As(err, &problem) && problem.Code == "outcome_unknown"
	s.mu.Lock()
	if opts.input {
		if uncertain {
			l.unknownInputSource = l.source
		}
		l.inputInFlight = false
	}
	s.releaseLaneAdmissionLocked(run, l, opts.rearmIdle)
	s.mu.Unlock()
	run.admission.RUnlock()
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
	result, err := sessionWork(ctx, s, ref, sessionWorkOptions{input: true, rearmIdle: true}, func(owner context.Context, run *runBinding, id string) (dispatchResult, error) {
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
