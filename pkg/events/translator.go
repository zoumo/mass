package events

import (
	"log/slog"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/zoumo/oar/api"
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

	mu            sync.Mutex
	subs          map[int]chan ShimEvent
	nextID        int
	nextSeq       int
	done          chan struct{}
	once          sync.Once
	currentTurnId string
	streamSeq     int
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
		runID:   runID,
		in:      in,
		log:     log,
		subs:    make(map[int]chan ShimEvent),
		nextSeq: nextSeq,
		done:    make(chan struct{}),
	}
}

// SetSessionID injects the ACP session ID after the session/new handshake
// completes. This is called by the shim command after mgr.Create() succeeds.
func (t *Translator) SetSessionID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = id
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
func (t *Translator) Subscribe() (<-chan ShimEvent, int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	id := t.nextID
	t.nextID++
	ch := make(chan ShimEvent, 64)
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
func (t *Translator) SubscribeFromSeq(logPath string, fromSeq int) ([]ShimEvent, <-chan ShimEvent, int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries, err := ReadEventLog(logPath, fromSeq)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if entries == nil {
		entries = []ShimEvent{}
	}

	id := t.nextID
	t.nextID++
	ch := make(chan ShimEvent, 64)
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

// LastSeq returns the last assigned sequence number, or -1 when no event
// has been emitted yet.
func (t *Translator) LastSeq() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextSeq - 1
}

// NotifyTurnStart broadcasts a turn_start ShimEvent.
// The new turnId and initial streamSeq are assigned atomically inside the
// broadcast callback, which runs under mu.Lock.
func (t *Translator) NotifyTurnStart() {
	newTurnID := uuid.New().String()
	t.broadcast(func(seq int, at time.Time) ShimEvent {
		// Runs under mu.Lock — safe to mutate turn state here.
		t.currentTurnId = newTurnID
		t.streamSeq = 0
		return ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  api.CategorySession,
			Type:      api.EventTypeTurnStart,
			TurnID:    t.currentTurnId,
			StreamSeq: t.streamSeq,
			Phase:     "acting",
			Content:   TurnStartEvent{},
		}
	})
}

// NotifyUserPrompt broadcasts a user_message ShimEvent so that all subscribers
// (including late-joining chat clients) see the user's prompt.
// This must be called after NotifyTurnStart and before mgr.Prompt.
func (t *Translator) NotifyUserPrompt(text string) {
	t.broadcast(func(seq int, at time.Time) ShimEvent {
		t.streamSeq++
		return ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  api.CategorySession,
			Type:      api.EventTypeUserMessage,
			TurnID:    t.currentTurnId,
			StreamSeq: t.streamSeq,
			Phase:     "acting",
			Content:   UserMessageEvent{Text: text},
		}
	})
}

// NotifyTurnEnd broadcasts a turn_end ShimEvent.
// The current turnId is included in the event and cleared AFTER use so the
// turn_end event itself carries the identifier.
func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
	t.broadcast(func(seq int, at time.Time) ShimEvent {
		t.streamSeq++
		se := ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  api.CategorySession,
			Type:      api.EventTypeTurnEnd,
			TurnID:    t.currentTurnId,
			StreamSeq: t.streamSeq,
			Phase:     "acting",
			Content:   TurnEndEvent{StopReason: string(reason)},
		}
		t.currentTurnId = "" // Clear AFTER using — turn_end event carries the turnId
		return se
	})
}

// NotifyStateChange broadcasts a runtime category state_change ShimEvent.
// Runtime events never carry turn fields.
func (t *Translator) NotifyStateChange(previousStatus, status string, pid int, reason string) {
	t.broadcast(func(seq int, at time.Time) ShimEvent {
		return ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  api.CategoryRuntime,
			Type:      api.EventTypeStateChange,
			Content: StateChangeEvent{
				PreviousStatus: previousStatus,
				Status:         status,
				PID:            pid,
				Reason:         reason,
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
			t.broadcastSessionEvent(ev)
		}
	}
}

// broadcastSessionEvent builds and broadcasts a session category ShimEvent.
// Turn metadata (TurnID/StreamSeq/Phase) is applied to all session events
// when an active turn exists.
func (t *Translator) broadcastSessionEvent(ev Event) {
	t.broadcast(func(seq int, at time.Time) ShimEvent {
		eventType := ev.eventType()
		se := ShimEvent{
			RunID:     t.runID,
			SessionID: t.sessionID,
			Seq:       seq,
			Time:      at,
			Category:  api.CategorySession,
			Type:      eventType,
			Content:   ev,
		}
		// Apply turn metadata to all session events when inside an active turn.
		if t.currentTurnId != "" {
			t.streamSeq++
			se.TurnID = t.currentTurnId
			se.StreamSeq = t.streamSeq
			se.Phase = PhaseForEvent(eventType)
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
func (t *Translator) broadcast(build func(seq int, at time.Time) ShimEvent) {
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

	// Fan-out while holding the lock so Stop() cannot close channels concurrently.
	// Sends are non-blocking (buffered channel + default case), so no deadlock risk.
	for _, ch := range t.subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber — drop rather than block fan-out.
		}
	}
	t.mu.Unlock()
}

// translate converts a raw SessionNotification into a typed Event.
// All SessionUpdate branches are translated — no branch is silently discarded.
func translate(n acp.SessionNotification) Event {
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		c := u.AgentMessageChunk
		return TextEvent{
			Text:    safeBlockText(c.Content),
			Content: convertContentBlock(c.Content),
		}
	case u.AgentThoughtChunk != nil:
		c := u.AgentThoughtChunk
		return ThinkingEvent{
			Text:    safeBlockText(c.Content),
			Content: convertContentBlock(c.Content),
		}
	case u.UserMessageChunk != nil:
		c := u.UserMessageChunk
		return UserMessageEvent{
			Text:    safeBlockText(c.Content),
			Content: convertContentBlock(c.Content),
		}
	case u.ToolCall != nil:
		tc := u.ToolCall
		return ToolCallEvent{
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
		return ToolResultEvent{
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
		return PlanEvent{Meta: u.Plan.Meta, Entries: u.Plan.Entries}
	case u.AvailableCommandsUpdate != nil:
		ac := u.AvailableCommandsUpdate
		return AvailableCommandsEvent{
			Meta:     ac.Meta,
			Commands: convertCommands(ac.AvailableCommands),
		}
	case u.CurrentModeUpdate != nil:
		cm := u.CurrentModeUpdate
		return CurrentModeEvent{
			Meta:   cm.Meta,
			ModeID: string(cm.CurrentModeId),
		}
	case u.ConfigOptionUpdate != nil:
		co := u.ConfigOptionUpdate
		return ConfigOptionEvent{
			Meta:          co.Meta,
			ConfigOptions: convertConfigOptions(co.ConfigOptions),
		}
	case u.SessionInfoUpdate != nil:
		si := u.SessionInfoUpdate
		return SessionInfoEvent{
			Meta:      si.Meta,
			Title:     si.Title,
			UpdatedAt: si.UpdatedAt,
		}
	case u.UsageUpdate != nil:
		uu := u.UsageUpdate
		return UsageEvent{
			Meta: uu.Meta,
			Cost: convertCost(uu.Cost),
			Size: uu.Size,
			Used: uu.Used,
		}
	default:
		return ErrorEvent{Msg: "unknown session update variant"}
	}
}

// ── Helper: text extraction ───────────────────────────────────────────────────

// safeBlockText extracts the text string from a ContentBlock, returning ""
// if the Text variant is nil.
func safeBlockText(cb acp.ContentBlock) string {
	if cb.Text != nil {
		return cb.Text.Text
	}
	return ""
}

// ── Convert: ContentBlock ─────────────────────────────────────────────────────

// convertContentBlock converts an acp.ContentBlock to the OAR mirror type.
// Returns nil if the block has no active variant.
func convertContentBlock(cb acp.ContentBlock) *ContentBlock {
	switch {
	case cb.Text != nil:
		return &ContentBlock{Text: &TextContent{
			Meta:        cb.Text.Meta,
			Text:        cb.Text.Text,
			Annotations: convertAnnotations(cb.Text.Annotations),
		}}
	case cb.Image != nil:
		return &ContentBlock{Image: &ImageContent{
			Meta:        cb.Image.Meta,
			Data:        cb.Image.Data,
			MimeType:    cb.Image.MimeType,
			URI:         cb.Image.Uri,
			Annotations: convertAnnotations(cb.Image.Annotations),
		}}
	case cb.Audio != nil:
		return &ContentBlock{Audio: &AudioContent{
			Meta:        cb.Audio.Meta,
			Data:        cb.Audio.Data,
			MimeType:    cb.Audio.MimeType,
			Annotations: convertAnnotations(cb.Audio.Annotations),
		}}
	case cb.ResourceLink != nil:
		return &ContentBlock{ResourceLink: &ResourceLinkContent{
			Meta:        cb.ResourceLink.Meta,
			URI:         cb.ResourceLink.Uri,
			Name:        cb.ResourceLink.Name,
			Description: cb.ResourceLink.Description,
			MimeType:    cb.ResourceLink.MimeType,
			Title:       cb.ResourceLink.Title,
			Size:        cb.ResourceLink.Size,
			Annotations: convertAnnotations(cb.ResourceLink.Annotations),
		}}
	case cb.Resource != nil:
		return &ContentBlock{Resource: &ResourceContent{
			Meta:        cb.Resource.Meta,
			Resource:    convertEmbeddedResource(cb.Resource.Resource),
			Annotations: convertAnnotations(cb.Resource.Annotations),
		}}
	default:
		return nil
	}
}

// convertAnnotations converts *acp.Annotations to *Annotations.
func convertAnnotations(a *acp.Annotations) *Annotations {
	if a == nil {
		return nil
	}
	ann := &Annotations{
		Meta:         a.Meta,
		LastModified: a.LastModified,
		Priority:     a.Priority,
	}
	for _, r := range a.Audience {
		ann.Audience = append(ann.Audience, string(r))
	}
	return ann
}

// convertEmbeddedResource converts acp.EmbeddedResourceResource to EmbeddedResource.
func convertEmbeddedResource(r acp.EmbeddedResourceResource) EmbeddedResource {
	switch {
	case r.TextResourceContents != nil:
		return EmbeddedResource{TextResource: &TextResourceContents{
			Meta:     r.TextResourceContents.Meta,
			URI:      r.TextResourceContents.Uri,
			MimeType: r.TextResourceContents.MimeType,
			Text:     r.TextResourceContents.Text,
		}}
	case r.BlobResourceContents != nil:
		return EmbeddedResource{BlobResource: &BlobResourceContents{
			Meta:     r.BlobResourceContents.Meta,
			URI:      r.BlobResourceContents.Uri,
			MimeType: r.BlobResourceContents.MimeType,
			Blob:     r.BlobResourceContents.Blob,
		}}
	default:
		return EmbeddedResource{}
	}
}

// ── Convert: ToolCall content & locations ────────────────────────────────────

// convertToolCallContents converts a slice of acp.ToolCallContent.
func convertToolCallContents(contents []acp.ToolCallContent) []ToolCallContent {
	if len(contents) == 0 {
		return nil
	}
	out := make([]ToolCallContent, 0, len(contents))
	for _, c := range contents {
		switch {
		case c.Content != nil:
			out = append(out, ToolCallContent{Content: &ToolCallContentContent{
				Meta:    c.Content.Meta,
				Content: convertContentBlockValue(c.Content.Content),
			}})
		case c.Diff != nil:
			out = append(out, ToolCallContent{Diff: &ToolCallContentDiff{
				Meta:    c.Diff.Meta,
				Path:    c.Diff.Path,
				OldText: c.Diff.OldText,
				NewText: c.Diff.NewText,
			}})
		case c.Terminal != nil:
			out = append(out, ToolCallContent{Terminal: &ToolCallContentTerminal{
				Meta:       c.Terminal.Meta,
				TerminalID: c.Terminal.TerminalId,
			}})
		}
	}
	return out
}

// convertContentBlockValue converts acp.ContentBlock by value (not pointer).
func convertContentBlockValue(cb acp.ContentBlock) ContentBlock {
	p := convertContentBlock(cb)
	if p == nil {
		return ContentBlock{}
	}
	return *p
}

// convertLocations converts a slice of acp.ToolCallLocation.
func convertLocations(locs []acp.ToolCallLocation) []ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]ToolCallLocation, len(locs))
	for i, l := range locs {
		out[i] = ToolCallLocation{Meta: l.Meta, Path: l.Path, Line: l.Line}
	}
	return out
}

// ── Convert: AvailableCommands ────────────────────────────────────────────────

// convertCommands converts a slice of acp.AvailableCommand.
func convertCommands(cmds []acp.AvailableCommand) []AvailableCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]AvailableCommand, len(cmds))
	for i, c := range cmds {
		out[i] = AvailableCommand{
			Meta:        c.Meta,
			Name:        c.Name,
			Description: c.Description,
			Input:       convertAvailableCommandInput(c.Input),
		}
	}
	return out
}

// convertAvailableCommandInput converts *acp.AvailableCommandInput.
func convertAvailableCommandInput(inp *acp.AvailableCommandInput) *AvailableCommandInput {
	if inp == nil {
		return nil
	}
	if inp.Unstructured != nil {
		return &AvailableCommandInput{Unstructured: &UnstructuredCommandInput{
			Meta: inp.Unstructured.Meta,
			Hint: inp.Unstructured.Hint,
		}}
	}
	return nil
}

// ── Convert: ConfigOptions ───────────────────────────────────────────────────

// convertConfigOptions converts a slice of acp.SessionConfigOption.
func convertConfigOptions(opts []acp.SessionConfigOption) []ConfigOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]ConfigOption, 0, len(opts))
	for _, o := range opts {
		if o.Select != nil {
			s := o.Select
			co := ConfigOption{Select: &ConfigOptionSelect{
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
func convertConfigSelectOptions(opts acp.SessionConfigSelectOptions) ConfigSelectOptions {
	switch {
	case opts.Grouped != nil:
		groups := make([]ConfigSelectGroup, len(*opts.Grouped))
		for i, g := range *opts.Grouped {
			groups[i] = ConfigSelectGroup{
				Meta:    g.Meta,
				Group:   string(g.Group),
				Name:    g.Name,
				Options: convertConfigSelectOptionSlice(g.Options),
			}
		}
		return ConfigSelectOptions{Grouped: groups}
	case opts.Ungrouped != nil:
		return ConfigSelectOptions{Ungrouped: convertConfigSelectOptionSlice(*opts.Ungrouped)}
	default:
		return ConfigSelectOptions{}
	}
}

// convertConfigSelectOptionSlice converts a slice of acp.SessionConfigSelectOption.
func convertConfigSelectOptionSlice(opts []acp.SessionConfigSelectOption) []ConfigSelectOption {
	out := make([]ConfigSelectOption, len(opts))
	for i, o := range opts {
		out[i] = ConfigSelectOption{
			Meta:        o.Meta,
			Name:        o.Name,
			Value:       string(o.Value),
			Description: o.Description,
		}
	}
	return out
}

// ── Convert: Cost ─────────────────────────────────────────────────────────────

// convertCost converts *acp.Cost to *Cost.
func convertCost(c *acp.Cost) *Cost {
	if c == nil {
		return nil
	}
	return &Cost{Amount: c.Amount, Currency: c.Currency}
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
