package sa

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxInboxPayloadBytes = 64 << 10
	maxInboxFieldBytes   = 4 << 10
	maxRouteKeyBytes     = 1 << 10
)

// AcceptEventRequest is one durably accepted external event.
type AcceptEventRequest struct {
	ProjectID    string          `json:"projectId"`
	RootTicketID string          `json:"rootTicketId"`
	RouteKey     string          `json:"routeKey,omitempty"`
	Source       string          `json:"source"`
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	Payload      json.RawMessage `json:"payload"`
}

// AcceptedEvent is the route fixed when an event is accepted.
type AcceptedEvent struct {
	EventID            string     `json:"eventId"`
	Sequence           uint64     `json:"sequence"`
	ProjectID          string     `json:"projectId"`
	RootTicketID       string     `json:"rootTicketId"`
	RouteKey           string     `json:"routeKey,omitempty"`
	RoutingPath        string     `json:"routingPath"`
	State              string     `json:"state"`
	TargetAssignmentID string     `json:"targetAssignmentId,omitempty"`
	TargetWorkerName   string     `json:"targetWorkerName"`
	TargetSession      SessionRef `json:"targetSession"`
	AcceptedAt         time.Time  `json:"acceptedAt"`
}

// RouteBindingView is one readable routeKey binding.
type RouteBindingView struct {
	RootTicketID string    `json:"rootTicketId"`
	RouteKey     string    `json:"routeKey"`
	WorkerName   string    `json:"workerName"`
	AssignmentID string    `json:"assignmentId"`
	CreatedAt    time.Time `json:"createdAt"`
	Revoked      bool      `json:"revoked,omitempty"`
}

// BindEventRouteRequest creates or replaces one root Ticket routeKey binding.
type BindEventRouteRequest struct {
	ProjectID    string `json:"projectId"`
	RootTicketID string `json:"rootTicketId"`
	RouteKey     string `json:"routeKey"`
	WorkerName   string `json:"workerName"`
	AssignmentID string `json:"assignmentId"`
}

// InboxEventView exposes one event's durable delivery state without duplicating
// its payload in the management read model.
type InboxEventView struct {
	ID                 string     `json:"id"`
	Sequence           uint64     `json:"sequence"`
	RouteKey           string     `json:"routeKey,omitempty"`
	Source             string     `json:"source"`
	Kind               string     `json:"kind"`
	Summary            string     `json:"summary"`
	RoutingPath        string     `json:"routingPath"`
	State              string     `json:"state"`
	TargetAssignmentID string     `json:"targetAssignmentId,omitempty"`
	TargetWorkerName   string     `json:"targetWorkerName"`
	TargetSession      SessionRef `json:"targetSession"`
	ExecutionID        string     `json:"executionId,omitempty"`
	ErrorReason        string     `json:"errorReason,omitempty"`
	AcceptedAt         time.Time  `json:"acceptedAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	DeliveredAt        time.Time  `json:"deliveredAt,omitempty"`
}

// DeliveryBatchView exposes one prompt batch and its event order.
type DeliveryBatchView struct {
	ID                 string     `json:"id"`
	RoutingPath        string     `json:"routingPath"`
	State              string     `json:"state"`
	EventIDs           []string   `json:"eventIds"`
	TargetAssignmentID string     `json:"targetAssignmentId,omitempty"`
	TargetWorkerName   string     `json:"targetWorkerName"`
	TargetSession      SessionRef `json:"targetSession"`
	ExecutionID        string     `json:"executionId,omitempty"`
	PromptBytes        int        `json:"promptBytes"`
	ErrorReason        string     `json:"errorReason,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	DeliveredAt        time.Time  `json:"deliveredAt,omitempty"`
	ResultState        string     `json:"resultState,omitempty"`
	FinalEventID       string     `json:"finalEventId,omitempty"`
	FinalCommentID     string     `json:"finalCommentId,omitempty"`
	Structured         bool       `json:"structured,omitempty"`
	ParseError         string     `json:"parseError,omitempty"`
	ResultError        string     `json:"resultError,omitempty"`
}

// DeliveryCounts summarizes root events by route and delivery state.
type DeliveryCounts struct {
	Router         int `json:"router"`
	Direct         int `json:"direct"`
	Queued         int `json:"queued"`
	Dispatching    int `json:"dispatching"`
	Delivered      int `json:"delivered"`
	Unknown        int `json:"unknown"`
	Failed         int `json:"failed"`
	Blocked        int `json:"blocked"`
	Reconciliation int `json:"reconciliation"`
}

// ReconciliationView is one prompt whose side effect was accepted before a
// process restart but whose final business result was not observed. It is a
// read-only operator signal; mass-sa never resends the prompt automatically.
type ReconciliationView struct {
	DeliveryID       string     `json:"deliveryId,omitempty"`
	AssignmentID     string     `json:"assignmentId"`
	TicketID         string     `json:"ticketId"`
	WorkerName       string     `json:"workerName"`
	Session          SessionRef `json:"session"`
	ExecutionState   string     `json:"executionState"`
	Reason           string     `json:"reason"`
	PromptAcceptedAt time.Time  `json:"promptAcceptedAt,omitempty"`
}

// TicketInspection is the management read model for one root Ticket.
type TicketInspection struct {
	Root           TicketState          `json:"root"`
	Tickets        []TicketState        `json:"tickets"`
	Workers        []WorkerView         `json:"workers"`
	Assignments    []AssignmentState    `json:"assignments"`
	RouteBindings  []RouteBindingView   `json:"routeBindings"`
	Batches        []DeliveryBatchView  `json:"batches"`
	Events         []InboxEventView     `json:"events"`
	Counts         DeliveryCounts       `json:"counts"`
	Reconciliation []ReconciliationView `json:"reconciliation"`
}

type fixedEventTarget struct {
	routingPath        string
	targetAssignmentID string
	targetWorkerName   string
	targetSession      SessionRef
}

// AcceptEvent first fixes a route and then commits the durable inbox event.
// A stale binding is removed in the same state replacement and the event falls
// back to the project router. Later binding changes cannot alter this event.
func (s *WorkerService) AcceptEvent(req AcceptEventRequest) (AcceptedEvent, error) {
	if _, err := s.project(req.ProjectID); err != nil {
		return AcceptedEvent{}, err
	}
	if _, err := s.rootTicket(req.ProjectID, req.RootTicketID); err != nil {
		return AcceptedEvent{}, err
	}
	if err := validateInboxField("source", req.Source, true); err != nil {
		return AcceptedEvent{}, err
	}
	if err := validateInboxField("kind", req.Kind, true); err != nil {
		return AcceptedEvent{}, err
	}
	if err := validateInboxField("summary", req.Summary, true); err != nil {
		return AcceptedEvent{}, err
	}
	if err := validateRouteKey(req.RouteKey, false); err != nil {
		return AcceptedEvent{}, err
	}
	payload := append(json.RawMessage(nil), req.Payload...)
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if len(payload) > maxInboxPayloadBytes {
		return AcceptedEvent{}, operationError("invalid_argument", "event_payload_too_large", "event payload exceeds the size limit")
	}
	if !json.Valid(payload) {
		return AcceptedEvent{}, operationError("invalid_argument", "invalid_event_payload", "event payload must be valid JSON")
	}
	eventID, err := s.newID("e")
	if err != nil {
		return AcceptedEvent{}, err
	}
	acceptedAt := s.now()
	var accepted AcceptedEvent
	err = s.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		root, rootExists := project.Tickets[req.RootTicketID]
		if !rootExists || root.ProjectID != req.ProjectID || root.RootTicketID != root.ID {
			return workerNotFound()
		}
		target, err := s.fixedEventTarget(project, req.ProjectID, req.RootTicketID, req.RouteKey)
		if err != nil {
			return err
		}
		event := InboxEventState{
			ID:                 eventID,
			ProjectID:          req.ProjectID,
			RootTicketID:       req.RootTicketID,
			RouteKey:           req.RouteKey,
			Source:             strings.TrimSpace(req.Source),
			Kind:               strings.TrimSpace(req.Kind),
			Summary:            strings.TrimSpace(req.Summary),
			Payload:            append(json.RawMessage(nil), payload...),
			TargetAssignmentID: target.targetAssignmentID,
			TargetWorkerName:   target.targetWorkerName,
			TargetSession:      target.targetSession,
			RoutingPath:        target.routingPath,
			State:              "queued",
			AcceptedAt:         acceptedAt,
			UpdatedAt:          acceptedAt,
		}
		project.NextInboxSeq++
		event.Sequence = project.NextInboxSeq
		projectConfig, _ := projectConfigByID(s.config, req.ProjectID)
		promptBytes := len(workerPromptFromEvents([]InboxEventState{event}))
		if promptBytes > projectConfig.Inbox.MaxPromptBytes {
			return operationError("invalid_argument", "event_prompt_too_large", "one event exceeds the configured prompt budget")
		}
		if _, exists := project.Inbox[eventID]; exists {
			return operationError("already_exists", "event_exists", "inbox event already exists")
		}
		project.Inbox[eventID] = event
		accepted = AcceptedEvent{
			EventID:            event.ID,
			Sequence:           event.Sequence,
			ProjectID:          event.ProjectID,
			RootTicketID:       event.RootTicketID,
			RouteKey:           event.RouteKey,
			RoutingPath:        event.RoutingPath,
			State:              event.State,
			TargetAssignmentID: event.TargetAssignmentID,
			TargetWorkerName:   event.TargetWorkerName,
			TargetSession:      event.TargetSession,
			AcceptedAt:         event.AcceptedAt,
		}
		return nil
	})
	if err != nil {
		return AcceptedEvent{}, err
	}
	return accepted, nil
}

// BindEventRoute creates or replaces one root Ticket routeKey binding. The
// Assignment is explicit and must still be the worker's current Assignment.
func (s *WorkerService) BindEventRoute(req BindEventRouteRequest) (RouteBindingView, error) {
	result, err := s.bindRoute(req.ProjectID, bindRouteArgs{
		RootTicketID: req.RootTicketID,
		RouteKey:     req.RouteKey,
		WorkerName:   req.WorkerName,
		AssignmentID: req.AssignmentID,
	})
	if err != nil {
		return RouteBindingView{}, err
	}
	return RouteBindingView{
		RootTicketID: result.RootTicketID,
		RouteKey:     result.RouteKey,
		WorkerName:   result.WorkerName,
		AssignmentID: result.AssignmentID,
		CreatedAt:    result.CreatedAt,
	}, nil
}

// RevokeEventRoute removes a root routeKey binding and returns the removed
// identity for readback.
func (s *WorkerService) RevokeEventRoute(projectID, rootTicketID, routeKey string) (RouteBindingView, error) {
	if _, err := s.project(projectID); err != nil {
		return RouteBindingView{}, err
	}
	if _, err := s.rootTicket(projectID, rootTicketID); err != nil {
		return RouteBindingView{}, err
	}
	if err := validateRouteKey(routeKey, true); err != nil {
		return RouteBindingView{}, err
	}
	var revoked RouteBindingView
	err := s.store.updateProject(projectID, func(project *ProjectState) error {
		key := routeBindingKey(rootTicketID, routeKey)
		binding, exists := project.RouteBindings[key]
		if !exists {
			return operationError("not_found", "route_binding_not_found", "route binding was not found")
		}
		delete(project.RouteBindings, key)
		revoked = RouteBindingView{
			RootTicketID: binding.RootTicketID,
			RouteKey:     binding.RouteKey,
			WorkerName:   binding.WorkerName,
			AssignmentID: binding.AssignmentID,
			CreatedAt:    binding.CreatedAt,
			Revoked:      true,
		}
		return nil
	})
	return revoked, err
}

// ListEventRoutes reads every persisted routeKey binding for a root.
func (s *WorkerService) ListEventRoutes(projectID, rootTicketID string) ([]RouteBindingView, error) {
	if _, err := s.project(projectID); err != nil {
		return nil, err
	}
	if _, err := s.rootTicket(projectID, rootTicketID); err != nil {
		return nil, err
	}
	project := s.store.Snapshot().Projects[projectID]
	var bindings []RouteBindingView
	for _, binding := range project.RouteBindings {
		if binding.ProjectID == projectID && binding.RootTicketID == rootTicketID {
			bindings = append(bindings, RouteBindingView{
				RootTicketID: binding.RootTicketID,
				RouteKey:     binding.RouteKey,
				WorkerName:   binding.WorkerName,
				AssignmentID: binding.AssignmentID,
				CreatedAt:    binding.CreatedAt,
			})
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].RouteKey < bindings[j].RouteKey })
	return bindings, nil
}

// InspectTicket reads route counts, batches, events, and worker identities for
// one root Ticket without querying MASS history.
func (s *WorkerService) InspectTicket(projectID, rootTicketID string) (TicketInspection, error) {
	root, err := s.rootTicket(projectID, rootTicketID)
	if err != nil {
		return TicketInspection{}, err
	}
	project := s.store.Snapshot().Projects[projectID]
	result := TicketInspection{
		Root:          root,
		Tickets:       []TicketState{},
		Workers:       []WorkerView{},
		Assignments:   []AssignmentState{},
		RouteBindings: []RouteBindingView{},
		Batches:       []DeliveryBatchView{},
		Events:        []InboxEventView{},
	}
	for _, ticket := range project.Tickets {
		if ticket.ProjectID == projectID && ticket.RootTicketID == rootTicketID {
			result.Tickets = append(result.Tickets, ticket)
		}
	}
	sort.Slice(result.Tickets, func(i, j int) bool {
		if result.Tickets[i].CreatedAt.Equal(result.Tickets[j].CreatedAt) {
			return result.Tickets[i].ID < result.Tickets[j].ID
		}
		return result.Tickets[i].CreatedAt.Before(result.Tickets[j].CreatedAt)
	})
	for _, worker := range project.Workers {
		if worker.ProjectID == projectID && worker.RootTicketID == rootTicketID {
			result.Workers = append(result.Workers, viewWorker(worker))
		}
	}
	sort.Slice(result.Workers, func(i, j int) bool { return result.Workers[i].Name < result.Workers[j].Name })
	for _, assignment := range project.Assignments {
		if assignment.ProjectID == projectID && assignment.RootTicketID == rootTicketID {
			result.Assignments = append(result.Assignments, assignment)
			if assignment.ReconciliationRequired {
				result.Reconciliation = append(result.Reconciliation, ReconciliationView{
					AssignmentID:     assignment.ID,
					TicketID:         assignment.TicketID,
					WorkerName:       assignment.WorkerName,
					Session:          assignment.Session,
					ExecutionState:   assignment.ExecutionState,
					Reason:           assignment.ReconciliationReason,
					PromptAcceptedAt: assignment.PromptAcceptedAt,
				})
			}
		}
	}
	sort.Slice(result.Assignments, func(i, j int) bool {
		if result.Assignments[i].CreatedAt.Equal(result.Assignments[j].CreatedAt) {
			return result.Assignments[i].ID < result.Assignments[j].ID
		}
		return result.Assignments[i].CreatedAt.Before(result.Assignments[j].CreatedAt)
	})
	sort.Slice(result.Reconciliation, func(i, j int) bool {
		left := result.Reconciliation[i].AssignmentID + "\x00" + result.Reconciliation[i].DeliveryID
		right := result.Reconciliation[j].AssignmentID + "\x00" + result.Reconciliation[j].DeliveryID
		return left < right
	})
	routes, err := s.ListEventRoutes(projectID, rootTicketID)
	if err != nil {
		return TicketInspection{}, err
	}
	result.RouteBindings = routes
	if result.Reconciliation == nil {
		result.Reconciliation = []ReconciliationView{}
	}
	for _, event := range project.Inbox {
		if event.ProjectID != projectID || event.RootTicketID != rootTicketID {
			continue
		}
		result.Events = append(result.Events, viewInboxEvent(event))
		switch event.RoutingPath {
		case "router":
			result.Counts.Router++
		case "direct":
			result.Counts.Direct++
		}
		switch event.State {
		case "queued":
			result.Counts.Queued++
		case "dispatching":
			result.Counts.Dispatching++
		case "delivered":
			result.Counts.Delivered++
		case "unknown":
			result.Counts.Unknown++
		case "failed":
			result.Counts.Failed++
		case "blocked":
			result.Counts.Blocked++
		}
	}
	sort.Slice(result.Events, func(i, j int) bool {
		if result.Events[i].AcceptedAt.Equal(result.Events[j].AcceptedAt) {
			return result.Events[i].ID < result.Events[j].ID
		}
		return result.Events[i].AcceptedAt.Before(result.Events[j].AcceptedAt)
	})
	for _, batch := range project.Deliveries {
		if batch.ProjectID != projectID || batch.RootTicketID != rootTicketID {
			continue
		}
		result.Batches = append(result.Batches, viewDeliveryBatch(batch))
		if batch.ResultState == "reconcile_required" {
			result.Reconciliation = append(result.Reconciliation, ReconciliationView{
				DeliveryID:       batch.ID,
				WorkerName:       batch.TargetWorkerName,
				Session:          batch.TargetSession,
				ExecutionState:   "delivered",
				Reason:           batch.ResultError,
				PromptAcceptedAt: batch.DeliveredAt,
			})
		}
	}
	sort.Slice(result.Batches, func(i, j int) bool {
		if result.Batches[i].CreatedAt.Equal(result.Batches[j].CreatedAt) {
			return result.Batches[i].ID < result.Batches[j].ID
		}
		return result.Batches[i].CreatedAt.Before(result.Batches[j].CreatedAt)
	})
	sort.Slice(result.Reconciliation, func(i, j int) bool {
		left := result.Reconciliation[i].AssignmentID + "\x00" + result.Reconciliation[i].DeliveryID
		right := result.Reconciliation[j].AssignmentID + "\x00" + result.Reconciliation[j].DeliveryID
		return left < right
	})
	result.Counts.Reconciliation = len(result.Reconciliation)
	return result, nil
}

// RecoverDispatches resolves write-ahead batches after a process restart. A
// batch already marked dispatching may have reached MASS, so its events become
// unknown and are never sent automatically.
func (s *WorkerService) RecoverDispatches() error {
	var recoveryErrors []error
	for _, project := range s.config.Projects {
		if err := s.recoverProjectDispatches(project.ID); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("project %s: %w", project.ID, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

func (s *WorkerService) recoverProjectDispatches(projectID string) error {
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		now := s.now()
		for id, trigger := range project.Triggers {
			if trigger.Decision == "creating" {
				trigger.Decision = "unknown"
				trigger.ErrorReason = "trigger_creation_interrupted"
				project.Triggers[id] = trigger
			}
		}
		assignmentsWithUnknownPrompt := map[string]struct{}{}
		for batchID, batch := range project.Deliveries {
			if batch.State == "delivered" && batch.RoutingPath == "router" && batch.ResultState == "" {
				batch.ResultState = "reconcile_required"
				batch.ResultError = "router_final_output_unobserved"
				batch.UpdatedAt = now
				project.Deliveries[batchID] = batch
				continue
			}
			if batch.State != "dispatching" {
				continue
			}
			batch.State = "unknown"
			batch.ErrorReason = "dispatcher_interrupted"
			batch.UpdatedAt = now
			project.Deliveries[batchID] = batch
			if batch.TargetAssignmentID != "" {
				assignmentsWithUnknownPrompt[batch.TargetAssignmentID] = struct{}{}
			}
			for _, eventID := range batch.EventIDs {
				event, exists := project.Inbox[eventID]
				if !exists || (event.State != "queued" && event.State != "dispatching") {
					continue
				}
				event.State = "unknown"
				event.ErrorReason = "dispatcher_interrupted"
				event.UpdatedAt = now
				project.Inbox[eventID] = event
			}
		}
		for assignmentID, assignment := range project.Assignments {
			switch assignment.ExecutionState {
			case "running":
				if _, ambiguous := assignmentsWithUnknownPrompt[assignmentID]; ambiguous {
					assignment.ExecutionState = "unknown"
					assignment.ReconciliationRequired = true
					assignment.ReconciliationReason = "dispatcher_interrupted"
				} else {
					// Preparation is committed before session.prompt. Running without
					// a dispatching batch therefore proves that no prompt was sent.
					assignment.ExecutionState = ""
				}
			case "prompted":
				// The prompt was accepted before the previous process stopped. The
				// model may have acted, so the turn is never resent automatically.
				assignment.ReconciliationRequired = true
				assignment.ReconciliationReason = "prompt_accepted_without_final"
			case "unknown":
				assignment.ReconciliationRequired = true
				if assignment.ReconciliationReason == "" {
					assignment.ReconciliationReason = "prompt_outcome_unknown"
				}
			}
			assignment.UpdatedAt = now
			project.Assignments[assignmentID] = assignment
		}
		return nil
	})
}

func (s *WorkerService) fixedEventTarget(project *ProjectState, projectID, rootTicketID, routeKey string) (fixedEventTarget, error) {
	router := project.Router
	if router == nil || router.SessionID == "" {
		return fixedEventTarget{}, operationError("failed_precondition", "router_session_missing", "project router Session is not available")
	}
	routerTarget := fixedEventTarget{
		routingPath:      "router",
		targetWorkerName: router.Name,
		targetSession:    router.SessionRef,
	}
	if routeKey == "" {
		return routerTarget, nil
	}
	key := routeBindingKey(rootTicketID, routeKey)
	binding, exists := project.RouteBindings[key]
	if !exists {
		return routerTarget, nil
	}
	invalid := binding.ProjectID != projectID || binding.RootTicketID != rootTicketID ||
		binding.RouteKey != routeKey || binding.AssignmentID == "" || binding.WorkerName == ""
	if !invalid {
		namespace := WorkerNamespace(projectID, rootTicketID)
		worker, workerExists := project.Workers[workerKey(namespace, binding.WorkerName)]
		assignment, assignmentExists := project.Assignments[binding.AssignmentID]
		ticket, ticketExists := project.Tickets[assignment.TicketID]
		invalid = !workerExists || !assignmentExists || !ticketExists ||
			worker.ProjectID != projectID || worker.RootTicketID != rootTicketID ||
			worker.AssignmentID != binding.AssignmentID ||
			worker.Session.SessionID == "" ||
			assignment.ProjectID != projectID || assignment.RootTicketID != rootTicketID ||
			assignment.WorkerName != worker.Name || assignment.Status != "active" ||
			assignment.ExecutionState == "unknown" ||
			ticket.ProjectID != projectID || ticket.RootTicketID != rootTicketID ||
			ticket.AssignmentID != assignment.ID
	}
	if invalid {
		delete(project.RouteBindings, key)
		return routerTarget, nil
	}
	worker := project.Workers[workerKey(WorkerNamespace(projectID, rootTicketID), binding.WorkerName)]
	return fixedEventTarget{
		routingPath:        "direct",
		targetAssignmentID: binding.AssignmentID,
		targetWorkerName:   binding.WorkerName,
		targetSession:      worker.Session,
	}, nil
}

func validateInboxField(name, value string, required bool) error {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return operationError("invalid_argument", "invalid_event", name+" is required")
	}
	if len(value) > maxInboxFieldBytes {
		return operationError("invalid_argument", "invalid_event", name+" exceeds the size limit")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return operationError("invalid_argument", "invalid_event", name+" must be valid UTF-8 without NUL")
	}
	return nil
}

func validateRouteKey(routeKey string, required bool) error {
	if required && strings.TrimSpace(routeKey) == "" {
		return operationError("invalid_argument", "invalid_route_key", "route key is required")
	}
	if len(routeKey) > maxRouteKeyBytes {
		return operationError("invalid_argument", "invalid_route_key", "route key exceeds the size limit")
	}
	if !utf8.ValidString(routeKey) || strings.ContainsRune(routeKey, '\x00') {
		return operationError("invalid_argument", "invalid_route_key", "route key must be valid UTF-8 without NUL")
	}
	return nil
}

func planInboxBatches(events []InboxEventState, maxEvents, maxPromptBytes int) (batches [][]InboxEventState, oversized []InboxEventState) {
	sortInboxEvents(events)
	var current []InboxEventState
	flush := func() {
		if len(current) != 0 {
			batches = append(batches, current)
			current = nil
		}
	}
	for _, event := range events {
		if len(workerPromptFromEvents([]InboxEventState{event})) > maxPromptBytes {
			oversized = append(oversized, event)
			continue
		}
		if len(current) == 0 {
			current = []InboxEventState{event}
			continue
		}
		candidate := append(append([]InboxEventState(nil), current...), event)
		if len(candidate) > maxEvents || len(workerPromptFromEvents(candidate)) > maxPromptBytes {
			flush()
			current = []InboxEventState{event}
			continue
		}
		current = candidate
	}
	flush()
	return batches, oversized
}

func sortInboxEvents(events []InboxEventState) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Sequence != 0 && events[j].Sequence != 0 && events[i].Sequence != events[j].Sequence {
			return events[i].Sequence < events[j].Sequence
		}
		if events[i].AcceptedAt.Equal(events[j].AcceptedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].AcceptedAt.Before(events[j].AcceptedAt)
	})
}

func viewInboxEvent(event InboxEventState) InboxEventView {
	return InboxEventView{
		ID:                 event.ID,
		Sequence:           event.Sequence,
		RouteKey:           event.RouteKey,
		Source:             event.Source,
		Kind:               event.Kind,
		Summary:            event.Summary,
		RoutingPath:        event.RoutingPath,
		State:              event.State,
		TargetAssignmentID: event.TargetAssignmentID,
		TargetWorkerName:   event.TargetWorkerName,
		TargetSession:      event.TargetSession,
		ExecutionID:        event.ExecutionID,
		ErrorReason:        event.ErrorReason,
		AcceptedAt:         event.AcceptedAt,
		UpdatedAt:          event.UpdatedAt,
		DeliveredAt:        event.DeliveredAt,
	}
}

func viewDeliveryBatch(batch DeliveryBatchState) DeliveryBatchView {
	return DeliveryBatchView{
		ID:                 batch.ID,
		RoutingPath:        batch.RoutingPath,
		State:              batch.State,
		EventIDs:           append([]string(nil), batch.EventIDs...),
		TargetAssignmentID: batch.TargetAssignmentID,
		TargetWorkerName:   batch.TargetWorkerName,
		TargetSession:      batch.TargetSession,
		ExecutionID:        batch.ExecutionID,
		PromptBytes:        batch.PromptBytes,
		ErrorReason:        batch.ErrorReason,
		CreatedAt:          batch.CreatedAt,
		UpdatedAt:          batch.UpdatedAt,
		DeliveredAt:        batch.DeliveredAt,
		ResultState:        batch.ResultState,
		FinalEventID:       batch.FinalEventID,
		FinalCommentID:     batch.FinalCommentID,
		Structured:         batch.Structured,
		ParseError:         batch.ParseError,
		ResultError:        batch.ResultError,
	}
}
