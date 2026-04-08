package events

import (
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

// Translator drains ACP session notifications, translates each notification
// into the stable shim envelope surface, and fans the result out to all
// registered subscriber channels. If log is non-nil, every envelope is also
// appended to the JSONL event log for durable history.
type Translator struct {
	sessionID string
	in        <-chan acp.SessionNotification
	log       *EventLog

	mu               sync.Mutex
	subs             map[int]chan Envelope
	nextID           int
	nextSeq          int
	done             chan struct{}
	once             sync.Once
	currentTurnId    string
	currentStreamSeq int
}

// NewTranslator creates a Translator that reads from in.
// Pass a non-nil EventLog to enable durable event logging.
func NewTranslator(sessionID string, in <-chan acp.SessionNotification, log *EventLog) *Translator {
	nextSeq := 0
	if log != nil {
		nextSeq = log.NextSeq()
	}
	return &Translator{
		sessionID: sessionID,
		in:        in,
		log:       log,
		subs:      make(map[int]chan Envelope),
		nextSeq:   nextSeq,
		done:      make(chan struct{}),
	}
}

// Start launches the background fan-out goroutine. Call once.
func (t *Translator) Start() {
	go t.run()
}

// Stop signals the background goroutine to exit and drains/closes all subscriber channels.
func (t *Translator) Stop() {
	t.once.Do(func() {
		close(t.done)
		t.mu.Lock()
		defer t.mu.Unlock()
		for id, ch := range t.subs {
			close(ch)
			delete(t.subs, id)
		}
	})
}

// Subscribe returns a buffered channel that will receive translated envelopes,
// along with a subscription ID and the next sequence number that could be
// assigned after the subscription is established.
func (t *Translator) Subscribe() (<-chan Envelope, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	ch := make(chan Envelope, 64)
	t.subs[id] = ch
	return ch, id, t.nextSeq
}

// SubscribeFromSeq atomically reads history from logPath starting at fromSeq
// and registers a live subscription, all under the Translator's mutex.
// This eliminates the event gap between separate History and Subscribe calls.
//
// Returns (backfill entries, subscription channel, subscription ID, nextSeq, error).
//
// Intended for recovery/startup only — holds the mutex during file I/O.
// Do not use in hot paths where event broadcasting latency matters.
func (t *Translator) SubscribeFromSeq(logPath string, fromSeq int) ([]Envelope, <-chan Envelope, int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries, err := ReadEventLog(logPath, fromSeq)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if entries == nil {
		entries = []Envelope{}
	}

	id := t.nextID
	t.nextID++
	ch := make(chan Envelope, 64)
	t.subs[id] = ch

	return entries, ch, id, t.nextSeq, nil
}

// Unsubscribe removes a subscriber and closes its channel.
func (t *Translator) Unsubscribe(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ch, ok := t.subs[id]; ok {
		close(ch)
		delete(t.subs, id)
	}
}

// LastSeq returns the last assigned sequence number, or -1 when no envelope
// has been emitted yet.
func (t *Translator) LastSeq() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextSeq - 1
}

// NotifyTurnStart broadcasts a session/update turn_start envelope.
// The new turnId and initial streamSeq are assigned atomically inside the
// broadcastEnvelope callback, which runs under mu.Lock.
func (t *Translator) NotifyTurnStart() {
	newTurnId := uuid.New().String()
	t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
		// Runs under mu.Lock — safe to mutate turn state here.
		t.currentTurnId = newTurnId
		t.currentStreamSeq = 0
		ss := t.currentStreamSeq // = 0
		t.currentStreamSeq++
		params := SessionUpdateParams{
			SequenceMeta: SequenceMeta{
				SessionID: t.sessionID, Seq: seq,
				Timestamp: at.UTC().Format(time.RFC3339Nano),
			},
			TurnId:    t.currentTurnId,
			StreamSeq: &ss,
			Event:     newTypedEvent(TurnStartEvent{}),
		}
		return Envelope{Method: MethodSessionUpdate, Params: params}
	})
}

// NotifyTurnEnd broadcasts a session/update turn_end envelope.
// The current turnId is included in the event and cleared AFTER use so the
// turn_end event itself carries the identifier. All state mutations run inside
// the broadcastEnvelope callback under mu.Lock.
func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
	t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
		// Runs under mu.Lock.
		ss := t.currentStreamSeq
		t.currentStreamSeq++
		params := SessionUpdateParams{
			SequenceMeta: SequenceMeta{
				SessionID: t.sessionID, Seq: seq,
				Timestamp: at.UTC().Format(time.RFC3339Nano),
			},
			TurnId:    t.currentTurnId,
			StreamSeq: &ss,
			Event:     newTypedEvent(TurnEndEvent{StopReason: string(reason)}),
		}
		t.currentTurnId = "" // Clear AFTER using — turn_end event carries the turnId
		return Envelope{Method: MethodSessionUpdate, Params: params}
	})
}

// NotifyStateChange broadcasts a runtime/stateChange envelope.
func (t *Translator) NotifyStateChange(previousStatus, status string, pid int, reason string) {
	t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
		return NewRuntimeStateChangeEnvelope(t.sessionID, seq, at, previousStatus, status, pid, reason)
	})
}

func (t *Translator) run() {
	for {
		select {
		case <-t.done:
			return
		case n, ok := <-t.in:
			if !ok {
				return
			}
			ev := translate(n)
			if ev == nil {
				continue
			}
			t.broadcastSessionEvent(ev)
		}
	}
}

func (t *Translator) broadcastSessionEvent(ev Event) {
	t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
		env := NewSessionUpdateEnvelope(t.sessionID, seq, at, ev)
		if t.currentTurnId != "" {
			params := env.Params.(SessionUpdateParams)
			params.TurnId = t.currentTurnId
			ss := t.currentStreamSeq
			params.StreamSeq = &ss
			t.currentStreamSeq++
			env.Params = params
		}
		return env
	})
}

func (t *Translator) broadcastEnvelope(build func(seq int, at time.Time) Envelope) {
	var (
		env  Envelope
		log  *EventLog
		subs []chan Envelope
	)

	t.mu.Lock()
	env = build(t.nextSeq, time.Now().UTC())
	t.nextSeq++
	log = t.log
	subs = make([]chan Envelope, 0, len(t.subs))
	for _, ch := range t.subs {
		subs = append(subs, ch)
	}
	t.mu.Unlock()

	if log != nil {
		// Log writes are best-effort; a history failure must not block live fan-out.
		_ = log.Append(env)
	}
	for _, ch := range subs {
		select {
		case ch <- env:
		default:
			// Slow subscriber — drop rather than block fan-out.
		}
	}
}

// translate converts a raw SessionNotification into a typed Event.
// Returns nil for known-but-uninteresting variants (AvailableCommandsUpdate,
// CurrentModeUpdate) that have no meaningful representation in the event stream.
func translate(n acp.SessionNotification) Event {
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		return TextEvent{Text: safeBlockText(u.AgentMessageChunk.Content)}
	case u.AgentThoughtChunk != nil:
		return ThinkingEvent{Text: safeBlockText(u.AgentThoughtChunk.Content)}
	case u.UserMessageChunk != nil:
		return UserMessageEvent{Text: safeBlockText(u.UserMessageChunk.Content)}
	case u.ToolCall != nil:
		tc := u.ToolCall
		return ToolCallEvent{ID: string(tc.ToolCallId), Kind: string(tc.Kind), Title: tc.Title}
	case u.ToolCallUpdate != nil:
		tcu := u.ToolCallUpdate
		return ToolResultEvent{ID: string(tcu.ToolCallId), Status: safeStatus(tcu.Status)}
	case u.Plan != nil:
		return PlanEvent{Entries: u.Plan.Entries}
	case u.AvailableCommandsUpdate != nil, u.CurrentModeUpdate != nil:
		return nil
	default:
		return ErrorEvent{Msg: "unknown session update variant"}
	}
}

// safeBlockText extracts the text string from a ContentBlock, returning ""
// if the Text variant is nil.
func safeBlockText(cb acp.ContentBlock) string {
	if cb.Text != nil {
		return cb.Text.Text
	}
	return ""
}

// safeStatus converts a *ToolCallStatus to a string, returning "unknown" when nil.
func safeStatus(s *acp.ToolCallStatus) string {
	if s == nil {
		return "unknown"
	}
	return string(*s)
}
