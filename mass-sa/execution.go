package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
)

const (
	historyPageLimit = 100
	maxHistoryPages  = 1000
)

// WorkerRunResult reports one accepted prompt, its final complete message, and
// the durable ticket/router records produced from it.
type WorkerRunResult struct {
	Worker               WorkerView   `json:"worker"`
	AssignmentID         string       `json:"assignmentId"`
	ExecutionID          string       `json:"executionId"`
	TicketID             string       `json:"ticketId"`
	InputEventIDs        []string     `json:"inputEventIds"`
	SourceEventID        string       `json:"sourceEventId"`
	SourceItemID         string       `json:"sourceItemId,omitempty"`
	SourceTurnID         string       `json:"sourceTurnId,omitempty"`
	ReportCommentID      string       `json:"reportCommentId"`
	RouterInboxEventID   string       `json:"routerInboxEventId"`
	ReportText           string       `json:"reportText"`
	ReportFallback       bool         `json:"reportFallback"`
	Structured           bool         `json:"structured"`
	ParseError           string       `json:"parseError,omitempty"`
	RecoveredFromHistory bool         `json:"recoveredFromHistory"`
	FinalEvent           SessionEvent `json:"finalEvent"`
}

// ExecuteWorkerForProject executes the first eligible batch for one worker. It
// uses the same prompt, subscription, report, and ticket path as the automatic
// dispatcher; callers that already selected a durable batch use the dispatcher.
func (s *WorkerService) ExecuteWorkerForProject(ctx context.Context, projectID, rootTicketID, workerName string) (WorkerRunResult, error) {
	root, err := s.rootTicket(projectID, rootTicketID)
	if err != nil {
		return WorkerRunResult{}, err
	}
	namespace := WorkerNamespace(projectID, root.ID)
	worker, err := s.worker(projectID, namespace, workerName)
	if err != nil {
		return WorkerRunResult{}, err
	}
	if worker.Session.SessionID == "" {
		return WorkerRunResult{}, operationError("failed_precondition", "worker_session_missing", "worker has no confirmed MASS Session")
	}
	queued, err := s.queuedInboxEvents(projectID, root.ID, worker.AssignmentID, worker.Name)
	if err != nil {
		return WorkerRunResult{}, err
	}
	if len(queued) == 0 {
		return WorkerRunResult{}, operationError("failed_precondition", "worker_work_missing", "worker has no queued work to execute")
	}
	projectConfig, _ := projectConfigByID(s.config, projectID)
	batches, oversized := planInboxBatches(queued, projectConfig.Inbox.MaxBatchSize, projectConfig.Inbox.MaxPromptBytes)
	if len(oversized) != 0 {
		group := deliveryGroupForEvents(projectID, rootTicketID, oversized)
		if err := s.createBlockedBatch(projectID, group, oversized, "prompt_budget_exceeded"); err != nil {
			return WorkerRunResult{}, err
		}
	}
	if len(batches) == 0 {
		return WorkerRunResult{}, operationError("failed_precondition", "worker_work_too_large", "queued events exceed the configured prompt budget")
	}
	return s.executeWorkerEvents(ctx, nil, root.ID, worker, batches[0])
}

func (s *WorkerService) executeWorkerEvents(ctx context.Context, sharedClient sessionClient, rootID string, worker WorkerState, events []InboxEventState) (WorkerRunResult, error) {
	projectID := worker.ProjectID
	if len(events) == 0 {
		return WorkerRunResult{}, operationError("failed_precondition", "worker_work_missing", "worker has no queued work to execute")
	}
	eventIDs := inboxEventIDs(events)
	prompt := workerPromptFromEvents(events)

	release, err := s.holdExecution(worker.AssignmentID)
	if err != nil {
		return WorkerRunResult{}, err
	}
	defer release()

	client := sharedClient
	if client == nil {
		client, err = s.openMASS(ctx)
		if err != nil {
			return WorkerRunResult{}, err
		}
		defer client.Close()
	}
	ref := SessionRef{SessionID: worker.Session.SessionID}
	session, err := client.GetSession(ctx, ref)
	if err != nil {
		return WorkerRunResult{}, err
	}
	if session.Status.State != "idle" && !s.resumedInputReady(projectID, worker.AssignmentID, session) {
		return WorkerRunResult{}, operationError(
			"failed_precondition",
			"worker_not_idle",
			"worker Session must be idle before prompting; current state is "+session.Status.State,
		)
	}

	subscription, err := client.Subscribe(ctx, ref)
	if err != nil {
		return WorkerRunResult{}, err
	}
	defer subscription.Close()
	baselineEvents, err := readAllHistory(ctx, client, ref, []string{"user.message", "agent.message"})
	if err != nil {
		return WorkerRunResult{}, err
	}
	baseline := historyEventIDs(baselineEvents)

	if err := s.beginWorkerExecution(projectID, worker.AssignmentID); err != nil {
		return WorkerRunResult{}, err
	}
	executionID := s.currentExecutionID(projectID, worker.AssignmentID)
	fail := func(state string, err error) (WorkerRunResult, error) {
		_ = s.setExecutionState(projectID, worker.AssignmentID, state, time.Time{})
		return WorkerRunResult{}, err
	}
	group := deliveryGroupForEvents(projectID, rootID, events)
	prepared, prepareErr := s.prepareDelivery(projectID, rootID, group, executionID, events)
	if prepareErr != nil {
		_ = s.setExecutionState(projectID, worker.AssignmentID, "", time.Time{})
		return WorkerRunResult{}, prepareErr
	}
	batch := &prepared
	if _, err := client.PromptSession(ctx, ref, prompt); err != nil {
		state := "failed"
		if IsErrorCode(err, "outcome_unknown") {
			state = "unknown"
		}
		settleErr := s.settleDelivery(projectID, batch.ID, state, operationReason(err), time.Time{}, true)
		return WorkerRunResult{}, errors.Join(err, settleErr)
	}
	promptAcceptedAt := s.now()
	if err := s.settleDelivery(projectID, batch.ID, "delivered", "", promptAcceptedAt, true); err != nil {
		_ = s.settleDelivery(projectID, batch.ID, "unknown", "inbox_state_unconfirmed", time.Time{}, true)
		return WorkerRunResult{}, operationError("outcome_unknown", "inbox_state_unconfirmed", "worker prompt was accepted but delivery state was not confirmed")
	}

	finalEvent, recovered, collectErr := collectSessionFinal(ctx, client, subscription, ref, prompt, baseline, "worker")
	if collectErr != nil {
		state := "failed"
		if IsErrorCode(collectErr, "outcome_unknown") || errors.Is(collectErr, io.EOF) ||
			errors.Is(collectErr, context.DeadlineExceeded) || errors.Is(collectErr, context.Canceled) {
			state = "unknown"
		}
		return fail(state, collectErr)
	}
	reportText, fallbackFromShape := workerReportText(finalEvent)
	structuredResult, parseErr := parseStructuredReport(reportText)
	assignment := s.store.Snapshot().Projects[projectID].Assignments[worker.AssignmentID]
	updatedTicket, err := s.tickets.AddReport(ctx, AddReportRequest{
		ProjectID:     projectID,
		TicketID:      assignment.TicketID,
		AssignmentID:  worker.AssignmentID,
		Session:       worker.Session,
		SourceEventID: finalEvent.EventID,
		SourceItemID:  finalEvent.ItemID,
		SourceTurnID:  observedTurnID(finalEvent),
		Structured:    parseErr == "",
		ParseError:    parseErr,
		Body:          reportText,
	})
	if err != nil {
		state := "failed"
		if IsErrorCode(err, "outcome_unknown") {
			state = "unknown"
		}
		return fail(state, err)
	}
	report, ok := updatedTicket.reportBySource(finalEvent.EventID)
	if !ok || report.CommentID == "" {
		return fail("unknown", operationError("outcome_unknown", "report_unconfirmed", "worker report comment was not confirmed in local state"))
	}
	inboxEventID, err := s.enqueueRouterReport(projectID, rootID, worker, finalEvent, report.CommentID, reportText, structuredResult, parseErr)
	if err != nil {
		return fail("unknown", err)
	}
	if err := s.setExecutionState(projectID, worker.AssignmentID, "completed", time.Time{}); err != nil {
		return WorkerRunResult{}, err
	}
	return WorkerRunResult{
		Worker:               viewWorker(worker),
		AssignmentID:         worker.AssignmentID,
		ExecutionID:          executionID,
		TicketID:             assignment.TicketID,
		InputEventIDs:        eventIDs,
		SourceEventID:        finalEvent.EventID,
		SourceItemID:         finalEvent.ItemID,
		SourceTurnID:         observedTurnID(finalEvent),
		ReportCommentID:      report.CommentID,
		RouterInboxEventID:   inboxEventID,
		ReportText:           reportText,
		ReportFallback:       fallbackFromShape || parseErr != "",
		Structured:           parseErr == "",
		ParseError:           parseErr,
		RecoveredFromHistory: recovered,
		FinalEvent:           finalEvent,
	}, nil
}

func (s *WorkerService) queuedInboxEvents(projectID, rootTicketID, assignmentID, workerName string) ([]InboxEventState, error) {
	project := s.store.Snapshot().Projects[projectID]
	assignment, exists := project.Assignments[assignmentID]
	if !exists || assignment.Status != "active" || assignment.TicketID == "" {
		return nil, operationError("failed_precondition", "assignment_inactive", "worker Assignment is not active")
	}
	ticket, exists := project.Tickets[assignment.TicketID]
	if !exists || ticket.AssignmentID != assignmentID {
		return nil, operationError("failed_precondition", "assignment_stale", "worker Assignment is no longer the ticket's current attempt")
	}
	var events []InboxEventState
	for _, event := range project.Inbox {
		if event.ProjectID != projectID || event.RootTicketID != rootTicketID {
			continue
		}
		if event.State == "queued" {
			resolved, available := resolveQueuedInput(project, event)
			if !available {
				continue
			}
			event = resolved
		}
		if event.TargetAssignmentID != assignmentID || event.TargetWorkerName != workerName || event.State != "queued" {
			continue
		}
		events = append(events, cloneInboxEvent(event))
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].AcceptedAt.Equal(events[j].AcceptedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].AcceptedAt.Before(events[j].AcceptedAt)
	})
	return events, nil
}

func cloneInboxEvent(event InboxEventState) InboxEventState {
	clone := event
	if event.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	return clone
}

func inboxEventIDs(events []InboxEventState) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func workerPromptFromEvents(events []InboxEventState) string {
	var prompt strings.Builder
	prompt.WriteString("Process the following inbox events in acceptance order. Use the event IDs as stable references and include them in your final report.\n")
	for _, event := range events {
		prompt.WriteString("\n### Event ")
		prompt.WriteString(event.ID)
		if event.TriggerID != "" {
			fmt.Fprintf(&prompt, "\n- Trigger ID: %s\n- Thread ID: %s\n- Source event ID: %s\n- Ticket ID: %s", event.TriggerID, event.ThreadID, event.SourceEventID, event.TicketID)
		}
		if event.ChildLifecycle != nil {
			fmt.Fprintf(&prompt, "\n- Parent Ticket ID: %s\n- Confirmed child lifecycle fact; read context as needed. This fact grants no new work and does not reopen or complete the parent.", event.TicketID)
		}
		prompt.WriteString("\n- Source: ")
		prompt.WriteString(event.Source)
		prompt.WriteString("\n- Kind: ")
		prompt.WriteString(event.Kind)
		prompt.WriteString("\n- Accepted at: ")
		prompt.WriteString(event.AcceptedAt.Format(time.RFC3339))
		prompt.WriteString("\n- Summary: ")
		prompt.WriteString(event.Summary)
		prompt.WriteString("\n- Payload:\n\n```json\n")
		if len(bytes.TrimSpace(event.Payload)) == 0 {
			prompt.WriteString("{}")
		} else {
			prompt.Write(event.Payload)
		}
		prompt.WriteString("\n```\n")
	}
	return prompt.String()
}

func (s *WorkerService) holdExecution(assignmentID string) (func(), error) {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if _, exists := s.executions[assignmentID]; exists {
		return nil, operationError("already_exists", "worker_execution_in_progress", "worker Assignment already has an execution in progress")
	}
	s.executions[assignmentID] = struct{}{}
	return func() {
		s.executionMu.Lock()
		delete(s.executions, assignmentID)
		s.executionMu.Unlock()
	}, nil
}

func (s *WorkerService) beginWorkerExecution(projectID, assignmentID string) error {
	executionID, err := s.newID("x")
	if err != nil {
		return err
	}
	now := s.now()
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		assignment, exists := project.Assignments[assignmentID]
		if !exists || assignment.ProjectID != projectID {
			return workerNotFound()
		}
		if assignment.Status != "active" {
			return operationError("failed_precondition", "assignment_inactive", "worker Assignment is not active")
		}
		ticket, exists := project.Tickets[assignment.TicketID]
		if !exists || ticket.AssignmentID != assignmentID {
			return operationError("failed_precondition", "assignment_stale", "worker Assignment is no longer the ticket's current attempt")
		}
		if terminalBusinessState(ticket.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot execute workers")
		}
		switch assignment.ExecutionState {
		case "", "completed":
		case "running", "prompted":
			return operationError("already_exists", "worker_execution_in_progress", "worker Assignment already has an execution in progress")
		default:
			return operationError("failed_precondition", "worker_execution_unconfirmed", "worker Assignment has an unconfirmed prior execution; inspect it before using a new Assignment")
		}
		assignment.ExecutionID = executionID
		assignment.ExecutionState = "running"
		assignment.AwaitingResumedInput = false
		assignment.PromptAcceptedAt = time.Time{}
		assignment.UpdatedAt = now
		project.Assignments[assignmentID] = assignment
		return nil
	})
}

func (s *WorkerService) currentExecutionID(projectID, assignmentID string) string {
	return s.store.Snapshot().Projects[projectID].Assignments[assignmentID].ExecutionID
}

func (s *WorkerService) setExecutionState(projectID, assignmentID, state string, promptAcceptedAt time.Time) error {
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		assignment, exists := project.Assignments[assignmentID]
		if !exists {
			return workerNotFound()
		}
		assignment.ExecutionState = state
		if !promptAcceptedAt.IsZero() {
			assignment.PromptAcceptedAt = promptAcceptedAt
		}
		assignment.UpdatedAt = s.now()
		project.Assignments[assignmentID] = assignment
		return nil
	})
}

func collectSessionFinal(ctx context.Context, client sessionClient, subscription sessionEvent, ref SessionRef, prompt string, baseline map[string]struct{}, role string) (SessionEvent, bool, error) {
	var candidates finalCandidates
	// collectSessionFinal is entered only after session.prompt was accepted. Any
	// subsequent idle boundary can therefore terminate this turn, but idle alone
	// still never counts as success without a complete message.
	turnStarted := true
	seen := make(map[string]struct{}, len(baseline))
	for eventID := range baseline {
		seen[eventID] = struct{}{}
	}
	var terminal error
	for {
		event, err := subscription.Next(ctx)
		if err != nil {
			terminal = err
			break
		}
		if event.Replay != nil {
			continue
		}
		if _, exists := seen[event.EventID]; exists {
			continue
		}
		seen[event.EventID] = struct{}{}
		switch event.Type {
		case "user.message":
			if text, ok := event.textContent(); ok && strings.TrimSpace(text) == strings.TrimSpace(prompt) {
				turnStarted = true
				candidates.reset()
			}
		case "session.state_update":
			state, ok := eventState(event)
			if !ok {
				continue
			}
			switch state {
			case "running":
				turnStarted = true
			case "idle":
				if !turnStarted {
					continue
				}
				if candidate, ok := candidates.chosen(); ok {
					return candidate, false, nil
				}
				return recoverFinalMessage(ctx, client, event.SessionID, prompt, baseline, role)
			case "unknown":
				return SessionEvent{}, false, operationError("outcome_unknown", role+"_state_unknown", role+" Session entered unknown state before a complete final message")
			}
		case "agent.message":
			turnStarted = true
			candidates.consider(event)
		case "agent.message_chunk":
			continue
		case "session.error":
			if isRecoverableSessionGap(event) {
				slog.Default().Warn(
					"mass-sa session observation gap",
					"role", role,
					"sessionId", event.SessionID,
					"eventId", event.EventID,
					"reason", sessionErrorReason(event),
				)
				continue
			}
			return SessionEvent{}, false, sessionEventError(event)
		}
	}

	session, getErr := client.GetSession(ctx, ref)
	if getErr == nil && session.Status.State == "idle" && turnStarted {
		if candidate, ok := candidates.chosen(); ok {
			return candidate, false, nil
		}
		return recoverFinalMessage(ctx, client, session.Status.SessionID, prompt, baseline, role)
	}
	if errors.Is(terminal, io.EOF) {
		return SessionEvent{}, false, operationError("outcome_unknown", role+"_subscription_closed", role+" subscription closed without a complete final message and idle Session")
	}
	if terminal == nil {
		terminal = operationError("outcome_unknown", role+"_result_unknown", role+" final message was not observed")
	}
	return SessionEvent{}, false, terminal
}

type finalCandidates struct {
	final *SessionEvent
	other *SessionEvent
}

func (c *finalCandidates) reset() {
	c.final = nil
	c.other = nil
}

func (c *finalCandidates) consider(event SessionEvent) {
	copied := event
	switch messagePhase(event) {
	case "final_answer":
		c.final = &copied
	case "":
		if completeMessageHasContent(event) {
			c.other = &copied
		}
	}
}

func (c finalCandidates) chosen() (SessionEvent, bool) {
	if c.final != nil {
		return *c.final, true
	}
	if c.other != nil {
		return *c.other, true
	}
	return SessionEvent{}, false
}

func completeMessageHasContent(event SessionEvent) bool {
	var payload struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return false
	}
	return len(payload.Content) > 0
}

func recoverFinalMessage(ctx context.Context, client sessionClient, sessionID, prompt string, baseline map[string]struct{}, role string) (SessionEvent, bool, error) {
	if sessionID == "" {
		return SessionEvent{}, false, operationError("failed_precondition", role+"_history_unavailable", role+" Session identity is unavailable for history recovery")
	}
	events, err := readAllHistory(ctx, client, SessionRef{SessionID: sessionID}, []string{"user.message", "agent.message"})
	if err != nil {
		return SessionEvent{}, false, err
	}
	var currentPrompt bool
	var candidates finalCandidates
	for _, event := range events {
		if _, existed := baseline[event.EventID]; existed || event.Replay != nil {
			continue
		}
		if event.Type == "user.message" {
			text, ok := event.textContent()
			if ok && strings.TrimSpace(text) == strings.TrimSpace(prompt) {
				currentPrompt = true
				candidates.reset()
			}
			continue
		}
		if event.Type == "agent.message" && currentPrompt {
			candidates.consider(event)
		}
	}
	if candidate, ok := candidates.chosen(); ok {
		return candidate, true, nil
	}
	return SessionEvent{}, false, operationError("failed_precondition", role+"_final_missing", "no complete final agent message was found for the accepted prompt")
}

func readAllHistory(ctx context.Context, client sessionClient, ref SessionRef, types []string) ([]SessionEvent, error) {
	var events []SessionEvent
	after := ""
	seenCursors := map[string]struct{}{}
	for pageNumber := 0; pageNumber < maxHistoryPages; pageNumber++ {
		page, err := client.History(ctx, ref, HistoryQuery{Types: types, After: after, Limit: historyPageLimit})
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if page.Next == "" {
			return events, nil
		}
		if _, exists := seenCursors[page.Next]; exists {
			return nil, operationError("unavailable", "history_cursor_cycle", "MASS history returned a repeated cursor")
		}
		seenCursors[page.Next] = struct{}{}
		after = page.Next
	}
	return nil, operationError("resource_exhausted", "history_page_limit", "MASS history exceeded the recovery page limit")
}

func historyEventIDs(events []SessionEvent) map[string]struct{} {
	ids := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event.EventID != "" {
			ids[event.EventID] = struct{}{}
		}
	}
	return ids
}

func eventState(event SessionEvent) (string, bool) {
	var payload struct {
		State *string `json:"state"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.State == nil {
		return "", false
	}
	return *payload.State, true
}

func messagePhase(event SessionEvent) string {
	var payload struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Phase)
}

func sessionEventError(event SessionEvent) error {
	var payload struct {
		Message string `json:"message"`
		Reason  string `json:"reason,omitempty"`
	}
	_ = json.Unmarshal(event.Payload, &payload)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "worker Session reported an error before a complete final message"
	}
	reason := payload.Reason
	if reason == "" {
		reason = "worker_session_error"
	}
	return operationError("failed_precondition", reason, message)
}

func isRecoverableSessionGap(event SessionEvent) bool {
	switch sessionErrorReason(event) {
	case "native_event_unsupported", "native_relationship_gap", "native_observation_missing":
		return true
	default:
		return false
	}
}

func sessionErrorReason(event SessionEvent) string {
	var payload struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = json.Unmarshal(event.Payload, &payload)
	return payload.Reason
}

func observedTurnID(event SessionEvent) string {
	for _, key := range []string{"turnId", "turn_id"} {
		if value, ok := event.Meta[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func workerReportText(event SessionEvent) (string, bool) {
	if text, ok := event.textContent(); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text), false
	}
	raw, err := json.MarshalIndent(event.Payload, "", "  ")
	if err != nil {
		raw = []byte("<payload unavailable>")
	}
	return fmt.Sprintf("Unstructured complete final event; no text ContentBlock was available.\n\n```json\n%s\n```", raw), true
}

func parseStructuredReport(text string) (json.RawMessage, string) {
	candidate := strings.TrimSpace(text)
	if candidate == "" {
		return nil, "final text is empty"
	}
	if strings.HasPrefix(candidate, "```") {
		firstLineEnd := strings.IndexByte(candidate, '\n')
		if firstLineEnd < 0 {
			return nil, "JSON fence is incomplete"
		}
		language := strings.TrimSpace(strings.TrimPrefix(candidate[:firstLineEnd], "```"))
		if language != "" && !strings.EqualFold(language, "json") {
			return nil, "fenced block is not JSON"
		}
		body := candidate[firstLineEnd+1:]
		closeAt := strings.LastIndex(body, "```")
		if closeAt < 0 {
			return nil, "JSON fence is incomplete"
		}
		candidate = strings.TrimSpace(body[:closeAt])
	}
	if candidate == "" || (candidate[0] != '{' && candidate[0] != '[') {
		return nil, "final text is not a JSON object or array"
	}
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "final JSON is invalid: " + err.Error()
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "final JSON contains trailing data"
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		return nil, "final JSON must be an object or array"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "final JSON cannot be normalized"
	}
	return raw, ""
}

func (s *WorkerService) enqueueRouterReport(projectID, rootTicketID string, worker WorkerState, event SessionEvent, commentID, reportText string, structuredResult json.RawMessage, parseErr string) (string, error) {
	eventID, err := s.newID("e")
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"ticketId":      worker.TicketID,
		"assignmentId":  worker.AssignmentID,
		"sessionId":     worker.Session.SessionID,
		"session":       worker.Session.Namespace + "/" + worker.Session.Name,
		"commentId":     commentID,
		"sourceEventId": event.EventID,
		"sourceItemId":  event.ItemID,
		"sourceTurnId":  observedTurnID(event),
		"structured":    parseErr == "",
		"report":        reportText,
	}
	if parseErr != "" {
		payload["parseError"] = parseErr
	}
	if structuredResult != nil {
		payload["structuredResult"] = structuredResult
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", operationError("invalid_argument", "invalid_worker_report", "worker report event cannot be encoded")
	}
	err = s.store.updateProject(projectID, func(project *ProjectState) error {
		for _, existing := range project.Inbox {
			if existing.Kind == "worker_report" && existing.SourceEventID == event.EventID &&
				existing.TargetAssignmentID == worker.AssignmentID {
				eventID = existing.ID
				return nil
			}
		}
		if project.Router == nil || project.Router.SessionID == "" {
			return operationError("failed_precondition", "router_session_missing", "project router Session is not available for worker reports")
		}
		now := s.now()
		project.NextInboxSeq++
		project.Inbox[eventID] = InboxEventState{
			ID:                 eventID,
			Sequence:           project.NextInboxSeq,
			ProjectID:          projectID,
			RootTicketID:       rootTicketID,
			Source:             "worker:" + worker.Name,
			Kind:               "worker_report",
			Summary:            "Worker report for ticket " + worker.TicketID,
			Payload:            rawPayload,
			TargetAssignmentID: worker.AssignmentID,
			TargetWorkerName:   project.Router.Name,
			TargetSession:      project.Router.SessionRef,
			RoutingPath:        "router",
			State:              "queued",
			SourceEventID:      event.EventID,
			AcceptedAt:         now,
			UpdatedAt:          now,
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return eventID, nil
}
