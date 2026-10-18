package managed

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// One publication owner assigns final cursors and projects metadata. Adapter
// receive positions retain their own meaning, including before identity binds.
type publishedSession struct {
	rt.Session
	next     sync.Mutex
	mu       sync.Mutex
	epoch    string
	seq      uint64
	metadata rt.MetadataPayload
}

func newPublishedSession(s rt.Session) *publishedSession {
	return &publishedSession{Session: s, epoch: rand.Text()}
}
func (s *publishedSession) Snapshot() rt.SessionSnapshot {
	snapshot := s.Session.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot.ConnectionEpoch = s.epoch
	snapshot.Metadata.Apply(s.metadata)
	return snapshot
}
func (s *publishedSession) Next(ctx context.Context) (rt.Event, error) {
	s.next.Lock()
	defer s.next.Unlock()
	e, err := s.Session.Next(ctx)
	if err != nil {
		return rt.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		e.Session = &rt.SessionSnapshot{Key: s.Key(), Attachment: "attached", ConnectionEpoch: s.epoch, ObservedAt: e.ObservedAt}
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
