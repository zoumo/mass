package service

import (
	"context"
	"errors"
	"slices"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

var (
	errPromptDispatchDeferred = errors.New("prompt dispatch deferred until the next idle observation")
	errPromptNotDispatched    = errors.New("prompt was not dispatched")
)

type promptQueue struct {
	pending          []*queuedPrompt
	dispatching      bool
	idleVersion      uint64
	attemptedVersion uint64
	stopErr          error
}

type queuedPrompt struct {
	input         runtime.Input
	done          chan promptOutcome
	nativeStarted bool
	abandoned     bool
	finished      bool
	delivered     bool
}

type promptOutcome struct {
	receipt runtime.InputReceipt
	err     error
}

func validatePromptInput(driver runtime.Driver, input runtime.Input) error {
	if validator, ok := driver.(interface {
		ValidateInitialInput(runtime.Input) error
	}); ok {
		return validator.ValidateInitialInput(input)
	}
	return runtime.ValidateTextInput(input)
}

func (s *Service) admitPrompt(r resource.Session, input runtime.Input) (*runBinding, *lane, *promptQueue, *queuedPrompt, error) {
	s.mu.Lock()
	run := s.runs[r.Spec.RuntimeRef]
	if run == nil {
		s.mu.Unlock()
		return nil, nil, nil, nil, sessionResumeRequired(r.Status.State)
	}
	if _, err := s.promptLaneLocked(r, run); err != nil {
		s.mu.Unlock()
		return nil, nil, nil, nil, err
	}
	driver := run.driver
	s.mu.Unlock()

	if err := validatePromptInput(driver, input); err != nil {
		return nil, nil, nil, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	run = s.runs[r.Spec.RuntimeRef]
	if run == nil {
		return nil, nil, nil, nil, sessionResumeRequired(r.Status.State)
	}
	l, err := s.promptLaneLocked(r, run)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	item, err := s.appendPromptLocked(l, input)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return run, l, l.prompts, item, nil
}

// Caller holds s.mu.
func (s *Service) promptLaneLocked(r resource.Session, run *runBinding) (*lane, error) {
	if !run.started || run.stopping || run.err != nil || s.closing {
		return nil, sessionResumeRequired(r.Status.State)
	}
	l := run.lanes[r.Status.SessionID]
	key := addressKey(runtime.Address{Namespace: r.Metadata.Namespace, SessionName: r.Metadata.Name})
	switch {
	case l == nil:
		return nil, sessionResumeRequired(r.Status.State)
	case s.sessionCloseAcceptedLocked(r.Status.SessionID):
		return nil, sessionResumeRequired(r.Status.State)
	case l.closing || l.releasing:
		return nil, sessionResumeRequired(r.Status.State)
	case l.overloaded:
		return nil, runtime.Exhausted(sessionDeliveryOverloaded)
	case l.err != nil:
		return nil, sessionResumeRequired(r.Status.State)
	case run.pending[key] != nil:
		return nil, sessionResumeRequired(r.Status.State)
	case l.load != nil && !l.load.complete.Load():
		return nil, runtime.FailedPrecondition("session history load is not complete")
	case l.stateObserved && l.observedState == events.Unknown:
		return nil, sessionResumeRequired(events.Unknown)
	case l.prompts != nil && l.prompts.stopErr != nil:
		return nil, l.prompts.stopErr
	}
	return l, nil
}

// Caller holds s.mu so initial input is admitted before the live lane becomes
// visible to concurrent SendPrompt callers.
func (s *Service) appendPromptLocked(l *lane, input runtime.Input) (*queuedPrompt, error) {
	if l.prompts == nil {
		l.prompts = &promptQueue{}
	}
	if l.prompts.stopErr != nil {
		return nil, l.prompts.stopErr
	}
	if len(l.prompts.pending) >= queueCapacity {
		return nil, runtime.Exhausted(sessionPromptQueueFull)
	}
	item := &queuedPrompt{input: input, done: make(chan promptOutcome, 1)}
	l.prompts.pending = append(l.prompts.pending, item)
	s.invalidateIdleLocked(l)
	return item, nil
}

func (s *Service) awaitPrompt(ctx context.Context, run *runBinding, l *lane, q *promptQueue, item *queuedPrompt) (runtime.InputReceipt, error) {
	select {
	case outcome := <-item.done:
		return outcome.receipt, outcome.err
	case <-ctx.Done():
	}

	s.mu.Lock()
	if item.finished || item.delivered {
		s.mu.Unlock()
		select {
		case outcome := <-item.done:
			return outcome.receipt, outcome.err
		default:
		}
	} else if !item.nativeStarted {
		item.finished = true
		removeQueuedPromptLocked(q, item)
		s.mu.Unlock()
		s.pumpPromptQueue(run, l)
		return runtime.InputReceipt{}, ctx.Err()
	} else {
		item.abandoned = true
		s.mu.Unlock()
	}
	return runtime.InputReceipt{}, &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "work_wait_abandoned",
		Message: "Operation remains owned by its original Session; do not resend",
	}
}

func (s *Service) awaitAdmittedPrompt(ctx context.Context, run *runBinding, l *lane, q *promptQueue, item *queuedPrompt) (runtime.InputReceipt, error) {
	if err := ctx.Err(); err == nil {
		s.pumpPromptQueue(run, l)
	}
	return s.awaitPrompt(ctx, run, l, q, item)
}

func (s *Service) pumpPromptQueue(run *runBinding, l *lane) {
	s.mu.Lock()
	q := l.prompts
	if q == nil || q.dispatching || q.stopErr != nil || len(q.pending) == 0 {
		s.mu.Unlock()
		return
	}
	item := q.pending[0]
	if item.finished {
		s.mu.Unlock()
		return
	}
	if s.runs[l.session.Spec.RuntimeRef] != run ||
		run.lanes[l.session.Status.SessionID] != l ||
		s.closing ||
		run.stopping ||
		run.err != nil ||
		l.closing ||
		l.releasing ||
		l.err != nil ||
		s.sessionCloseAcceptedLocked(l.session.Status.SessionID) ||
		!l.stateObserved ||
		l.observedState != events.Idle ||
		q.idleVersion <= q.attemptedVersion {
		s.mu.Unlock()
		return
	}
	q.dispatching = true
	q.attemptedVersion = q.idleVersion
	s.mu.Unlock()

	go s.dispatchQueuedPrompt(run, l, q, item)
}

func (s *Service) dispatchQueuedPrompt(run *runBinding, l *lane, q *promptQueue, item *queuedPrompt) {
	receipt, err := sessionWork(context.Background(), s, resource.SessionRef{SessionID: l.session.Status.SessionID}, false, func(owner context.Context, _ *runBinding, id string) (runtime.InputReceipt, error) {
		s.mu.Lock()
		switch {
		case l.prompts != q:
			s.mu.Unlock()
			return runtime.InputReceipt{}, errPromptNotDispatched
		case item.finished:
			s.mu.Unlock()
			return runtime.InputReceipt{}, errPromptNotDispatched
		case q.stopErr != nil:
			stopErr := q.stopErr
			s.mu.Unlock()
			return runtime.InputReceipt{}, &promptNotDispatchedError{cause: stopErr}
		case !l.stateObserved || l.observedState != events.Idle:
			s.mu.Unlock()
			return runtime.InputReceipt{}, errPromptDispatchDeferred
		default:
			item.nativeStarted = true
			s.mu.Unlock()
			return run.driver.SendPrompt(owner, id, item.input)
		}
	})
	s.finishPromptDispatch(run, l, q, item, receipt, err)
}

func (s *Service) finishPromptDispatch(run *runBinding, l *lane, q *promptQueue, item *queuedPrompt, receipt runtime.InputReceipt, dispatchErr error) {
	s.mu.Lock()
	q.dispatching = false
	if item.nativeStarted {
		item.nativeStarted = false
	}
	if item.finished || !queuedPromptPresent(q, item) {
		s.mu.Unlock()
		s.pumpPromptQueue(run, l)
		return
	}

	var notDispatched *promptNotDispatchedError
	switch {
	case errors.Is(dispatchErr, errPromptDispatchDeferred):
		// A newer non-idle observation arrived before the native call. Keep the
		// input at the head and wait for another real idle event.
	case errors.As(dispatchErr, &notDispatched):
		s.finishPromptLocked(q, item, promptOutcome{err: promptError(notDispatched)})
	case dispatchErr != nil:
		if promptOutcomeUnknown(dispatchErr) {
			s.finishPromptLocked(q, item, promptOutcome{err: dispatchErr})
			stopPromptQueueLocked(q, dispatchErr)
		} else {
			s.finishPromptLocked(q, item, promptOutcome{err: dispatchErr})
		}
	default:
		switch receipt.Result {
		case agent.InputAppliedCurrentWork, agent.InputAppliedNewWork, agent.InputAppliedBestEffort:
			s.finishPromptLocked(q, item, promptOutcome{receipt: receipt})
		case agent.InputNotApplied:
			// A known non-application stays at the FIFO head. Wake the caller
			// once, but require a fresh idle observation before retrying it.
			switch {
			case q.stopErr != nil:
				s.finishPromptLocked(q, item, promptOutcome{receipt: receipt})
			case item.abandoned:
				s.finishPromptLocked(q, item, promptOutcome{})
			default:
				notifyPromptLocked(item, promptOutcome{receipt: receipt})
			}
		default:
			outcomeErr := &runtime.OperationError{
				Code:    "outcome_unknown",
				Reason:  "prompt_result_unrecognized",
				Message: "Input dispatch returned an unrecognized result; do not resend",
			}
			s.finishPromptLocked(q, item, promptOutcome{err: outcomeErr})
			stopPromptQueueLocked(q, outcomeErr)
		}
	}
	s.mu.Unlock()
	s.pumpPromptQueue(run, l)
}

type promptNotDispatchedError struct {
	cause error
}

func (e *promptNotDispatchedError) Error() string {
	if e.cause == nil {
		return errPromptNotDispatched.Error()
	}
	return errPromptNotDispatched.Error() + ": " + e.cause.Error()
}

func (e *promptNotDispatchedError) Unwrap() []error {
	if e.cause == nil {
		return []error{errPromptNotDispatched}
	}
	return []error{errPromptNotDispatched, e.cause}
}

func promptError(err *promptNotDispatchedError) error {
	if err.cause != nil {
		return err.cause
	}
	return errPromptNotDispatched
}

func promptOutcomeUnknown(err error) bool {
	var operation *runtime.OperationError
	return errors.As(err, &operation) && operation.Code == "outcome_unknown"
}

func (s *Service) observePromptStateLocked(l *lane, state events.SessionState) {
	q := l.prompts
	if q == nil || q.stopErr != nil {
		return
	}
	switch state {
	case events.Idle:
		q.idleVersion++
	case events.Unknown:
		stopPromptQueueLocked(q, runtime.FailedPrecondition("session state is unknown; queued input was not dispatched"))
	}
}

// Caller holds s.mu. Only inputs that have not crossed the native call boundary
// are settled here; an in-flight SDK call settles from its own receipt.
func stopPromptQueueLocked(q *promptQueue, cause error) {
	if q == nil {
		return
	}
	if q.stopErr == nil {
		q.stopErr = cause
	}
	kept := make([]*queuedPrompt, 0, len(q.pending))
	for _, item := range q.pending {
		if item.finished {
			continue
		}
		if item.nativeStarted {
			kept = append(kept, item)
			continue
		}
		item.finished = true
		notifyPromptLocked(item, promptOutcome{err: q.stopErr})
	}
	q.pending = kept
}

// Caller holds s.mu.
func (s *Service) finishPromptLocked(q *promptQueue, item *queuedPrompt, outcome promptOutcome) {
	if item.finished {
		return
	}
	item.finished = true
	removeQueuedPromptLocked(q, item)
	notifyPromptLocked(item, outcome)
}

// Caller holds s.mu.
func notifyPromptLocked(item *queuedPrompt, outcome promptOutcome) {
	if item.delivered {
		return
	}
	item.delivered = true
	item.done <- outcome
	close(item.done)
}

// Caller holds s.mu.
func removeQueuedPromptLocked(q *promptQueue, target *queuedPrompt) {
	for i, item := range q.pending {
		if item != target {
			continue
		}
		copy(q.pending[i:], q.pending[i+1:])
		q.pending[len(q.pending)-1] = nil
		q.pending = q.pending[:len(q.pending)-1]
		return
	}
}

// Caller holds s.mu.
func queuedPromptPresent(q *promptQueue, target *queuedPrompt) bool {
	return slices.Contains(q.pending, target)
}
