package sa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TicketResolution is the business conclusion of one ticket attempt.
type TicketResolution string

const (
	ResolutionCompleted   TicketResolution = "completed"
	ResolutionFailed      TicketResolution = "failed"
	ResolutionCancelled   TicketResolution = "cancelled"
	ResolutionBlocked     TicketResolution = "blocked"
	ResolutionWaitingUser TicketResolution = "waiting_user"
	ResolutionReopened    TicketResolution = "reopened"
)

func (r TicketResolution) validate() error {
	switch r {
	case ResolutionCompleted, ResolutionFailed, ResolutionCancelled, ResolutionBlocked, ResolutionWaitingUser, ResolutionReopened:
		return nil
	default:
		return operationError("invalid_argument", "invalid_resolution", "resolution must be completed, failed, cancelled, blocked, waiting_user, or reopened")
	}
}

type CreateTicketRequest struct {
	Authorization  *WorkAuthorization
	ProjectID      string
	ParentTicketID string
	Title          string
	Objective      string
	Acceptance     string
}

type RecordAssignmentRequest struct {
	ProjectID    string
	TicketID     string
	AssignmentID string
}

type AddReportRequest struct {
	ProjectID     string
	TicketID      string
	AssignmentID  string
	Session       SessionRef
	SourceEventID string
	SourceItemID  string
	SourceTurnID  string
	Structured    bool
	ParseError    string
	Body          string
}

type FinishTicketRequest struct {
	ProjectID  string
	TicketID   string
	Resolution TicketResolution
	Summary    string
}

type TicketBridge struct {
	store    *StateStore
	projects map[string]bool
	now      func() time.Time
	newID    func(prefix string) (string, error)
}

func NewTicketBridge(config Config, store *StateStore) (*TicketBridge, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, operationError("invalid_argument", "invalid_state_store", "state store is required")
	}
	projects := make(map[string]bool, len(config.Projects))
	for _, project := range config.Projects {
		projects[project.ID] = true
	}
	return &TicketBridge{store: store, projects: projects, now: func() time.Time { return time.Now().UTC() }, newID: newRecordID}, nil
}

func (b *TicketBridge) CreateTicket(ctx context.Context, req CreateTicketRequest) (TicketState, error) {
	if err := ctx.Err(); err != nil {
		return TicketState{}, err
	}
	if !b.projects[req.ProjectID] {
		return TicketState{}, operationError("not_found", "unknown_project", "project is not configured")
	}
	for label, value := range map[string]string{"ticket title": req.Title, "ticket objective": req.Objective, "ticket acceptance": req.Acceptance} {
		if label == "ticket title" {
			if err := validateSingleLine(label, value, true); err != nil {
				return TicketState{}, err
			}
		} else if err := validateBlockText(label, value, true); err != nil {
			return TicketState{}, err
		}
	}
	ticketID, err := b.allocateTicketID(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	now := b.now()
	ticket := TicketState{
		Authorization: req.Authorization, ID: ticketID, ProjectID: req.ProjectID,
		RootTicketID: ticketID, Namespace: WorkerNamespace(req.ProjectID, ticketID),
		Title: strings.TrimSpace(req.Title), Objective: strings.TrimSpace(req.Objective),
		Acceptance: strings.TrimSpace(req.Acceptance), CreatedAt: now, UpdatedAt: now,
	}
	err = b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		if req.ParentTicketID != "" {
			parent, exists := project.Tickets[req.ParentTicketID]
			if !exists {
				return operationError("not_found", "ticket_not_found", "parent ticket was not found in this project")
			}
			if terminalBusinessState(parent.BusinessState) {
				return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive new child work")
			}
			ticket.ParentTicketID, ticket.RootTicketID, ticket.Namespace, ticket.Depth = parent.ID, parent.RootTicketID, parent.Namespace, parent.Depth+1
		}
		if _, exists := project.Tickets[ticketID]; exists {
			return operationError("failed_precondition", "duplicate_ticket", "ticket identifier already exists")
		}
		project.Tickets[ticketID] = ticket
		return nil
	})
	if err != nil {
		return TicketState{}, err
	}
	return ticket, nil
}

func (b *TicketBridge) RecordAssignment(_ context.Context, req RecordAssignmentRequest) (TicketState, error) {
	if err := validateSingleLine("assignment ID", req.AssignmentID, true); err != nil {
		return TicketState{}, err
	}
	ticket, err := b.ticket(req.ProjectID, req.TicketID)
	if err != nil {
		return TicketState{}, err
	}
	var updated TicketState
	err = b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		stored := project.Tickets[req.TicketID]
		if terminalBusinessState(stored.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive a new assignment")
		}
		if stored.AssignmentID != ticket.AssignmentID {
			return operationError("failed_precondition", "assignment_changed", "ticket assignment changed before the new assignment was recorded")
		}
		if stored.AssignmentID == req.AssignmentID {
			updated = stored
			return nil
		}
		if previous, exists := project.Assignments[stored.AssignmentID]; exists {
			previous.Status, previous.UpdatedAt = "superseded", b.now()
			project.Assignments[previous.ID] = previous
		}
		for key, binding := range project.RouteBindings {
			if binding.AssignmentID == stored.AssignmentID {
				delete(project.RouteBindings, key)
			}
		}
		stored.AssignmentID, stored.UpdatedAt = req.AssignmentID, b.now()
		project.Tickets[stored.ID], updated = stored, stored
		return nil
	})
	return updated, err
}

func (b *TicketBridge) AddReport(_ context.Context, req AddReportRequest) (TicketState, error) {
	if err := validateSingleLine("assignment ID", req.AssignmentID, true); err != nil {
		return TicketState{}, err
	}
	if req.Session.SessionID == "" || req.Session.Namespace == "" || req.Session.Name == "" {
		return TicketState{}, operationError("invalid_argument", "invalid_session_ref", "report must reference a MASS Session ID, namespace, and name")
	}
	if err := validateBlockText("report body", req.Body, true); err != nil {
		return TicketState{}, err
	}
	if req.SourceEventID != "" {
		if err := validateSingleLine("source event ID", req.SourceEventID, true); err != nil {
			return TicketState{}, err
		}
	}
	reportID, err := b.newID("r")
	if err != nil {
		return TicketState{}, err
	}
	var updated TicketState
	err = b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		ticket, exists := project.Tickets[req.TicketID]
		if !exists {
			return operationError("not_found", "ticket_not_found", "ticket was not found in this project")
		}
		if terminalBusinessState(ticket.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive worker reports")
		}
		if ticket.AssignmentID != req.AssignmentID {
			return operationError("failed_precondition", "assignment_mismatch", "report does not belong to the ticket's current assignment")
		}
		if req.SourceEventID != "" {
			if existing, ok := ticket.reportBySource(req.SourceEventID); ok {
				if existing.AssignmentID != req.AssignmentID || existing.Session != req.Session ||
					existing.SourceItemID != req.SourceItemID || existing.SourceTurnID != req.SourceTurnID ||
					existing.Structured != req.Structured || existing.ParseError != strings.TrimSpace(req.ParseError) || existing.Body != req.Body {
					return operationError("already_exists", "report_exists", "worker report source event already identifies different content")
				}
				updated = ticket
				return nil
			}
		}
		report := ReportState{
			ID: reportID, AssignmentID: req.AssignmentID, Session: req.Session,
			SourceEventID: req.SourceEventID, SourceItemID: req.SourceItemID, SourceTurnID: req.SourceTurnID,
			Status: "confirmed", Structured: req.Structured, ParseError: strings.TrimSpace(req.ParseError),
			Body: req.Body, ReportedAt: b.now(),
		}
		if assignment, exists := project.Assignments[req.AssignmentID]; exists && assignment.Session != req.Session {
			return operationError("failed_precondition", "assignment_mismatch", "report Session does not match its Assignment")
		}
		if err := recordRouterReport(project, ticket, report); err != nil {
			return err
		}
		ticket.Reports = append(ticket.Reports, report)
		ticket.UpdatedAt = report.ReportedAt
		project.Tickets[ticket.ID], updated = ticket, ticket
		return nil
	})
	return updated, err
}

func recordRouterReport(project *ProjectState, ticket TicketState, report ReportState) error {
	if project.Router == nil || project.Router.SessionID == "" {
		return operationError("failed_precondition", "router_session_missing", "project router Session is not available for worker reports")
	}
	payload := map[string]any{
		"ticketId": ticket.ID, "assignmentId": report.AssignmentID, "sessionId": report.Session.SessionID,
		"session": report.Session.Namespace + "/" + report.Session.Name, "reportId": report.ID,
		"sourceEventId": report.SourceEventID, "sourceItemId": report.SourceItemID, "sourceTurnId": report.SourceTurnID,
		"structured": report.Structured, "report": report.Body,
	}
	if report.ParseError != "" {
		payload["parseError"] = report.ParseError
	}
	if structured, parseError := parseStructuredReport(report.Body); parseError == "" {
		payload["structuredResult"] = structured
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return operationError("invalid_argument", "invalid_worker_report", "worker report event cannot be encoded")
	}
	id := "report/" + report.ID
	if _, exists := project.Inbox[id]; exists {
		return operationError("already_exists", "duplicate_report", "report identifier already exists")
	}
	project.NextInboxSeq++
	project.Inbox[id] = InboxEventState{
		ID: id, Sequence: project.NextInboxSeq, ProjectID: ticket.ProjectID, RootTicketID: ticket.RootTicketID,
		Source: "worker:" + report.Session.Name, Kind: "worker_report", Summary: "Worker report for ticket " + ticket.ID,
		Payload: raw, TargetAssignmentID: report.AssignmentID, TargetWorkerName: project.Router.Name,
		TargetSession: project.Router.SessionRef, RoutingPath: "router", State: "queued",
		SourceEventID: report.SourceEventID, AcceptedAt: report.ReportedAt, UpdatedAt: report.ReportedAt,
	}
	return nil
}

func (b *TicketBridge) FinishTicket(_ context.Context, req FinishTicketRequest) (TicketState, error) {
	if err := req.Resolution.validate(); err != nil {
		return TicketState{}, err
	}
	if err := validateBlockText("resolution summary", req.Summary, true); err != nil {
		return TicketState{}, err
	}
	var updated TicketState
	err := b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		ticket, exists := project.Tickets[req.TicketID]
		if !exists {
			return operationError("not_found", "ticket_not_found", "ticket was not found in this project")
		}
		if req.Resolution == ResolutionReopened {
			if ticket.BusinessState == "" || ticket.BusinessState == string(ResolutionReopened) {
				return operationError("failed_precondition", "ticket_not_reopenable", "only an explicitly blocked, waiting, or terminated Ticket can be reopened")
			}
		} else if terminalBusinessState(ticket.BusinessState) {
			return operationError("failed_precondition", "ticket_already_resolved", "ticket already has a business resolution")
		}
		if ticket.BusinessState == string(req.Resolution) {
			return operationError("failed_precondition", "ticket_state_unchanged", "Ticket already has this business state")
		}
		if req.Resolution == ResolutionCompleted {
			validReport := false
			for _, report := range ticket.Reports {
				if report.AssignmentID == ticket.AssignmentID && report.ID != "" && report.Status == "confirmed" && report.Body != "" {
					validReport = true
				}
			}
			if !validReport {
				return operationError("failed_precondition", "ticket_report_missing", "current Assignment has no confirmed worker report")
			}
		}
		if terminalBusinessState(string(req.Resolution)) {
			if err := requireChildrenDisposed(*project, ticket.ID); err != nil {
				return err
			}
		}
		ticket.BusinessState = string(req.Resolution)
		ticket.Revision++
		ticket.UpdatedAt = b.now()
		if terminalBusinessState(ticket.BusinessState) {
			if assignment, ok := project.Assignments[ticket.AssignmentID]; ok {
				assignment.Status = ticket.BusinessState
				if ticket.BusinessState == string(ResolutionCompleted) {
					assignment.ExecutionState = "completed"
				}
				assignment.UpdatedAt = ticket.UpdatedAt
				project.Assignments[assignment.ID] = assignment
			}
			for key, binding := range project.RouteBindings {
				if binding.AssignmentID == ticket.AssignmentID {
					delete(project.RouteBindings, key)
				}
			}
		}
		summary := strings.TrimSpace(req.Summary)
		recordTicketFact(project, ticket, ticket.BusinessState, summary, ticket.UpdatedAt)
		recordChildLifecycle(project, ticket, ticket.BusinessState, summary, ticket.UpdatedAt)
		project.Tickets[ticket.ID], updated = ticket, ticket
		return nil
	})
	return updated, err
}

func (b *TicketBridge) ticket(projectID, ticketID string) (TicketState, error) {
	if ticketID == "" {
		return TicketState{}, operationError("invalid_argument", "invalid_ticket_id", "ticket ID is required")
	}
	ticket, ok := b.store.Snapshot().Projects[projectID].Tickets[ticketID]
	if !ok {
		return TicketState{}, operationError("not_found", "ticket_not_found", "ticket was not found in this project")
	}
	return ticket, nil
}

func (b *TicketBridge) allocateTicketID(projectID string) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		id, err := b.newID("t")
		if err != nil {
			return "", err
		}
		if _, exists := b.store.Snapshot().Projects[projectID].Tickets[id]; !exists {
			return id, nil
		}
	}
	return "", operationError("unavailable", "id_generation_failed", "ticket identifier could not be allocated")
}

func newRecordID(prefix string) (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", operationError("unavailable", "id_generation_failed", "local identifier could not be generated")
	}
	return prefix + "-" + hex.EncodeToString(raw), nil
}

func terminalBusinessState(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func (ticket TicketState) reportBySource(sourceEventID string) (ReportState, bool) {
	for _, report := range ticket.Reports {
		if report.SourceEventID == sourceEventID {
			return report, true
		}
	}
	return ReportState{}, false
}

func operationReason(err error) string {
	var operation *OperationError
	if errors.As(err, &operation) {
		return operation.Reason
	}
	return "operation_failed"
}
