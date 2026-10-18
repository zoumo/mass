package managed

import (
	"context"
	"errors"
	chanx "github.com/zoumo/golib/channx"
	"io"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// One publication owner assigns final cursors and projects metadata. Adapter
// receive positions retain their own meaning, including before identity binds.
type publishedSession struct {
	lifecycle                        *lifecycleQueue
	bindingAvailable, releaseUnknown bool
	lastLifecycleState               string
	commandFacts                     map[string]string
	native                           NativeSession
	next                             sync.Mutex
	mu                               sync.Mutex
	epoch                            string
	seq                              uint64
	metadata                         rt.MetadataPayload
	background                       backgroundProjection
	commands                         map[string]*command
	active                           *command
	foreground                       string
	closed                           bool
	closeFinished                    chan struct{}
	closeErr                         error
	wake                             chan struct{}
	events                           *chanx.ChannX
	limits                           rt.DeliveryLimits
	queuedFacts, queuedBytes         int
	discardOnce                      sync.Once
	aborted                          bool
	incomplete                       bool
	finished                         chan struct{}
	terminal                         error
	observedAt                       time.Time
	ctx                              context.Context
	cancel                           context.CancelFunc
}

func (s *publishedSession) Snapshot() rt.SessionSnapshot {
	snapshot := s.native.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(snapshot)
}
func (s *publishedSession) snapshotLocked(snapshot rt.SessionSnapshot) rt.SessionSnapshot {
	snapshot.Capabilities = s.capabilitiesLocked()
	if s.closed {
		snapshot.Attachment = "closing"
		select {
		case <-s.closeFinished:
			snapshot.Attachment = "closed"
		default:
		}
	}
	snapshot.ConnectionEpoch = s.epoch
	snapshot.ObservedAt = s.observedAt
	snapshot.Freshness = "cached"
	snapshot.Availability = "available"
	if s.closed || s.terminal != nil {
		snapshot.Availability = "unavailable"
	}
	snapshot.Foreground.State = s.foreground
	if s.active != nil {
		snapshot.Foreground.OperationRef = s.active.receipt.OperationRef
		snapshot.Foreground.WorkRef = s.active.receipt.WorkRef
	}
	snapshot.Background = s.background.snapshot()
	snapshot.Metadata.Apply(s.metadata)
	return snapshot
}
func (s *publishedSession) receive() {
	defer close(s.finished)
	defer s.events.Close()
	for {
		e, err := s.native.Next(s.ctx)
		if err != nil {
			s.mu.Lock()
			s.terminal = err
			s.observedAt = time.Now().UTC()
			s.failForegroundLocked()
			s.signal()
			s.mu.Unlock()
			s.native.InvalidateInteractions("")
			return
		}
		s.mu.Lock()
		// A Codex notification can precede the StartTurn reply. Retain it until
		// the exact returned native ID proves correlation; never bind by current work.
		if s.Capabilities().NativeTurnID && s.active != nil && s.active.submitting && e.WorkRef != "" {
			c := s.active
			c.pending = &e
			for c.submitting && !s.closed {
				wake := s.wake
				s.mu.Unlock()
				select {
				case <-wake:
				case <-s.ctx.Done():
				}
				s.mu.Lock()
			}
			c.pending = nil
		}
		s.observe(&e)
		e, err = s.publish(e)
		if err == nil {
			s.observedAt = e.ObservedAt
		}

		var size int
		if err == nil {
			size, err = e.RetainedJSONBytes()
		}
		if err == nil && (s.queuedFacts >= s.limits.Facts || s.queuedBytes+size > s.limits.Bytes) {
			err = errors.Join(rt.ErrIncomplete, rt.ErrBackpressure)
		}
		if err != nil {
			s.terminal = err
			s.incomplete = true
			s.failForegroundLocked()
			s.signal()
			s.mu.Unlock()
			s.native.InvalidateInteractions("")
			go s.native.Close()
			return
		}
		s.emitStateLocked()
		s.queuedFacts++
		s.queuedBytes += size
		s.mu.Unlock()
		// Only this owner writes or closes the queue; ChannX never blocks on
		// the public reader. Runtime quotas include its buffered output.
		s.events.In() <- queuedEvent{event: e, bytes: size}
		if e.Error != nil && e.Error.Category == "decode_error" {
			s.mu.Lock()
			s.terminal = errors.Join(rt.ErrIncomplete, e.Error)
			s.incomplete = true
			s.failForegroundLocked()
			s.signal()
			s.mu.Unlock()
			s.native.InvalidateInteractions("")
			go s.native.Close()
			return
		}
	}
}
func (s *publishedSession) Next(ctx context.Context) (rt.Event, error) {
	s.next.Lock()
	defer s.next.Unlock()
	if err := ctx.Err(); err != nil {
		return rt.Event{}, err
	}

	s.mu.Lock()
	aborted := s.aborted
	s.mu.Unlock()
	if aborted {
		return rt.Event{}, rt.ErrIncomplete
	}
	var e rt.Event
	select {
	case item, ok := <-s.events.Out():
		if !ok {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.terminal != nil {
				return rt.Event{}, s.terminal
			}
			return rt.Event{}, io.EOF
		}
		q := item.(queuedEvent)
		s.mu.Lock()
		s.queuedFacts--
		s.queuedBytes -= q.bytes
		s.signal()
		aborted = s.aborted
		s.mu.Unlock()
		if aborted {
			return rt.Event{}, rt.ErrIncomplete
		}
		e = q.event
	case <-ctx.Done():
		return rt.Event{}, ctx.Err()
	}
	// Claude can observe provisional facts before native identity. Only opened
	// sessions are externally reachable; bind the final immutable key here.
	e.RuntimeName = s.Key().RuntimeName
	e.SessionID = s.Key().SessionID
	if e.Session != nil {
		e.Session.Key = s.Key()
	}
	return e, nil
}
func (s *publishedSession) publish(e rt.Event) (rt.Event, error) {
	if s.seq == 9007199254740991 {
		return rt.Event{}, rt.ErrIncomplete
	}
	s.seq++
	if e.Message != nil {
		switch e.Message.Stage {
		case "started":
			e.Kind = rt.MessageStarted
		case "delta":
			e.Kind = rt.MessageDelta
		case "completed":
			e.Kind = rt.MessageCompleted
		}
	}
	if e.Reasoning != nil {
		if e.Reasoning.Stage == "delta" {
			e.Kind = rt.ReasoningDelta
		} else {
			e.Kind = rt.ReasoningCompleted
		}
	}
	e.SchemaVersion = 1
	e.Scope = "session"
	e.RuntimeName = s.Key().RuntimeName
	e.SessionID = s.Key().SessionID
	e.ConnectionEpoch = s.epoch
	e.Seq = s.seq
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now().UTC()
	}
	if e.Origin.Kind == "" {
		e.Origin = rt.EventOrigin{Kind: "native", Attribution: "root", Completeness: "complete"}
	}
	if e.Kind == rt.SessionReady {
		e.Session = &rt.SessionSnapshot{Key: s.Key(), Attachment: "attached", ConnectionEpoch: s.epoch, Foreground: rt.ForegroundState{State: s.foreground}, Background: rt.BackgroundSummary{Completeness: "unknown"}, ObservedAt: e.ObservedAt}
	}
	if e.Kind != rt.Unknown {
		if e.Origin.SourceKind == "" {
			e.Origin.SourceKind = e.Diagnostic.NativeKind
		}
		e.Diagnostic = rt.Diagnostic{}
	}
	if err := s.background.apply(e); err != nil {
		return rt.Event{}, err
	}
	if e.Metadata != nil && e.Origin.Attribution == "root" && e.Subject == nil {
		s.metadata.Apply(*e.Metadata)
	}
	return e, nil
}

type queuedEvent struct {
	event rt.Event
	bytes int
}

func (s *publishedSession) discard() {
	s.mu.Lock()
	s.aborted = true
	s.incomplete = true
	s.mu.Unlock()
	// The producer has finished. One discard owner releases the ChannX
	// terminator without extending a caller's expired total wait.
	s.discardOnce.Do(func() {
		go func() {
			for item := range s.events.Out() {
				q := item.(queuedEvent)
				s.mu.Lock()
				s.queuedFacts--
				s.queuedBytes -= q.bytes
				s.signal()
				s.mu.Unlock()
			}
		}()
	})
}
func (s *publishedSession) drain(ctx context.Context) error {
	for {
		s.mu.Lock()
		n := s.queuedFacts
		wake := s.wake
		s.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// AbandonSession settles a publication attachment that will never be exposed to
// a caller. No future reader can own its prefix, so release the delivery queue.
func AbandonSession(session rt.Session) error {
	err := session.Close()
	if s, ok := session.(*publishedSession); ok {
		s.discard()
	}
	return err
}
