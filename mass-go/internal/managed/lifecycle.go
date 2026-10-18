package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// lifecycleQueue has one publication lock and no producer/reader goroutine.
// It owns encoded immutable data, finite count/byte quotas and a visible terminal
// error after the accepted prefix. Cleanup never waits for its consumer.
type lifecycleQueue struct {
	mu       sync.Mutex
	next     sync.Mutex
	name     string
	limits   rt.DeliveryLimits
	facts    [][]byte
	bytes    int
	seq      uint64
	wake     chan struct{}
	terminal error
	settled  bool
	running  bool
}

func newLifecycleQueue(name string, limits rt.DeliveryLimits) *lifecycleQueue {
	return &lifecycleQueue{name: name, limits: limits.Effective(), wake: make(chan struct{})}
}
func (q *lifecycleQueue) signal()              { close(q.wake); q.wake = make(chan struct{}) }
func (q *lifecycleQueue) terminalError() error { q.mu.Lock(); defer q.mu.Unlock(); return q.terminal }
func (q *lifecycleQueue) failed() bool         { return q.terminalError() != nil }
func (q *lifecycleQueue) accepting() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running && q.terminal == nil
}
func (q *lifecycleQueue) setRunning(value bool) { q.mu.Lock(); q.running = value; q.mu.Unlock() }
func (q *lifecycleQueue) delivery() string {
	if q.failed() {
		return "incomplete"
	}
	return "available"
}
func (q *lifecycleQueue) reopen() { q.mu.Lock(); q.settled = false; q.signal(); q.mu.Unlock() }
func (q *lifecycleQueue) settle() { q.mu.Lock(); q.settled = true; q.signal(); q.mu.Unlock() }
func (q *lifecycleQueue) publish(e rt.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.terminal != nil {
		return
	}
	if q.seq == 9007199254740991 {
		q.terminal = rt.ErrIncomplete
		q.signal()
		return
	}
	e.SchemaVersion = 1
	e.RuntimeName = q.name
	e.Seq = q.seq + 1
	e.ObservedAt = time.Now().UTC()
	e.Origin = rt.EventOrigin{Kind: "runtime", Attribution: "runtime", Completeness: "complete"}
	data, err := json.Marshal(e)
	if err != nil {
		q.terminal = errors.Join(rt.ErrIncomplete, &rt.SafeError{Category: "internal_failure", Scope: "runtime", Message: "lifecycle event encoding failed"})
	} else if len(q.facts) >= q.limits.Facts || q.bytes+len(data) > q.limits.Bytes {
		q.terminal = errors.Join(rt.ErrIncomplete, rt.ErrBackpressure)
	} else {
		q.seq++
		q.facts = append(q.facts, data)
		q.bytes += len(data)
	}
	q.signal()
}
func (q *lifecycleQueue) Next(ctx context.Context) (rt.Event, error) {
	q.next.Lock()
	defer q.next.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return rt.Event{}, err
		}
		q.mu.Lock()
		if len(q.facts) > 0 {
			data := q.facts[0]
			q.facts[0] = nil
			q.facts = q.facts[1:]
			q.bytes -= len(data)
			q.mu.Unlock()
			var e rt.Event
			err := json.Unmarshal(data, &e)
			return e, err
		}
		err := q.terminal
		settled := q.settled
		wake := q.wake
		q.mu.Unlock()
		if err != nil {
			return rt.Event{}, err
		}
		if settled {
			return rt.Event{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return rt.Event{}, ctx.Err()
		case <-wake:
		}
	}
}
func (r *Runtime) Next(ctx context.Context) (rt.Event, error) { return r.lifecycle.Next(ctx) }
func (r *Runtime) emitStatusLocked() {
	snapshot := r.statusLocked()
	r.lifecycle.publish(rt.Event{Kind: rt.RuntimeStatusChanged, Scope: "runtime", Runtime: &snapshot})
}
func (r *Runtime) emitOperationLocked(op *creation) {
	snapshot := copyOperation(op.snapshot)
	r.lifecycle.publish(rt.Event{Kind: rt.OperationStateChanged, Scope: "operation", OperationRef: snapshot.OperationRef, Operation: &rt.OperationPayload{Kind: operationKind(op), State: snapshot.Phase, Creation: &snapshot}})
	if snapshot.Failure != "" {
		r.lifecycle.publish(rt.Event{Kind: rt.OperationFailed, Scope: "operation", OperationRef: snapshot.OperationRef, Error: &rt.SafeError{Category: snapshot.Failure, Scope: "operation", OperationRef: snapshot.OperationRef, Message: "native creation did not establish a managed session"}})
	}
}
func (r *Runtime) attachSessionLocked(session rt.Session, kind rt.EventKind, operation string) {
	s, ok := session.(*publishedSession)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lifecycle = r.lifecycle
	s.bindingAvailable = r.bindings != nil
	snapshot := s.snapshotLocked(s.native.Snapshot())
	r.lifecycle.publish(rt.Event{Kind: kind, Scope: "runtime", OperationRef: operation, Session: &snapshot})
	s.lastLifecycleState = s.stateKeyLocked(snapshot)
	for _, command := range s.commands {
		s.emitSubmissionLocked(command)
		if command.cancel != nil {
			s.emitCancellationLocked(*command.cancel)
		}
	}
}
func (s *publishedSession) stateKeyLocked(snapshot rt.SessionSnapshot) string {
	data, _ := json.Marshal(struct {
		Attachment, Availability string
		Foreground               rt.ForegroundState
		Interactions             rt.InteractionSummary
		Capabilities             []rt.CapabilityAvailability
	}{snapshot.Attachment, snapshot.Availability, snapshot.Foreground, snapshot.Interactions, snapshot.Capabilities})
	return string(data)
}
func (s *publishedSession) emitStateLocked() {
	if s.lifecycle == nil {
		return
	}
	snapshot := s.snapshotLocked(s.native.Snapshot())
	key := s.stateKeyLocked(snapshot)
	if key == s.lastLifecycleState {
		return
	}
	s.lastLifecycleState = key
	s.lifecycle.publish(rt.Event{Kind: rt.SessionStateChanged, Scope: "runtime", Session: &snapshot})
}
func markReleaseUnknown(session rt.Session) {
	if s, ok := session.(*publishedSession); ok {
		s.mu.Lock()
		s.releaseUnknown = true
		s.emitStateLocked()
		s.mu.Unlock()
	}
}

func operationKind(op *creation) string {
	if op.kind != "" {
		return op.kind
	}
	return "creation"
}
func (s *publishedSession) emitCommandLocked(ref string, payload rt.OperationPayload, failure *rt.SafeError) {
	if s.lifecycle == nil || s.lifecycle.failed() {
		return
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if s.commandFacts == nil {
		s.commandFacts = map[string]string{}
	}
	if s.commandFacts[ref] == digest {
		return
	}
	if len(s.commandFacts) >= maxControlRecords*3 && s.commandFacts[ref] == "" {
		s.lifecycle.fail()
		return
	}
	s.commandFacts[ref] = digest
	s.lifecycle.publish(rt.Event{Kind: rt.OperationStateChanged, Scope: "operation", OperationRef: ref, Operation: &payload})
	if failure != nil {
		e := copyFailure(failure)
		if payload.Submission != nil && e.Acceptance == nil {
			evidence := copySubmission(*payload.Submission).SubmissionEvidence
			e.Acceptance = &evidence
		}
		e.OperationRef = ref
		e.Scope = "operation"
		s.lifecycle.publish(rt.Event{Kind: rt.OperationFailed, Scope: "operation", OperationRef: ref, Error: e})
	}
}
func (q *lifecycleQueue) fail() {
	q.mu.Lock()
	if q.terminal == nil {
		q.terminal = errors.Join(rt.ErrIncomplete, rt.ErrBackpressure)
		q.signal()
	}
	q.mu.Unlock()
}
func (s *publishedSession) emitSubmissionLocked(c *command) {
	receipt := copySubmission(c.receipt)
	receipt.Key = s.Key()
	state := receipt.Outcome
	if isKnown(receipt.Completed) {
		state = "settled"
	}
	s.emitCommandLocked(receipt.OperationRef, rt.OperationPayload{Kind: "submission", State: state, Submission: &receipt}, receipt.Failure)
}
func (s *publishedSession) emitCancellationLocked(value rt.Cancellation) {
	receipt := copyCancellation(value)
	state := receipt.Outcome
	if isKnown(receipt.TargetFinished) {
		state = "settled"
	}
	s.emitCommandLocked(receipt.OperationRef, rt.OperationPayload{Kind: "cancellation", State: state, Cancellation: &receipt}, receipt.Failure)
}

func (s *publishedSession) failForegroundLocked() {
	if s.active == nil || s.active.finished {
		return
	}
	s.active.receipt.Outcome = "unknown"
	s.foreground = "unknown"
	s.active.receipt.Failure = failure("outcome_unknown", s.active.receipt.OperationRef, s.Key())
	s.emitSubmissionLocked(s.active)
}
