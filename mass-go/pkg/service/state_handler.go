package service

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// stateHandler projects observations only. It owns no runtime control or history
// responsibility; lifecycle writes remain synchronous Service operations.
type stateHandler struct {
	meta         metastore.MetaStore
	session      resource.Session
	mu           sync.Mutex
	queue        chan events.SessionEvent
	stop         chan struct{}
	done         chan struct{}
	closed       bool
	closeTimeout time.Duration
}

func newStateHandler(meta metastore.MetaStore, session resource.Session, capacity int, closeTimeout time.Duration) *stateHandler {
	h := &stateHandler{meta: meta, session: session, queue: make(chan events.SessionEvent, capacity), stop: make(chan struct{}), done: make(chan struct{}), closeTimeout: closeTimeout}
	go h.run()
	return h
}

func (*stateHandler) Required() bool { return false }
func (h *stateHandler) Handle(e events.SessionEvent) error {
	if _, ok := e.Payload().(events.StateUpdate); !ok {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("state handler closed")
	}
	select {
	case h.queue <- e:
		return nil
	default:
		return errors.New("state observation queue full")
	}
}

func (h *stateHandler) run() {
	defer close(h.done)
	for {
		// Shutdown discards pending observations; it does not promise convergence.
		select {
		case <-h.stop:
			return
		default:
		}
		select {
		case <-h.stop:
			return
		case e := <-h.queue:
			if err := h.update(e); err != nil {
				slog.Warn("state observation failed", "error", err, "session_id", e.SessionID)
			}
		}
	}
}

func (h *stateHandler) update(e events.SessionEvent) error {
	state := e.Payload().(events.StateUpdate)
	for {
		select {
		case <-h.stop:
			return nil
		default:
		}
		r, err := h.meta.GetSession(resource.SessionRef{SessionID: e.SessionID})
		if err != nil {
			return err
		}
		if r.Metadata.UID != h.session.Metadata.UID || r.Metadata.Namespace != e.Namespace || r.Metadata.Name != e.SessionName || r.Status.SessionID != e.SessionID {
			return errors.New("state observation session mismatch")
		}
		if r.Status.State == "closing" || r.Status.State == "offline" || r.Status.AppliedGeneration == 0 {
			return nil
		}
		r.Status.State = events.Unknown
		if state.State != nil {
			r.Status.State = *state.State
		}
		_, err = h.meta.UpdateSession(r)
		// Only a known rejected conditional write is safe to retry. An unknown
		// outcome is diagnosed once and is never treated as an unwritten event.
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
}

func (h *stateHandler) Close() error {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.stop)
	}
	h.mu.Unlock()
	timer := time.NewTimer(h.closeTimeout)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-timer.C:
		return errors.New("state handler write still settling")
	}
}
