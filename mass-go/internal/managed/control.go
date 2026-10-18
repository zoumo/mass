package managed

import (
	"context"
	"crypto/rand"
	"errors"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// NativeSession is adapter-owned. Native control parameters never reach callers.
type NativeSession interface {
	Key() rt.SessionKey
	Snapshot() rt.SessionSnapshot
	Capabilities() rt.Capabilities
	Submit(context.Context, string) (rt.Submission, error)
	Cancel(context.Context, string) error
	Next(context.Context) (rt.Event, error)
	Close() error
	Respond(context.Context, string, rt.Response) (rt.ResponseReceipt, error)
	InvalidateInteractions(string)
}
type command struct {
	receipt    rt.Submission
	submitting bool
	pending    *rt.Event
	finished   bool
	cancel     *rt.Cancellation
	cancelling bool
}

const maxControlRecords = 256

func NewSession(native NativeSession) rt.Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &publishedSession{native: native, epoch: rand.Text(), commands: map[string]*command{}, wake: make(chan struct{}), events: make(chan rt.Event, 64), finished: make(chan struct{}), ctx: ctx, cancel: cancel, foreground: "idle"}
	go s.receive()
	return s
}
func (s *publishedSession) Key() rt.SessionKey            { return s.native.Key() }
func (s *publishedSession) Capabilities() rt.Capabilities { return s.native.Capabilities() }
func (s *publishedSession) signal()                       { close(s.wake); s.wake = make(chan struct{}) }
func isKnown(o rt.Observation) bool                       { return o.State == "known" && o.Value != nil && *o.Value }
func copySubmission(r rt.Submission) rt.Submission {
	// Copy pointer-bearing public records, including safe failure evidence.
	for _, o := range []*rt.Observation{&r.HandedToSDK, &r.WriteCompleted, &r.NativeAcknowledged, &r.NativeRecorded, &r.Completed} {
		if o.Value != nil {
			v := *o.Value
			o.Value = &v
		}
	}
	r.Failure = copyFailure(r.Failure)
	return r
}
func copyFailure(e *rt.SafeError) *rt.SafeError {
	if e == nil {
		return nil
	}
	v := *e
	if v.Key != nil {
		k := *v.Key
		v.Key = &k
	}
	if v.Acceptance != nil {
		a := copySubmission(rt.Submission{SubmissionEvidence: *v.Acceptance})
		v.Acceptance = &a.SubmissionEvidence
	}
	return &v
}
func copyCancellation(r rt.Cancellation) rt.Cancellation {
	for _, o := range []*rt.Observation{&r.Requested, &r.NativeAcknowledged, &r.TargetFinished} {
		if o.Value != nil {
			v := *o.Value
			o.Value = &v
		}
	}
	r.Failure = copyFailure(r.Failure)
	return r
}
func failure(category, ref string, key rt.SessionKey) *rt.SafeError {
	return &rt.SafeError{Category: category, Scope: "operation", Message: "runtime command could not settle", OperationRef: ref, Key: &key, Recovery: "inspect original operation; do not automatically resend"}
}
func (s *publishedSession) GetSubmission(ref string) (rt.Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.commands[ref]
	if c == nil {
		return rt.Submission{}, rt.ErrNotFound
	}
	r := copySubmission(c.receipt)
	r.Key = s.Key()
	return r, nil
}
func (s *publishedSession) GetCancellation(ref string) (rt.Cancellation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.commands {
		if c.cancel != nil && c.cancel.OperationRef == ref {
			return copyCancellation(*c.cancel), nil
		}
	}
	return rt.Cancellation{}, rt.ErrNotFound
}
func (s *publishedSession) settle(c *command) {
	// A session-wide interrupt with an unknown ACK can still be unresolved at
	// the peer even after the old result. Keep new input fenced until detach;
	// fixed native-ID interrupts cannot be redirected to subsequent work.
	unknownSessionInterrupt := !s.Capabilities().NativeTurnID && c.cancel != nil && c.cancel.Outcome == "unknown" && !isKnown(c.cancel.NativeAcknowledged)
	if c.finished && !c.submitting && !c.cancelling && !unknownSessionInterrupt && s.active == c {
		s.active = nil
		s.foreground = "idle"
	}
	s.signal()
}
func (s *publishedSession) reject(category string) (rt.Submission, error) {
	r := rt.UnknownSubmission()
	r.Key = s.Key()
	r.Outcome = "rejected"
	r.HandedToSDK = rt.Known(false)
	r.WriteCompleted = rt.Known(false)
	r.Failure = failure(category, "", r.Key)
	evidence := r.SubmissionEvidence
	r.Failure.Acceptance = &evidence
	return r, r.Failure
}
func (s *publishedSession) Submit(ctx context.Context, input rt.InputCommand) (rt.Submission, error) {
	if err := ctx.Err(); err != nil {
		return rt.Submission{}, err
	}
	text, err := input.Text()
	if err != nil {
		category := "invalid_input"
		if errors.Is(err, rt.ErrUnsupported) {
			category = "unsupported_capability"
		}
		return s.reject(category)
	}
	s.mu.Lock()
	if s.closed || s.terminal != nil {
		s.mu.Unlock()
		return s.reject("unavailable")
	}
	if s.active != nil {
		s.mu.Unlock()
		return s.reject("busy")
	}
	if len(s.commands) >= maxControlRecords {
		s.mu.Unlock()
		return s.reject("backpressure")
	}
	ref := "submit:" + rand.Text()
	r := rt.UnknownSubmission()
	r.OperationRef = ref
	r.Key = s.Key()
	r.Outcome = "admitted"
	if !s.Capabilities().NativeAcknowledgement {
		r.NativeAcknowledged = rt.Observation{State: "not_supported"}
	}
	c := &command{receipt: r, submitting: true}
	s.commands[ref] = c
	s.active = c
	s.foreground = "running"
	s.mu.Unlock()
	native, err := s.native.Submit(ctx, text)
	s.mu.Lock()
	c.submitting = false
	if native.HandedToSDK.State != "" {
		c.receipt.HandedToSDK = native.HandedToSDK
	}
	if native.WriteCompleted.State != "" {
		c.receipt.WriteCompleted = native.WriteCompleted
	}
	if native.NativeAcknowledged.State != "" {
		c.receipt.NativeAcknowledged = native.NativeAcknowledged
	}
	if native.WorkRef != "" {
		c.receipt.WorkRef = native.WorkRef
	}
	if c.pending != nil {
		s.observe(c.pending)
	}
	if err != nil {
		c.receipt.Outcome = "unknown"
		if c.cancel == nil {
			s.foreground = "unknown"
		}
		category := "outcome_unknown"
		var safe *rt.SafeError
		if errors.As(err, &safe) {
			category = safe.Category
		}
		if category != "outcome_unknown" {
			c.receipt.Outcome = "rejected"
			c.finished = true
		}
		c.receipt.Failure = failure(category, ref, s.Key())
		a := copySubmission(c.receipt).SubmissionEvidence
		c.receipt.Failure.Acceptance = &a
	}
	// A source terminal observed during the call remains a separate fact.
	if c.finished && isKnown(c.receipt.Completed) {
		c.receipt.Outcome = "admitted"
	}
	s.settle(c)
	result := copySubmission(c.receipt)
	if result.Failure != nil {
		err = result.Failure
	}
	s.mu.Unlock()
	return result, err
}
func (s *publishedSession) Cancel(ctx context.Context, target string) (rt.Cancellation, error) {
	if err := ctx.Err(); err != nil {
		return rt.Cancellation{}, err
	}
	s.mu.Lock()
	c := s.commands[target]
	if c == nil {
		s.mu.Unlock()
		return rt.Cancellation{}, rt.ErrNotFound
	}
	if c.cancel != nil {
		r := copyCancellation(*c.cancel)
		s.mu.Unlock()
		return r, rt.ErrConflict
	}
	r := rt.Cancellation{OperationRef: "cancel:" + rand.Text(), Key: s.Key(), TargetOperationRef: target, WorkRef: c.receipt.WorkRef, Outcome: "admitted", Requested: rt.Known(true), NativeAcknowledged: rt.UnknownObservation(), TargetFinished: rt.UnknownObservation()}
	c.cancel = &r
	if c.finished {
		r.TargetFinished = c.receipt.Completed
		r.Requested = rt.Known(false)
		c.cancel = &r
		result := copyCancellation(r)
		s.mu.Unlock()
		return result, nil
	}
	if s.closed || s.terminal != nil {
		s.mu.Unlock()
		return rt.Cancellation{}, rt.ErrUnavailable
	}
	s.native.InvalidateInteractions(c.receipt.WorkRef)
	c.cancelling = true
	s.foreground = "cancelling"
	s.signal()
	// Waiting for a native ID cannot release the fixed target or admit new work.
	for s.Capabilities().NativeTurnID && c.submitting && c.receipt.WorkRef == "" && !c.finished && !s.closed && s.terminal == nil && ctx.Err() == nil {
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
		}
		s.mu.Lock()
	}
	var err error
	if c.finished {
		r.Requested = rt.Known(false)
		r.TargetFinished = rt.Known(true)
	} else if s.closed || s.terminal != nil {
		err = rt.ErrUnavailable
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else if s.Capabilities().NativeTurnID && c.receipt.WorkRef == "" {
		// The SDK has already settled without a usable native ID. A discarded late
		// reply cannot wake this cancellation, so do not wait forever or guess a target.
		err = rt.ErrOutcomeUnknown
	} else {
		id := c.receipt.WorkRef
		r.WorkRef = id
		// Keep the cancellation fence until the SDK call has settled. In particular,
		// a session-wide Claude interrupt must not race admission of the next input.
		s.mu.Unlock()
		err = s.native.Cancel(ctx, id)
		s.mu.Lock()
		if err == nil {
			r.NativeAcknowledged = rt.Known(true)
		}
	}
	c.cancelling = false
	if c.finished {
		r.TargetFinished = rt.Known(true)
	}
	if err != nil {
		r.Outcome = "unknown"
		if s.active == c {
			s.foreground = "unknown"
		}
		r.Failure = failure("outcome_unknown", r.OperationRef, s.Key())
		var safe *rt.SafeError
		if errors.As(err, &safe) {
			r.Failure.Category = safe.Category
			if safe.Category != "outcome_unknown" {
				r.Outcome = "rejected"
			}
		}
		err = r.Failure
	}
	c.cancel = &r
	s.settle(c)
	result := copyCancellation(r)
	s.mu.Unlock()
	return result, err
}

// observe only changes foreground for the matching root work. Child facts and
// metadata/idle observations cannot finish a command or clear background state.
func (s *publishedSession) observe(e *rt.Event) {
	if e.Origin.Attribution == "child" || e.Origin.Attribution == "unresolved" {
		return
	}
	var c *command
	for _, candidate := range s.commands {
		if e.WorkRef != "" && candidate.receipt.WorkRef == e.WorkRef {
			c = candidate
			break
		}
	}
	if c == nil && s.active != nil {
		candidate := s.active
		if !s.Capabilities().NativeTurnID && e.WorkRef == "" && (e.Kind == rt.TurnCompleted || e.Kind == rt.MessageCompleted || e.Kind == rt.MessageDelta || e.Kind == rt.InteractionRequested) {
			c = candidate
		}
	}
	if c == nil {
		return
	}
	e.OperationRef = c.receipt.OperationRef
	if s.Capabilities().NativeTurnID && e.Message != nil && e.Message.Role == "user" && e.WorkRef != "" {
		c.receipt.NativeRecorded = rt.Known(true)
	}
	if e.Kind == rt.TurnCompleted {
		c.finished = true
		c.receipt.Completed = rt.Known(true)
		if c.cancel != nil {
			c.cancel.TargetFinished = rt.Known(true)
		}
		s.settle(c)
	}
}
func (s *publishedSession) Close() error {
	s.mu.Lock()
	s.closed = true
	s.native.InvalidateInteractions("")
	if s.active != nil && !s.active.finished {
		s.foreground = "unknown"
	}
	s.signal()
	s.mu.Unlock()
	s.cancel()
	err := s.native.Close()
	<-s.finished
	return err
}

// compile-time assertion also protects adapters from exposing native controls.
var _ rt.Session = (*publishedSession)(nil)
