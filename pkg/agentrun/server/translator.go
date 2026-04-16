package server

import (
	"log/slog"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
)

// Translator drains ACP session notifications, translates each notification
// into an AgentRunEvent, and fans the result out to all registered subscriber
// channels. If log is non-nil, every AgentRunEvent is also appended to the JSONL
// event log for durable history (log-before-fanout under mutex).
type Translator struct {
	runID     string
	sessionID string // ACP session ID, set after handshake via SetSessionID
	in        <-chan acp.SessionNotification
	log       *EventLog

	// sessionMetadataHook is called after broadcastEvent for runtime_update
	// events that carry metadata fields (availableCommands, configOptions,
	// sessionInfo, currentMode). Set once before Start() — no lock needed.
	sessionMetadataHook func(runapi.Event)

	mu               sync.Mutex
	subs             map[int]chan runapi.AgentRunEvent
	nextID           int
	nextSeq          int
	done             chan struct{}
	once             sync.Once
	currentTurnId    string
	currentBlockType string // event type of currently open content block ("" = none)
	eventCounts      map[string]int
}

// NewTranslator creates a Translator that reads from in.
// runID is the agent run identifier (the agent-run --id value).
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
		subs:        make(map[int]chan runapi.AgentRunEvent),
		nextSeq:     nextSeq,
		done:        make(chan struct{}),
		eventCounts: make(map[string]int),
	}
}

// SetSessionID injects the ACP session ID after the session/new handshake
// completes. This is called by the run command after mgr.Create() succeeds.
func (t *Translator) SetSessionID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = id
}

// SetSessionMetadataHook registers a callback that is invoked after
// broadcastSessionEvent for metadata event types (available_commands,
// config_option, session_info, current_mode). Must be called before Start();
// the field is read without a lock in the run goroutine.
func (t *Translator) SetSessionMetadataHook(hook func(runapi.Event)) {
	t.sessionMetadataHook = hook
}

// maybeNotifyMetadata calls sessionMetadataHook for runtime_update events
// that carry metadata fields (availableCommands, configOptions, sessionInfo,
// currentMode). Called from run() AFTER broadcastEvent returns.
func (t *Translator) maybeNotifyMetadata(ev runapi.Event) {
	if t.sessionMetadataHook == nil {
		return
	}
	ru, ok := ev.(runapi.RuntimeUpdateEvent)
	if !ok {
		return
	}
	if ru.AvailableCommands != nil || ru.ConfigOptions != nil ||
		ru.SessionInfo != nil || ru.CurrentMode != nil {
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

// Subscribe returns a buffered channel that will receive translated AgentRunEvents,
// along with a subscription ID and the next sequence number that could be
// assigned after the subscription is established.
func (t *Translator) Subscribe() (<-chan runapi.AgentRunEvent, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	ch := make(chan runapi.AgentRunEvent, 1024)
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

// NotifyTurnStart broadcasts a turn_start AgentRunEvent.
// The new turnId is assigned atomically inside the broadcast callback,
// which runs under mu.Lock.
func (t *Translator) NotifyTurnStart() {
	newTurnID := uuid.New().String()
	t.broadcast(func(seq int, at time.Time) runapi.AgentRunEvent {
		t.currentTurnId = newTurnID
		return runapi.AgentRunEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Type:      runapi.EventTypeTurnStart,
			TurnID:    t.currentTurnId,
			Payload:   runapi.TurnStartEvent{},
		}
	})
}

// NotifyUserPrompt broadcasts user_message AgentRunEvents so that all
// subscribers (including late-joining chat clients) see the user's prompt.
// Each ContentBlock becomes a separate event, matching the per-block pattern
// used on the response side (agent_message).
// This must be called after NotifyTurnStart and before mgr.Prompt.
func (t *Translator) NotifyUserPrompt(blocks []runapi.ContentBlock) {
	for _, block := range blocks {
		b := block // capture for closure
		t.broadcast(func(seq int, at time.Time) runapi.AgentRunEvent {
			return runapi.AgentRunEvent{
				RunID:     t.runID,
				SessionID: t.sessionID,
				Seq:       seq,
				Time:      at,
				Type:      runapi.EventTypeUserMessage,
				TurnID:    t.currentTurnId,
				Payload:   runapi.NewContentEvent(runapi.EventTypeUserMessage, runapi.BlockStatusStart, b),
			}
		})
	}
}

// NotifyTurnEnd broadcasts a turn_end AgentRunEvent.
// Closes any open content block first, then emits turn_end.
// The current turnId is included in the event and cleared AFTER use so the
// turn_end event itself carries the identifier.
func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
	t.closeOpenBlock()
	t.broadcast(func(seq int, at time.Time) runapi.AgentRunEvent {
		ae := runapi.AgentRunEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Type:      runapi.EventTypeTurnEnd,
			TurnID:    t.currentTurnId,
			Payload:   runapi.TurnEndEvent{StopReason: string(reason)},
		}
		t.currentTurnId = ""
		return ae
	})
}

// NotifyStateChange broadcasts a runtime_update AgentRunEvent with Status field.
// Runtime update events never carry turn fields.
// sessionChanged lists state sections that were updated (e.g. ["agentInfo","capabilities"]
// for the synthetic bootstrap-metadata event). Pass nil for lifecycle-only transitions.
func (t *Translator) NotifyStateChange(previousStatus, status string, pid int, reason string, sessionChanged []string) {
	t.broadcast(func(seq int, at time.Time) runapi.AgentRunEvent {
		return runapi.AgentRunEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Type:      runapi.EventTypeRuntimeUpdate,
			Payload: runapi.RuntimeUpdateEvent{
				Status: &runapi.RuntimeStatus{
					PreviousStatus: previousStatus,
					Status:         status,
					PID:            pid,
					Reason:         reason,
					SessionChanged: sessionChanged,
				},
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
				evType := runapi.EventTypeOf(ev)
				if t.currentBlockType == "" {
					ev = setContentStatus(ev, runapi.BlockStatusStart)
					t.currentBlockType = evType
				} else if t.currentBlockType != evType {
					t.closeOpenBlock()
					ev = setContentStatus(ev, runapi.BlockStatusStart)
					t.currentBlockType = evType
				} else {
					ev = setContentStatus(ev, runapi.BlockStatusStreaming)
				}
			} else {
				t.closeOpenBlock()
			}

			t.broadcastEvent(ev)
			t.maybeNotifyMetadata(ev)
		}
	}
}

// isContentEvent returns true for content block event types.
func isContentEvent(ev runapi.Event) bool {
	_, ok := ev.(runapi.ContentEvent)
	return ok
}

// setContentStatus sets the Status field on a ContentEvent.
func setContentStatus(ev runapi.Event, status string) runapi.Event {
	if ce, ok := ev.(runapi.ContentEvent); ok {
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
	ev := runapi.NewContentEvent(eventType, runapi.BlockStatusEnd, runapi.TextBlock(""))
	t.broadcastEvent(ev)
}

// closeOpenBlock closes the currently open content block, if any.
func (t *Translator) closeOpenBlock() {
	if t.currentBlockType != "" {
		t.emitBlockEnd(t.currentBlockType)
		t.currentBlockType = ""
	}
}

// broadcastEvent builds and broadcasts an AgentRunEvent.
// TurnID is applied to all events except runtime_update when an active turn exists.
func (t *Translator) broadcastEvent(ev runapi.Event) {
	t.broadcast(func(seq int, at time.Time) runapi.AgentRunEvent {
		eventType := runapi.EventTypeOf(ev)
		ae := runapi.AgentRunEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Type:      eventType,
			Payload:   ev,
		}
		if t.currentTurnId != "" && eventType != runapi.EventTypeRuntimeUpdate {
			ae.TurnID = t.currentTurnId
		}
		return ae
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
func (t *Translator) broadcast(build func(seq int, at time.Time) runapi.AgentRunEvent) {
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
func translate(n acp.SessionNotification) runapi.Event {
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		return runapi.NewContentEvent(runapi.EventTypeAgentMessage, "", u.AgentMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		return runapi.NewContentEvent(runapi.EventTypeAgentThinking, "", u.AgentThoughtChunk.Content)
	case u.UserMessageChunk != nil:
		return runapi.NewContentEvent(runapi.EventTypeUserMessage, "", u.UserMessageChunk.Content)
	case u.ToolCall != nil:
		tc := u.ToolCall
		return runapi.ToolCallEvent{
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
		return runapi.ToolResultEvent{
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
		return runapi.PlanEvent{Meta: u.Plan.Meta, Entries: u.Plan.Entries}
	case u.AvailableCommandsUpdate != nil:
		ac := u.AvailableCommandsUpdate
		return runapi.RuntimeUpdateEvent{
			AvailableCommands: &runapi.AvailableCommandsEvent{
				Meta:     ac.Meta,
				Commands: convertCommands(ac.AvailableCommands),
			},
		}
	case u.CurrentModeUpdate != nil:
		cm := u.CurrentModeUpdate
		return runapi.RuntimeUpdateEvent{
			CurrentMode: &runapi.CurrentModeEvent{
				Meta:   cm.Meta,
				ModeID: string(cm.CurrentModeId),
			},
		}
	case u.ConfigOptionUpdate != nil:
		co := u.ConfigOptionUpdate
		return runapi.RuntimeUpdateEvent{
			ConfigOptions: &runapi.ConfigOptionEvent{
				Meta:    co.Meta,
				Options: convertConfigOptions(co.ConfigOptions),
			},
		}
	case u.SessionInfoUpdate != nil:
		si := u.SessionInfoUpdate
		return runapi.RuntimeUpdateEvent{
			SessionInfo: &runapi.SessionInfoEvent{
				Meta:      si.Meta,
				Title:     si.Title,
				UpdatedAt: si.UpdatedAt,
			},
		}
	case u.UsageUpdate != nil:
		uu := u.UsageUpdate
		return runapi.RuntimeUpdateEvent{
			Usage: &runapi.UsageEvent{
				Meta: uu.Meta,
				Cost: convertCost(uu.Cost),
				Size: uu.Size,
				Used: uu.Used,
			},
		}
	default:
		return runapi.ErrorEvent{Msg: "unknown session update variant"}
	}
}


// ── Convert: ToolCall content & locations ────────────────────────────────────

// convertToolCallContents converts a slice of acp.ToolCallContent.
func convertToolCallContents(contents []acp.ToolCallContent) []runapi.ToolCallContent {
	if len(contents) == 0 {
		return nil
	}
	out := make([]runapi.ToolCallContent, 0, len(contents))
	for _, c := range contents {
		switch {
		case c.Content != nil:
			out = append(out, runapi.ToolCallContent{Content: &runapi.ToolCallContentContent{
				Meta:    c.Content.Meta,
				Content: c.Content.Content,
			}})
		case c.Diff != nil:
			out = append(out, runapi.ToolCallContent{Diff: &runapi.ToolCallContentDiff{
				Meta:    c.Diff.Meta,
				Path:    c.Diff.Path,
				OldText: c.Diff.OldText,
				NewText: c.Diff.NewText,
			}})
		case c.Terminal != nil:
			out = append(out, runapi.ToolCallContent{Terminal: &runapi.ToolCallContentTerminal{
				Meta:       c.Terminal.Meta,
				TerminalID: c.Terminal.TerminalId,
			}})
		}
	}
	return out
}

// convertLocations converts a slice of acp.ToolCallLocation.
func convertLocations(locs []acp.ToolCallLocation) []runapi.ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]runapi.ToolCallLocation, len(locs))
	for i, l := range locs {
		out[i] = runapi.ToolCallLocation{Meta: l.Meta, Path: l.Path, Line: l.Line}
	}
	return out
}

// ── Convert: AvailableCommands ────────────────────────────────────────────────

// convertCommands converts a slice of acp.AvailableCommand.
func convertCommands(cmds []acp.AvailableCommand) []runapi.AvailableCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]runapi.AvailableCommand, len(cmds))
	for i, c := range cmds {
		out[i] = runapi.AvailableCommand{
			Meta:        c.Meta,
			Name:        c.Name,
			Description: c.Description,
			Input:       convertAvailableCommandInput(c.Input),
		}
	}
	return out
}

// convertAvailableCommandInput converts *acp.AvailableCommandInput.
func convertAvailableCommandInput(inp *acp.AvailableCommandInput) *runapi.AvailableCommandInput {
	if inp == nil {
		return nil
	}
	if inp.Unstructured != nil {
		return &runapi.AvailableCommandInput{Unstructured: &runapi.UnstructuredCommandInput{
			Meta: inp.Unstructured.Meta,
			Hint: inp.Unstructured.Hint,
		}}
	}
	return nil
}

// ── Convert: ConfigOptions ───────────────────────────────────────────────────

// convertConfigOptions converts a slice of acp.SessionConfigOption.
func convertConfigOptions(opts []acp.SessionConfigOption) []runapi.ConfigOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]runapi.ConfigOption, 0, len(opts))
	for _, o := range opts {
		if o.Select != nil {
			s := o.Select
			co := runapi.ConfigOption{Select: &runapi.ConfigOptionSelect{
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
func convertConfigSelectOptions(opts acp.SessionConfigSelectOptions) runapi.ConfigSelectOptions {
	switch {
	case opts.Grouped != nil:
		groups := make([]runapi.ConfigSelectGroup, len(*opts.Grouped))
		for i, g := range *opts.Grouped {
			groups[i] = runapi.ConfigSelectGroup{
				Meta:    g.Meta,
				Group:   string(g.Group),
				Name:    g.Name,
				Options: convertConfigSelectOptionSlice(g.Options),
			}
		}
		return runapi.ConfigSelectOptions{Grouped: groups}
	case opts.Ungrouped != nil:
		return runapi.ConfigSelectOptions{Ungrouped: convertConfigSelectOptionSlice(*opts.Ungrouped)}
	default:
		return runapi.ConfigSelectOptions{}
	}
}

// convertConfigSelectOptionSlice converts a slice of acp.SessionConfigSelectOption.
func convertConfigSelectOptionSlice(opts []acp.SessionConfigSelectOption) []runapi.ConfigSelectOption {
	out := make([]runapi.ConfigSelectOption, len(opts))
	for i, o := range opts {
		out[i] = runapi.ConfigSelectOption{
			Meta:        o.Meta,
			Name:        o.Name,
			Value:       string(o.Value),
			Description: o.Description,
		}
	}
	return out
}

// ── Convert: Cost ─────────────────────────────────────────────────────────────

// convertCost converts *acp.Cost to *runapi.Cost.
func convertCost(c *acp.Cost) *runapi.Cost {
	if c == nil {
		return nil
	}
	return &runapi.Cost{Amount: c.Amount, Currency: c.Currency}
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
