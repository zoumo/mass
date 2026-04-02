package events

import (
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// Translator drains a channel of acp.SessionNotification, translates each
// notification into a typed Event, and fans the result out to all registered
// subscriber channels. If log is non-nil, every event is also appended to the
// JSONL event log for durable history.
type Translator struct {
	in     <-chan acp.SessionNotification
	log    *EventLog
	mu     sync.Mutex
	subs   map[int]chan Event
	nextID int
	done   chan struct{}
}

// NewTranslator creates a Translator that reads from in.
// Pass a non-nil EventLog to enable durable event logging.
func NewTranslator(in <-chan acp.SessionNotification, log *EventLog) *Translator {
	return &Translator{
		in:   in,
		log:  log,
		subs: make(map[int]chan Event),
		done: make(chan struct{}),
	}
}

// Start launches the background fan-out goroutine. Call once.
func (t *Translator) Start() {
	go t.run()
}

// Stop signals the background goroutine to exit and drains/closes all subscriber channels.
func (t *Translator) Stop() {
	close(t.done)
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, ch := range t.subs {
		close(ch)
		delete(t.subs, id)
	}
}

// Subscribe returns a buffered channel (cap 64) that will receive translated
// Events, along with a subscription ID for later Unsubscribe calls.
func (t *Translator) Subscribe() (<-chan Event, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := t.nextID
	t.nextID++
	ch := make(chan Event, 64)
	t.subs[id] = ch
	return ch, id
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

// NotifyTurnStart broadcasts a TurnStartEvent to all subscribers.
func (t *Translator) NotifyTurnStart() {
	t.broadcast(TurnStartEvent{})
}

// NotifyTurnEnd broadcasts a TurnEndEvent with the given stop reason.
func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
	t.broadcast(TurnEndEvent{StopReason: string(reason)})
}
// run is the background fan-out goroutine.
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
			t.broadcast(ev)
		}
	}
}

// broadcast sends ev to all current subscribers using non-blocking sends,
// and appends it to the event log if one is configured.
func (t *Translator) broadcast(ev Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ch := range t.subs {
		select {
		case ch <- ev:
		default:
			// slow subscriber — drop rather than block fan-out
		}
	}
	if t.log != nil {
		// Log write is best-effort; a failure here must not block the event stream.
		_ = t.log.Append(logTypeName(ev), ev)
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
		// Agent echoes the incoming user prompt back as a UserMessageChunk.
		return UserMessageEvent{Text: safeBlockText(u.UserMessageChunk.Content)}
	case u.ToolCall != nil:
		tc := u.ToolCall
		return ToolCallEvent{
			ID:    string(tc.ToolCallId),
			Kind:  string(tc.Kind),
			Title: tc.Title,
		}
	case u.ToolCallUpdate != nil:
		tcu := u.ToolCallUpdate
		return ToolResultEvent{
			ID:     string(tcu.ToolCallId),
			Status: safeStatus(tcu.Status),
		}
	case u.Plan != nil:
		return PlanEvent{Entries: u.Plan.Entries}
	case u.AvailableCommandsUpdate != nil, u.CurrentModeUpdate != nil:
		// Known variants with no current representation — silently ignored.
		return nil
	default:
		return ErrorEvent{Msg: "unknown session update variant"}
	}
}

// logTypeName returns the string discriminator for ev, used as the "type"
// field in LogEntry. Mirrors the mapping in pkg/rpc/server.go:eventTypeName.
func logTypeName(ev Event) string {
	switch ev.(type) {
	case TextEvent:
		return "text"
	case ThinkingEvent:
		return "thinking"
	case UserMessageEvent:
		return "user_message"
	case ToolCallEvent:
		return "tool_call"
	case ToolResultEvent:
		return "tool_result"
	case FileWriteEvent:
		return "file_write"
	case FileReadEvent:
		return "file_read"
	case CommandEvent:
		return "command"
	case PlanEvent:
		return "plan"
	case TurnStartEvent:
		return "turn_start"
	case TurnEndEvent:
		return "turn_end"
	case ErrorEvent:
		return "error"
	default:
		return "unknown"
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
