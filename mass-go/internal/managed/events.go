package managed

import (
	"context"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// One publication owner assigns final cursors and projects metadata. Adapter
// receive positions retain their own meaning, including before identity binds.
type publishedSession struct {
	native        NativeSession
	next          sync.Mutex
	mu            sync.Mutex
	epoch         string
	seq           uint64
	metadata      rt.MetadataPayload
	commands      map[string]*command
	active        *command
	foreground    string
	closed        bool
	closeFinished chan struct{}
	closeErr      error
	wake          chan struct{}
	events        chan rt.Event
	finished      chan struct{}
	terminal      error
	observedAt    time.Time
	ctx           context.Context
	cancel        context.CancelFunc
}

func (s *publishedSession) Snapshot() rt.SessionSnapshot {
	snapshot := s.native.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
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
	snapshot.Background.Completeness = "unknown"
	snapshot.Metadata.Apply(s.metadata)
	return snapshot
}
func (s *publishedSession) receive() {
	defer close(s.finished)
	for {
		e, err := s.native.Next(s.ctx)
		if err != nil {
			s.mu.Lock()
			s.terminal = err
			s.observedAt = time.Now().UTC()
			if s.active != nil && !s.active.finished {
				if !isKnown(s.active.receipt.NativeAcknowledged) && !isKnown(s.active.receipt.WriteCompleted) {
					s.active.receipt.Outcome = "unknown"
				}
				s.foreground = "unknown"
			}
			s.signal()
			s.mu.Unlock()
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
		s.mu.Unlock()
		if err != nil {
			s.mu.Lock()
			s.terminal = err
			s.mu.Unlock()
			return
		}
		select {
		case s.events <- e:
		case <-s.ctx.Done():
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
	var e rt.Event
	select {
	case e = <-s.events:
	case <-ctx.Done():
		return rt.Event{}, ctx.Err()
	case <-s.finished:
		select {
		case e = <-s.events:
		default:
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.terminal != nil {
				return rt.Event{}, s.terminal
			}
			return rt.Event{}, context.Canceled
		}
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
	if e.Metadata != nil {
		s.metadata.Apply(*e.Metadata)
	}
	return e, nil
}
