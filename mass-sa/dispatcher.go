package sa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDispatcherPollInterval  = 250 * time.Millisecond
	defaultDispatcherMaxConcurrent = 4
)

// EventDispatcher batches durably accepted events and delivers one prompt per
// target and root when that Session is idle.
type EventDispatcher struct {
	service       *WorkerService
	pollInterval  time.Duration
	maxConcurrent int
	now           func() time.Time
	wait          func(context.Context, time.Duration) error

	inFlightMu sync.Mutex
	inFlight   map[string]struct{}
	semaphore  chan struct{}
	background sync.WaitGroup
}

// EventDispatcherOption customizes the dispatcher clock and wait seam.
type EventDispatcherOption func(*EventDispatcher)

// WithDispatcherPollInterval changes the idle dispatcher polling interval.
func WithDispatcherPollInterval(interval time.Duration) EventDispatcherOption {
	return func(dispatcher *EventDispatcher) {
		dispatcher.pollInterval = interval
	}
}

// WithDispatcherMaxConcurrency bounds prompts that may wait for remote turns at
// the same time.
func WithDispatcherMaxConcurrency(limit int) EventDispatcherOption {
	return func(dispatcher *EventDispatcher) {
		dispatcher.maxConcurrent = limit
	}
}

// WithDispatcherClock replaces the dispatcher clock for deterministic tests.
func WithDispatcherClock(now func() time.Time) EventDispatcherOption {
	return func(dispatcher *EventDispatcher) {
		dispatcher.now = now
	}
}

// WithDispatcherWait replaces the dispatcher timer for deterministic tests.
func WithDispatcherWait(wait func(context.Context, time.Duration) error) EventDispatcherOption {
	return func(dispatcher *EventDispatcher) {
		dispatcher.wait = wait
	}
}

// DispatchCycle reports one non-blocking pass over the configured projects.
type DispatchCycle struct {
	Attempted int `json:"attempted"`
	Delivered int `json:"delivered"`
	Deferred  int `json:"deferred"`
	Blocked   int `json:"blocked"`
}

type queuedDeliveryGroup struct {
	projectID          string
	rootTicketID       string
	routingPath        string
	targetAssignmentID string
	targetWorkerName   string
	targetSession      SessionRef
	events             []InboxEventState
}

// NewEventDispatcher validates its timing options and binds one WorkerService.
func NewEventDispatcher(service *WorkerService, options ...EventDispatcherOption) (*EventDispatcher, error) {
	if service == nil {
		return nil, operationError("invalid_argument", "invalid_dispatcher", "worker service is required")
	}
	dispatcher := &EventDispatcher{
		service:       service,
		pollInterval:  defaultDispatcherPollInterval,
		maxConcurrent: defaultDispatcherMaxConcurrent,
		now:           service.now,
		inFlight:      map[string]struct{}{},
		semaphore:     make(chan struct{}, defaultDispatcherMaxConcurrent),
		wait: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	for _, option := range options {
		option(dispatcher)
	}
	if dispatcher.pollInterval <= 0 {
		return nil, operationError("invalid_argument", "invalid_dispatcher", "dispatcher poll interval must be positive")
	}
	if dispatcher.maxConcurrent <= 0 {
		return nil, operationError("invalid_argument", "invalid_dispatcher", "dispatcher concurrency must be positive")
	}
	dispatcher.semaphore = make(chan struct{}, dispatcher.maxConcurrent)
	if dispatcher.now == nil || dispatcher.wait == nil {
		return nil, operationError("invalid_argument", "invalid_dispatcher", "dispatcher clock and wait function are required")
	}
	return dispatcher, nil
}

// Run recovers ambiguous write-ahead dispatches once, then keeps delivering
// queued events until cancellation. Per-cycle delivery errors do not stop the
// loop, but startup recovery errors do because automatic duplicate delivery
// must not risk an uncertain prompt.
func (d *EventDispatcher) Run(ctx context.Context) error {
	if err := d.service.RecoverDispatches(); err != nil {
		return err
	}
	for {
		groups, discoveryErrors, err := d.collectGroups(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Default().Warn("mass-sa dispatcher cycle failed", "error", err)
			}
		} else {
			for _, discoveryErr := range discoveryErrors {
				slog.Default().Warn("mass-sa dispatcher discovery failed", "error", discoveryErr)
			}
			_, _, _ = d.scheduleGroups(ctx, groups, false)
		}
		if err := d.wait(ctx, d.pollInterval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				d.background.Wait()
				return nil
			}
			d.background.Wait()
			return err
		}
	}
}

// DispatchOnce processes at most one batch per target Session and root. The
// merge window is measured from event acceptance; running or transitional
// Sessions keep their events queued for a later pass.
func (d *EventDispatcher) DispatchOnce(ctx context.Context) (DispatchCycle, error) {
	var cycle DispatchCycle
	groups, discoveryErrors, err := d.collectGroups(ctx)
	if err != nil {
		return cycle, errors.Join(append(discoveryErrors, err)...)
	}
	cycle, cycleErrors, schedulingErr := d.scheduleGroups(ctx, groups, true)
	return cycle, errors.Join(append(discoveryErrors, append(cycleErrors, schedulingErr)...)...)
}

func (d *EventDispatcher) collectGroups(ctx context.Context) ([]queuedDeliveryGroup, []error, error) {
	projects := append([]ProjectConfig(nil), d.service.config.Projects...)
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	var groups []queuedDeliveryGroup
	var discoveryErrors []error
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, discoveryErrors, err
		}
		found, err := d.queuedGroups(project)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("project %s: %w", project.ID, err))
			continue
		}
		groups = append(groups, found...)
	}
	return groups, discoveryErrors, nil
}

func (d *EventDispatcher) scheduleGroups(ctx context.Context, groups []queuedDeliveryGroup, wait bool) (DispatchCycle, []error, error) {
	var cycle DispatchCycle
	var cycleErrors []error
	var errorsMu sync.Mutex
	var waitGroup sync.WaitGroup
	var schedulingErr error
	for _, group := range groups {
		targetKey := group.projectID + "\x00" + group.targetSession.SessionID
		d.inFlightMu.Lock()
		if _, exists := d.inFlight[targetKey]; exists {
			d.inFlightMu.Unlock()
			continue
		}
		d.inFlight[targetKey] = struct{}{}
		d.inFlightMu.Unlock()

		if wait {
			select {
			case d.semaphore <- struct{}{}:
			case <-ctx.Done():
				d.inFlightMu.Lock()
				delete(d.inFlight, targetKey)
				d.inFlightMu.Unlock()
				schedulingErr = ctx.Err()
				waitGroup.Wait()
				return cycle, cycleErrors, schedulingErr
			}
		} else {
			select {
			case d.semaphore <- struct{}{}:
			case <-ctx.Done():
				d.inFlightMu.Lock()
				delete(d.inFlight, targetKey)
				d.inFlightMu.Unlock()
				return cycle, cycleErrors, ctx.Err()
			default:
				d.inFlightMu.Lock()
				delete(d.inFlight, targetKey)
				d.inFlightMu.Unlock()
				continue
			}
		}
		waitGroup.Add(1)
		if !wait {
			d.background.Add(1)
		}
		go func(group queuedDeliveryGroup, targetKey string) {
			defer waitGroup.Done()
			if !wait {
				defer d.background.Done()
			}
			defer func() {
				<-d.semaphore
				d.inFlightMu.Lock()
				delete(d.inFlight, targetKey)
				d.inFlightMu.Unlock()
			}()
			project, exists := projectConfigByID(d.service.config, group.projectID)
			if !exists {
				err := fmt.Errorf("project %s is not configured", group.projectID)
				if wait {
					errorsMu.Lock()
					cycleErrors = append(cycleErrors, err)
					errorsMu.Unlock()
				} else {
					slog.Default().Warn("mass-sa dispatcher target failed", "error", err)
				}
				return
			}
			client, err := d.service.openMASS(ctx)
			if err != nil {
				if wait {
					errorsMu.Lock()
					cycleErrors = append(cycleErrors, err)
					errorsMu.Unlock()
				} else if !errors.Is(err, context.Canceled) {
					slog.Default().Warn("mass-sa dispatcher connection failed", "error", err)
				}
				return
			}
			defer client.Close()
			attempted, delivered, deferred, blocked, err := d.dispatchGroup(ctx, client, project, group)
			if err != nil {
				wrapped := fmt.Errorf("root %s: %w", group.rootTicketID, err)
				if wait {
					errorsMu.Lock()
					cycleErrors = append(cycleErrors, wrapped)
					errorsMu.Unlock()
				} else if !errors.Is(err, context.Canceled) {
					slog.Default().Warn("mass-sa dispatcher target failed", "error", wrapped)
				}
			}
			if wait {
				errorsMu.Lock()
				cycle.Attempted += attempted
				cycle.Delivered += delivered
				cycle.Deferred += deferred
				cycle.Blocked += blocked
				errorsMu.Unlock()
			}
		}(group, targetKey)
	}
	if wait {
		waitGroup.Wait()
	}
	return cycle, cycleErrors, nil
}

func (d *EventDispatcher) queuedGroups(project ProjectConfig) ([]queuedDeliveryGroup, error) {
	state := d.service.store.Snapshot().Projects[project.ID]
	cutoff := d.now().Add(-time.Duration(project.Inbox.MergeWindowMS) * time.Millisecond)
	groups := map[string]*queuedDeliveryGroup{}
	for _, event := range state.Inbox {
		if event.ProjectID != project.ID || event.State != "queued" || event.AcceptedAt.After(cutoff) {
			continue
		}
		var available bool
		event, available = resolveQueuedInput(state, event)
		if !available {
			continue
		}
		if event.RoutingPath != "router" && event.RoutingPath != "direct" {
			continue
		}
		key := deliveryGroupKey(event)
		group := groups[key]
		if group == nil {
			group = &queuedDeliveryGroup{
				projectID:          event.ProjectID,
				rootTicketID:       event.RootTicketID,
				routingPath:        event.RoutingPath,
				targetAssignmentID: event.TargetAssignmentID,
				targetWorkerName:   event.TargetWorkerName,
				targetSession:      event.TargetSession,
			}
			if group.routingPath == "router" {
				group.targetAssignmentID = ""
			}
			groups[key] = group
		}
		group.events = append(group.events, cloneInboxEvent(event))
	}
	result := make([]queuedDeliveryGroup, 0, len(groups))
	for _, group := range groups {
		sortInboxEvents(group.events)
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.projectID != right.projectID {
			return left.projectID < right.projectID
		}
		if !left.events[0].AcceptedAt.Equal(right.events[0].AcceptedAt) {
			return left.events[0].AcceptedAt.Before(right.events[0].AcceptedAt)
		}
		if left.rootTicketID != right.rootTicketID {
			return left.rootTicketID < right.rootTicketID
		}
		if left.routingPath != right.routingPath {
			return left.routingPath < right.routingPath
		}
		if left.targetAssignmentID != right.targetAssignmentID {
			return left.targetAssignmentID < right.targetAssignmentID
		}
		return left.targetWorkerName < right.targetWorkerName
	})
	return result, nil
}

func (d *EventDispatcher) dispatchGroup(ctx context.Context, client sessionClient, project ProjectConfig, group queuedDeliveryGroup) (attempted, delivered, deferred, blocked int, err error) {
	if group.routingPath == "direct" {
		if err := d.validateDirectGroup(project.ID, group); err != nil {
			for _, event := range group.events {
				if event.TriggerID != "" || event.ChildLifecycle != nil {
					return 0, 0, len(group.events), 0, nil
				}
			}
			blocked = len(group.events)
			if blockErr := d.service.createBlockedBatch(project.ID, group, group.events, operationReason(err)); blockErr != nil {
				return 0, 0, 0, 0, errors.Join(err, blockErr)
			}
			return 0, 0, 0, blocked, nil
		}
	}
	session, err := client.GetSession(ctx, SessionRef{SessionID: group.targetSession.SessionID})
	if err != nil {
		if group.routingPath == "direct" && IsErrorCode(err, "not_found") {
			for _, event := range group.events {
				if event.ChildLifecycle != nil {
					return 0, 0, len(group.events), 0, nil
				}
			}
			blocked = len(group.events)
			blockErr := d.service.createBlockedBatch(project.ID, group, group.events, "worker_session_missing")
			return 0, 0, 0, blocked, blockErr
		}
		return 0, 0, 0, 0, err
	}
	if session.Status.State == "offline" {
		if group.routingPath == "direct" {
			// Worker recovery renews the Assignment and capability through
			// create_worker; dispatch must not race that lifecycle transition.
			return 0, 0, len(group.events), 0, nil
		}
		resumed, resumeErr := client.ResumeSession(ctx, SessionRef{SessionID: group.targetSession.SessionID})
		if resumeErr != nil {
			return 0, 0, len(group.events), 0, nil
		}
		session = resumed
	}
	d.service.recordObservedSessionState(project.ID, group, session.Status.State)
	if session.Status.State != "idle" && (group.routingPath != "direct" || !d.service.resumedInputReady(project.ID, group.targetAssignmentID, session)) {
		return 0, 0, len(group.events), 0, nil
	}

	batches, oversized := planInboxBatches(group.events, project.Inbox.MaxBatchSize, project.Inbox.MaxPromptBytes)
	if len(oversized) != 0 {
		blocked = len(oversized)
		if blockErr := d.service.createBlockedBatch(project.ID, group, oversized, "prompt_budget_exceeded"); blockErr != nil {
			return 0, 0, 0, 0, blockErr
		}
	}
	if len(batches) == 0 {
		return 0, 0, deferred, blocked, nil
	}
	batchEvents := batches[0]
	attempted = len(batchEvents)
	switch group.routingPath {
	case "router":
		if err := d.deliverRouterBatch(ctx, client, group, batchEvents); err != nil {
			return attempted, 0, 0, blocked, err
		}
		return attempted, 1, 0, blocked, nil
	case "direct":
		worker, workerErr := d.service.worker(project.ID, WorkerNamespace(project.ID, group.rootTicketID), group.targetWorkerName)
		if workerErr != nil {
			blocked += len(batchEvents)
			blockErr := d.service.createBlockedBatch(project.ID, group, batchEvents, "worker_not_found")
			return 0, 0, 0, blocked, blockErr
		}
		if _, runErr := d.service.executeWorkerEvents(ctx, client, group.rootTicketID, worker, batchEvents); runErr != nil {
			return attempted, 0, 0, blocked, runErr
		}
		return attempted, 1, 0, blocked, nil
	default:
		return 0, 0, 0, blocked, operationError("failed_precondition", "invalid_routing_path", "event has an unsupported routing path")
	}
}

func (d *EventDispatcher) validateDirectGroup(projectID string, group queuedDeliveryGroup) error {
	project := d.service.store.Snapshot().Projects[projectID]
	worker, workerExists := project.Workers[workerKey(WorkerNamespace(projectID, group.rootTicketID), group.targetWorkerName)]
	assignment, assignmentExists := project.Assignments[group.targetAssignmentID]
	ticket, ticketExists := project.Tickets[assignment.TicketID]
	if !workerExists || !assignmentExists || !ticketExists ||
		worker.AssignmentID != group.targetAssignmentID ||
		worker.Session.SessionID != group.targetSession.SessionID ||
		assignment.Status != "active" ||
		assignment.ExecutionState == "unknown" ||
		ticket.AssignmentID != assignment.ID {
		return operationError("failed_precondition", "assignment_inactive", "accepted direct event target is no longer an active Assignment")
	}
	return nil
}

func (d *EventDispatcher) deliverRouterBatch(ctx context.Context, client sessionClient, group queuedDeliveryGroup, events []InboxEventState) error {
	ref := SessionRef{SessionID: group.targetSession.SessionID}
	subscription, err := client.Subscribe(ctx, ref)
	if err != nil {
		return err
	}
	defer subscription.Close()
	baselineEvents, err := readAllHistory(ctx, client, ref, []string{"user.message", "agent.message"})
	if err != nil {
		return err
	}
	baseline := historyEventIDs(baselineEvents)

	batch, err := d.service.prepareDelivery(group.projectID, group.rootTicketID, group, "", events)
	if err != nil {
		return err
	}
	prompt := workerPromptFromEvents(events)
	if _, err := client.PromptSession(ctx, ref, prompt); err != nil {
		state, reason := "failed", operationReason(err)
		if IsErrorCode(err, "outcome_unknown") {
			state = "unknown"
		}
		settleErr := d.service.settleDelivery(group.projectID, batch.ID, state, reason, time.Time{}, false)
		return errors.Join(err, settleErr)
	}
	acceptedAt := d.service.now()
	if err := d.service.settleDelivery(group.projectID, batch.ID, "delivered", "", acceptedAt, false); err != nil {
		return operationError("outcome_unknown", "delivery_state_unconfirmed", "router prompt was accepted but delivery state was not confirmed")
	}

	finalEvent, _, collectErr := collectSessionFinal(ctx, client, subscription, ref, prompt, baseline, "router")
	if collectErr != nil {
		reason := operationReason(collectErr)
		settleErr := d.service.settleRouterResult(group.projectID, batch.ID, "unknown", "", false, "", reason)
		return errors.Join(collectErr, settleErr)
	}
	reportText, _ := workerReportText(finalEvent)
	_, parseErr := parseStructuredReport(reportText)
	return d.service.settleRouterResult(group.projectID, batch.ID, "confirmed", finalEvent.EventID, parseErr == "", parseErr, "")
}

func (s *WorkerService) settleRouterResult(projectID, batchID, state, finalEventID string, structured bool, parseError, reason string) error {
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		batch, exists := project.Deliveries[batchID]
		if !exists {
			return operationError("failed_precondition", "delivery_missing", "delivery batch no longer exists")
		}
		batch.ResultState = state
		batch.FinalEventID = finalEventID
		batch.Structured = structured
		batch.ParseError = strings.TrimSpace(parseError)
		batch.ResultError = strings.TrimSpace(reason)
		batch.UpdatedAt = s.now()
		project.Deliveries[batchID] = batch
		return nil
	})
}

func (s *WorkerService) prepareDelivery(projectID, rootTicketID string, group queuedDeliveryGroup, executionID string, events []InboxEventState) (DeliveryBatchState, error) {
	batchID, err := s.newID("b")
	if err != nil {
		return DeliveryBatchState{}, err
	}
	now := s.now()
	prompt := workerPromptFromEvents(events)
	batch := DeliveryBatchState{
		ID:                 batchID,
		ProjectID:          projectID,
		RootTicketID:       rootTicketID,
		TargetAssignmentID: group.targetAssignmentID,
		TargetWorkerName:   group.targetWorkerName,
		TargetSession:      group.targetSession,
		RoutingPath:        group.routingPath,
		EventIDs:           inboxEventIDs(events),
		ExecutionID:        executionID,
		PromptBytes:        len(prompt),
		State:              "dispatching",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if batch.RoutingPath == "router" {
		batch.TargetAssignmentID = ""
	}
	err = s.store.updateProject(projectID, func(project *ProjectState) error {
		for _, selected := range events {
			event, exists := project.Inbox[selected.ID]
			if !exists {
				return operationError("failed_precondition", "inbox_event_missing", "inbox event disappeared before dispatch")
			}
			if event.State != "queued" {
				return operationError("failed_precondition", "inbox_state_changed", "inbox event is no longer queued")
			}
			if event.TriggerID != "" || event.ChildLifecycle != nil {
				resolved, available := resolveQueuedInput(*project, event)
				if !available {
					return operationError("failed_precondition", "inbox_target_changed", "Ticket has no eligible current Assignment")
				}
				event = resolved
			}
			if event.RootTicketID != rootTicketID ||
				event.RoutingPath != group.routingPath ||
				event.TargetAssignmentID != selected.TargetAssignmentID ||
				event.TargetWorkerName != selected.TargetWorkerName ||
				event.TargetSession.SessionID != group.targetSession.SessionID {
				return operationError("failed_precondition", "inbox_target_changed", "inbox event target changed after batching")
			}
			event.State = "dispatching"
			event.ExecutionID = executionID
			event.ErrorReason = ""
			event.UpdatedAt = now
			project.Inbox[event.ID] = event
		}
		project.Deliveries[batch.ID] = batch
		return nil
	})
	if err != nil {
		return DeliveryBatchState{}, err
	}
	return batch, nil
}

func (s *WorkerService) settleDelivery(projectID, batchID, state, reason string, acceptedAt time.Time, updateAssignment bool) error {
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		batch, exists := project.Deliveries[batchID]
		if !exists {
			return operationError("failed_precondition", "delivery_missing", "delivery batch no longer exists")
		}
		if batch.State != "dispatching" && batch.State != state {
			return operationError("failed_precondition", "delivery_state_changed", "delivery batch state changed before confirmation")
		}
		now := s.now()
		if acceptedAt.IsZero() {
			acceptedAt = now
		}
		batch.State = state
		batch.ErrorReason = strings.TrimSpace(reason)
		batch.UpdatedAt = now
		if state == "delivered" {
			batch.DeliveredAt = acceptedAt
		}
		project.Deliveries[batch.ID] = batch
		for _, eventID := range batch.EventIDs {
			event, exists := project.Inbox[eventID]
			if !exists {
				return operationError("failed_precondition", "inbox_event_missing", "inbox event no longer exists")
			}
			if event.State != "dispatching" && event.State != state {
				return operationError("failed_precondition", "inbox_state_changed", "inbox event state changed before delivery confirmation")
			}
			event.State = state
			event.ErrorReason = strings.TrimSpace(reason)
			event.UpdatedAt = now
			if state == "delivered" {
				event.DeliveredAt = acceptedAt
			}
			project.Inbox[eventID] = event
		}
		if updateAssignment && batch.TargetAssignmentID != "" {
			assignment, exists := project.Assignments[batch.TargetAssignmentID]
			if !exists {
				return operationError("failed_precondition", "assignment_missing", "delivery Assignment no longer exists")
			}
			switch state {
			case "delivered":
				assignment.ExecutionState = "prompted"
				assignment.PromptAcceptedAt = acceptedAt
			case "unknown":
				assignment.ExecutionState = "unknown"
			case "failed":
				assignment.ExecutionState = "failed"
			}
			assignment.UpdatedAt = now
			project.Assignments[assignment.ID] = assignment
		}
		return nil
	})
}

func (s *WorkerService) createBlockedBatch(projectID string, group queuedDeliveryGroup, events []InboxEventState, reason string) error {
	if len(events) == 0 {
		return nil
	}
	batchID, err := s.newID("b")
	if err != nil {
		return err
	}
	now := s.now()
	eventIDs := inboxEventIDs(events)
	batch := DeliveryBatchState{
		ID:                 batchID,
		ProjectID:          projectID,
		RootTicketID:       group.rootTicketID,
		TargetAssignmentID: group.targetAssignmentID,
		TargetWorkerName:   group.targetWorkerName,
		TargetSession:      group.targetSession,
		RoutingPath:        group.routingPath,
		EventIDs:           eventIDs,
		PromptBytes:        len(workerPromptFromEvents(events)),
		State:              "blocked",
		ErrorReason:        reason,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if batch.RoutingPath == "router" {
		batch.TargetAssignmentID = ""
	}
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		for _, eventID := range eventIDs {
			event, exists := project.Inbox[eventID]
			if !exists || event.State != "queued" {
				continue
			}
			event.State = "blocked"
			event.ErrorReason = reason
			event.UpdatedAt = now
			project.Inbox[eventID] = event
		}
		project.Deliveries[batch.ID] = batch
		return nil
	})
}

func (s *WorkerService) recordObservedSessionState(projectID string, group queuedDeliveryGroup, state string) {
	_ = s.store.updateProject(projectID, func(project *ProjectState) error {
		if group.routingPath == "router" {
			if project.Router != nil && project.Router.SessionID == group.targetSession.SessionID {
				project.Router.ObservedState = state
				project.Router.UpdatedAt = s.now()
			}
			return nil
		}
		key := workerKey(WorkerNamespace(projectID, group.rootTicketID), group.targetWorkerName)
		worker, exists := project.Workers[key]
		if exists && worker.AssignmentID == group.targetAssignmentID {
			worker.State = state
			worker.UpdatedAt = s.now()
			project.Workers[key] = worker
		}
		return nil
	})
}

func deliveryGroupKey(event InboxEventState) string {
	if event.RoutingPath == "router" {
		return strings.Join([]string{
			event.ProjectID,
			event.RootTicketID,
			event.RoutingPath,
			event.TargetSession.SessionID,
		}, "\x00")
	}
	return strings.Join([]string{
		event.ProjectID,
		event.RootTicketID,
		event.RoutingPath,
		event.TargetAssignmentID,
		event.TargetWorkerName,
		event.TargetSession.SessionID,
	}, "\x00")
}

func deliveryGroupForEvents(projectID, rootTicketID string, events []InboxEventState) queuedDeliveryGroup {
	first := events[0]
	group := queuedDeliveryGroup{
		projectID:          projectID,
		rootTicketID:       rootTicketID,
		routingPath:        first.RoutingPath,
		targetAssignmentID: first.TargetAssignmentID,
		targetWorkerName:   first.TargetWorkerName,
		targetSession:      first.TargetSession,
		events:             append([]InboxEventState(nil), events...),
	}
	if group.routingPath == "router" {
		group.targetAssignmentID = ""
	}
	return group
}
