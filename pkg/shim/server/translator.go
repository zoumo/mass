package server

import (
	"log/slog"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	apishim "github.com/zoumo/mass/pkg/shim/api"
)

// Translator drains ACP session notifications, translates each notification
// into a ShimEvent, and fans the result out to all registered subscriber
// channels. If log is non-nil, every ShimEvent is also appended to the JSONL
// event log for durable history (log-before-fanout under mutex).
type Translator struct {
	runID     string
	sessionID string // ACP session ID, set after handshake via SetSessionID
	in        <-chan acp.SessionNotification
	log       *EventLog

	// sessionMetadataHook is called after broadcastSessionEvent for metadata
	// event types (available_commands, config_option, session_info, current_mode).
	// Set once before Start() via SetSessionMetadataHook — no lock needed.
	sessionMetadataHook func(apishim.Event)

	mu               sync.Mutex
	subs             map[int]chan apishim.ShimEvent
	nextID           int
	nextSeq          int
	done             chan struct{}
	once             sync.Once
	currentTurnId    string
	currentBlockType string // event type of currently open content block ("" = none)
	eventCounts      map[string]int
}

// NewTranslator creates a Translator that reads from in.
// runID is the agent run identifier (the shim --id value).
// Pass a non-nil EventLog to enable durable event logging.
func NewTranslator(runID string, in <-chan acp.SessionNotification, log *EventLog) *Translator {
	nextSeq := 0
	if log != nil {
		nextSeq = log.NextSeq()
	}
	return &Translator{
		runID:       runID,
		in:          in,
		log:         log,
		subs:        make(map[int]chan apishim.ShimEvent),
		nextSeq:     nextSeq,
		done:        make(chan struct{}),
		eventCounts: make(map[string]int),
	}
}

// SetSessionID injects the ACP session ID after the session/new handshake
// completes. This is called by the shim command after mgr.Create() succeeds.
func (t *Translator) SetSessionID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = id
}

// SetSessionMetadataHook registers a callback that is invoked after
// broadcastSessionEvent for metadata event types (available_commands,
// config_option, session_info, current_mode). Must be called before Start();
// the field is read without a lock in the run goroutine.
func (t *Translator) SetSessionMetadataHook(hook func(apishim.Event)) {
	t.sessionMetadataHook = hook
}

// maybeNotifyMetadata calls sessionMetadataHook for the 4 metadata event types.
// Called from run() AFTER broadcastSessionEvent returns (Translator.mu released).
// All other event types are silently ignored.
func (t *Translator) maybeNotifyMetadata(ev apishim.Event) {
	if t.sessionMetadataHook == nil {
		return
	}
	switch ev.(type) {
	case apishim.AvailableCommandsEvent,
		apishim.ConfigOptionEvent,
		apishim.SessionInfoEvent,
		apishim.CurrentModeEvent:
		t.sessionMetadataHook(ev)
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

// Subscribe returns a buffered channel that will receive translated ShimEvents,
// along with a subscription ID and the next sequence number that could be
// assigned after the subscription is established.
func (t *Translator) Subscribe() (<-chan apishim.ShimEvent, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	ch := make(chan apishim.ShimEvent, 1024)
	t.subs[id] = ch
	return ch, id, t.nextSeq
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

// EventCounts returns a snapshot of the per-event-type count map.
// The returned map is a copy — callers cannot race with broadcast().
func (t *Translator) EventCounts() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make(map[string]int, len(t.eventCounts))
	for k, v := range t.eventCounts {
		cp[k] = v
	}
	return cp
}

// LastSeq returns the last assigned sequence number, or -1 when no event
// has been emitted yet.
func (t *Translator) LastSeq() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextSeq - 1
}

// NotifyTurnStart broadcasts a turn_start ShimEvent.
// The new turnId is assigned atomically inside the broadcast callback,
// which runs under mu.Lock.
func (t *Translator) NotifyTurnStart() {
	newTurnID := uuid.New().String()
	t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
		t.currentTurnId = newTurnID
		return apishim.ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  apishim.CategorySession,
			Type:      apishim.EventTypeTurnStart,
			TurnID:    t.currentTurnId,
			Payload:   apishim.TurnStartEvent{},
		}
	})
}

// NotifyUserPrompt broadcasts a user_message ShimEvent so that all subscribers
// (including late-joining chat clients) see the user's prompt.
// This must be called after NotifyTurnStart and before mgr.Prompt.
func (t *Translator) NotifyUserPrompt(text string) {
	t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
		return apishim.ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  apishim.CategorySession,
			Type:      apishim.EventTypeUserMessage,
			TurnID:    t.currentTurnId,
			Payload:   apishim.NewContentEvent(apishim.EventTypeUserMessage, apishim.BlockStatusStart, apishim.TextBlock(text)),
		}
	})
}

// NotifyTurnEnd broadcasts a turn_end ShimEvent.
// Closes any open content block first, then emits turn_end.
// The current turnId is included in the event and cleared AFTER use so the
// turn_end event itself carries the identifier.
func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
	t.closeOpenBlock()
	t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
		se := apishim.ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  apishim.CategorySession,
			Type:      apishim.EventTypeTurnEnd,
			TurnID:    t.currentTurnId,
			Payload:   apishim.TurnEndEvent{StopReason: string(reason)},
		}
		t.currentTurnId = ""
		return se
	})
}

// NotifyStateChange broadcasts a runtime category state_change ShimEvent.
// Runtime events never carry turn fields.
// sessionChanged lists state sections that were updated (e.g. ["agentInfo","capabilities"]
// for the synthetic bootstrap-metadata event). Pass nil for lifecycle-only transitions.
func (t *Translator) NotifyStateChange(previousStatus, status string, pid int, reason string, sessionChanged []string) {
	t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
		return apishim.ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  apishim.CategoryRuntime,
			Type:      apishim.EventTypeStateChange,
			Payload: apishim.StateChangeEvent{
				PreviousStatus: previousStatus,
				Status:         status,
				PID:            pid,
				Reason:         reason,
				SessionChanged: sessionChanged,
			},
		}
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

			if isContentEvent(ev) {
				evType := apishim.EventTypeOf(ev)
				if t.currentBlockType == "" {
					ev = setContentStatus(ev, apishim.BlockStatusStart)
					t.currentBlockType = evType
				} else if t.currentBlockType != evType {
					t.closeOpenBlock()
					ev = setContentStatus(ev, apishim.BlockStatusStart)
					t.currentBlockType = evType
				} else {
					ev = setContentStatus(ev, apishim.BlockStatusStreaming)
				}
			} else {
				t.closeOpenBlock()
			}

			t.broadcastSessionEvent(ev)
			t.maybeNotifyMetadata(ev)
		}
	}
}

// isContentEvent returns true for content block event types.
func isContentEvent(ev apishim.Event) bool {
	_, ok := ev.(apishim.ContentEvent)
	return ok
}

// setContentStatus sets the Status field on a ContentEvent.
func setContentStatus(ev apishim.Event, status string) apishim.Event {
	if ce, ok := ev.(apishim.ContentEvent); ok {
		ce.Status = status
		return ce
	}
	return ev
}

// emitBlockEnd broadcasts a synthetic "end" event for the given content block type.
// Uses TextBlock("") instead of zero-valued ContentBlock{} because acp.ContentBlock
// requires exactly one variant set — zero-valued blocks fail JSON marshaling,
// causing log.Append to drop the event silently (fail-closed).
func (t *Translator) emitBlockEnd(eventType string) {
	ev := apishim.NewContentEvent(eventType, apishim.BlockStatusEnd, apishim.TextBlock(""))
	t.broadcastSessionEvent(ev)
}

// closeOpenBlock closes the currently open content block, if any.
func (t *Translator) closeOpenBlock() {
	if t.currentBlockType != "" {
		t.emitBlockEnd(t.currentBlockType)
		t.currentBlockType = ""
	}
}

// broadcastSessionEvent builds and broadcasts a session category ShimEvent.
// TurnID is applied to all session events when an active turn exists.
func (t *Translator) broadcastSessionEvent(ev apishim.Event) {
	t.broadcast(func(seq int, at time.Time) apishim.ShimEvent {
		eventType := apishim.EventTypeOf(ev)
		se := apishim.ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  apishim.CategorySession,
			Type:      eventType,
			Payload:   ev,
		}
		if t.currentTurnId != "" {
			se.TurnID = t.currentTurnId
		}
		return se
	})
}

// broadcast is the single fan-out entry point. The build callback runs under
// mu.Lock and receives the assigned seq and current timestamp. The lock is held
// for the entire log-then-fanout sequence to guarantee:
//   - log entries and live notifications share the same seq space
//   - concurrent broadcasts do not interleave their seq numbers
//
// Fail-closed: if log.Append fails, the event is dropped (not fanned out) and
// nextSeq is NOT incremented. The next event reuses the same seq number.
// This preserves seq continuity for the history/live recovery invariant.
// Append failures are logged as structured errors for monitoring.
func (t *Translator) broadcast(build func(seq int, at time.Time) apishim.ShimEvent) {
	t.mu.Lock()

	// If the translator is stopped, channels may already be closed — bail out.
	select {
	case <-t.done:
		t.mu.Unlock()
		return
	default:
	}

	ev := build(t.nextSeq, time.Now().UTC())
	ev.Seq = t.nextSeq

	if t.log != nil {
		if err := t.log.Append(ev); err != nil {
			slog.Error("events: log append failed, event dropped",
				"seq", t.nextSeq,
				"type", ev.Type,
				"category", ev.Category,
				"error", err,
			)
			// fail-closed: do NOT increment nextSeq, do NOT fan-out
			t.mu.Unlock()
			return
		}
	}

	t.nextSeq++
	t.eventCounts[ev.Type]++

	// Fan-out while holding the lock so Stop() cannot close channels concurrently.
	// Sends are non-blocking (buffered channel + default case), so no deadlock risk.
	// Slow subscribers are evicted (K8s-style): close channel + remove from map.
	// The client detects the disconnect and reconnects with fromSeq=lastSeq+1.
	for id, ch := range t.subs {
		select {
		case ch <- ev:
		default:
			slog.Warn("events: slow subscriber evicted (channel full)",
				"subID", id, "seq", ev.Seq, "type", ev.Type)
			close(ch)
			delete(t.subs, id)
		}
	}
	t.mu.Unlock()
}

// translate converts a raw SessionNotification into a typed Event.
// All SessionUpdate branches are translated — no branch is silently discarded.
func translate(n acp.SessionNotification) apishim.Event {
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		return apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", u.AgentMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		return apishim.NewContentEvent(apishim.EventTypeAgentThinking, "", u.AgentThoughtChunk.Content)
	case u.UserMessageChunk != nil:
		return apishim.NewContentEvent(apishim.EventTypeUserMessage, "", u.UserMessageChunk.Content)
	case u.ToolCall != nil:
		tc := u.ToolCall
		return apishim.ToolCallEvent{
			Meta:      tc.Meta,
			ID:        string(tc.ToolCallId),
			Kind:      string(tc.Kind),
			Title:     tc.Title,
			Status:    string(tc.Status),
			Content:   convertToolCallContents(tc.Content),
			Locations: convertLocations(tc.Locations),
			RawInput:  tc.RawInput,
			RawOutput: tc.RawOutput,
		}
	case u.ToolCallUpdate != nil:
		tcu := u.ToolCallUpdate
		return apishim.ToolResultEvent{
			Meta:      tcu.Meta,
			ID:        string(tcu.ToolCallId),
			Status:    safeStatus(tcu.Status),
			Kind:      safeToolKind(tcu.Kind),
			Title:     safeStringPtr(tcu.Title),
			Content:   convertToolCallContents(tcu.Content),
			Locations: convertLocations(tcu.Locations),
			RawInput:  tcu.RawInput,
			RawOutput: tcu.RawOutput,
		}
	case u.Plan != nil:
		return apishim.PlanEvent{Meta: u.Plan.Meta, Entries: u.Plan.Entries}
	case u.AvailableCommandsUpdate != nil:
		ac := u.AvailableCommandsUpdate
		return apishim.AvailableCommandsEvent{
			Meta:     ac.Meta,
			Commands: convertCommands(ac.AvailableCommands),
		}
	case u.CurrentModeUpdate != nil:
		cm := u.CurrentModeUpdate
		return apishim.CurrentModeEvent{
			Meta:   cm.Meta,
			ModeID: string(cm.CurrentModeId),
		}
	case u.ConfigOptionUpdate != nil:
		co := u.ConfigOptionUpdate
		return apishim.ConfigOptionEvent{
			Meta:          co.Meta,
			ConfigOptions: convertConfigOptions(co.ConfigOptions),
		}
	case u.SessionInfoUpdate != nil:
		si := u.SessionInfoUpdate
		return apishim.SessionInfoEvent{
			Meta:      si.Meta,
			Title:     si.Title,
			UpdatedAt: si.UpdatedAt,
		}
	case u.UsageUpdate != nil:
		uu := u.UsageUpdate
		return apishim.UsageEvent{
			Meta: uu.Meta,
			Cost: convertCost(uu.Cost),
			Size: uu.Size,
			Used: uu.Used,
		}
	default:
		return apishim.ErrorEvent{Msg: "unknown session update variant"}
	}
}


// ── Convert: ToolCall content & locations ────────────────────────────────────

// convertToolCallContents converts a slice of acp.ToolCallContent.
func convertToolCallContents(contents []acp.ToolCallContent) []apishim.ToolCallContent {
	if len(contents) == 0 {
		return nil
	}
	out := make([]apishim.ToolCallContent, 0, len(contents))
	for _, c := range contents {
		switch {
		case c.Content != nil:
			out = append(out, apishim.ToolCallContent{Content: &apishim.ToolCallContentContent{
				Meta:    c.Content.Meta,
				Content: c.Content.Content,
			}})
		case c.Diff != nil:
			out = append(out, apishim.ToolCallContent{Diff: &apishim.ToolCallContentDiff{
				Meta:    c.Diff.Meta,
				Path:    c.Diff.Path,
				OldText: c.Diff.OldText,
				NewText: c.Diff.NewText,
			}})
		case c.Terminal != nil:
			out = append(out, apishim.ToolCallContent{Terminal: &apishim.ToolCallContentTerminal{
				Meta:       c.Terminal.Meta,
				TerminalID: c.Terminal.TerminalId,
			}})
		}
	}
	return out
}

// convertLocations converts a slice of acp.ToolCallLocation.
func convertLocations(locs []acp.ToolCallLocation) []apishim.ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]apishim.ToolCallLocation, len(locs))
	for i, l := range locs {
		out[i] = apishim.ToolCallLocation{Meta: l.Meta, Path: l.Path, Line: l.Line}
	}
	return out
}

// ── Convert: AvailableCommands ────────────────────────────────────────────────

// convertCommands converts a slice of acp.AvailableCommand.
func convertCommands(cmds []acp.AvailableCommand) []apishim.AvailableCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]apishim.AvailableCommand, len(cmds))
	for i, c := range cmds {
		out[i] = apishim.AvailableCommand{
			Meta:        c.Meta,
			Name:        c.Name,
			Description: c.Description,
			Input:       convertAvailableCommandInput(c.Input),
		}
	}
	return out
}

// convertAvailableCommandInput converts *acp.AvailableCommandInput.
func convertAvailableCommandInput(inp *acp.AvailableCommandInput) *apishim.AvailableCommandInput {
	if inp == nil {
		return nil
	}
	if inp.Unstructured != nil {
		return &apishim.AvailableCommandInput{Unstructured: &apishim.UnstructuredCommandInput{
			Meta: inp.Unstructured.Meta,
			Hint: inp.Unstructured.Hint,
		}}
	}
	return nil
}

// ── Convert: ConfigOptions ───────────────────────────────────────────────────

// convertConfigOptions converts a slice of acp.SessionConfigOption.
func convertConfigOptions(opts []acp.SessionConfigOption) []apishim.ConfigOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]apishim.ConfigOption, 0, len(opts))
	for _, o := range opts {
		if o.Select != nil {
			s := o.Select
			co := apishim.ConfigOption{Select: &apishim.ConfigOptionSelect{
				Meta:         s.Meta,
				ID:           string(s.Id),
				Name:         s.Name,
				CurrentValue: string(s.CurrentValue),
				Description:  s.Description,
				Category:     convertConfigCategory(s.Category),
				Options:      convertConfigSelectOptions(s.Options),
			}}
			out = append(out, co)
		}
	}
	return out
}

// convertConfigCategory converts *acp.SessionConfigOptionCategory to *string.
// ACP category is a union with a single "Other" raw-string variant.
func convertConfigCategory(cat *acp.SessionConfigOptionCategory) *string {
	if cat == nil {
		return nil
	}
	if cat.Other != nil {
		s := string(*cat.Other)
		return &s
	}
	return nil
}

// convertConfigSelectOptions converts acp.SessionConfigSelectOptions.
func convertConfigSelectOptions(opts acp.SessionConfigSelectOptions) apishim.ConfigSelectOptions {
	switch {
	case opts.Grouped != nil:
		groups := make([]apishim.ConfigSelectGroup, len(*opts.Grouped))
		for i, g := range *opts.Grouped {
			groups[i] = apishim.ConfigSelectGroup{
				Meta:    g.Meta,
				Group:   string(g.Group),
				Name:    g.Name,
				Options: convertConfigSelectOptionSlice(g.Options),
			}
		}
		return apishim.ConfigSelectOptions{Grouped: groups}
	case opts.Ungrouped != nil:
		return apishim.ConfigSelectOptions{Ungrouped: convertConfigSelectOptionSlice(*opts.Ungrouped)}
	default:
		return apishim.ConfigSelectOptions{}
	}
}

// convertConfigSelectOptionSlice converts a slice of acp.SessionConfigSelectOption.
func convertConfigSelectOptionSlice(opts []acp.SessionConfigSelectOption) []apishim.ConfigSelectOption {
	out := make([]apishim.ConfigSelectOption, len(opts))
	for i, o := range opts {
		out[i] = apishim.ConfigSelectOption{
			Meta:        o.Meta,
			Name:        o.Name,
			Value:       string(o.Value),
			Description: o.Description,
		}
	}
	return out
}

// ── Convert: Cost ─────────────────────────────────────────────────────────────

// convertCost converts *acp.Cost to *apishim.Cost.
func convertCost(c *acp.Cost) *apishim.Cost {
	if c == nil {
		return nil
	}
	return &apishim.Cost{Amount: c.Amount, Currency: c.Currency}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// safeStatus converts a *ToolCallStatus to a string, returning "unknown" when nil.
func safeStatus(s *acp.ToolCallStatus) string {
	if s == nil {
		return "unknown"
	}
	return string(*s)
}

// safeToolKind converts *acp.ToolKind to string, returning "" when nil.
func safeToolKind(k *acp.ToolKind) string {
	if k == nil {
		return ""
	}
	return string(*k)
}

// safeStringPtr dereferences *string safely, returning "" when nil.
func safeStringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
