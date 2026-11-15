package sa

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TicketResolution is the business conclusion of one ticket attempt.
type TicketResolution string

const (
	// ResolutionCompleted accepts the work.
	ResolutionCompleted TicketResolution = "completed"
	// ResolutionFailed records work that could not be completed.
	ResolutionFailed TicketResolution = "failed"
	// ResolutionCancelled records work that was cancelled.
	ResolutionCancelled TicketResolution = "cancelled"
	// ResolutionBlocked records work that is blocked on an external condition.
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

func (r TicketResolution) issueStatus() string {
	switch r {
	case ResolutionBlocked, ResolutionWaitingUser:
		return "blocked"
	case ResolutionReopened:
		return "todo"
	default:
		return "done"
	}
}

// CreateTicketRequest creates one ticket as a child Issue of a root.
type CreateTicketRequest struct {
	Authorization  *WorkAuthorization
	ProjectID      string
	ParentTicketID string
	Title          string
	Objective      string
	Acceptance     string
	AssignmentID   string
}

// RecordAssignmentRequest binds the ticket's current worker Assignment.
type RecordAssignmentRequest struct {
	ProjectID    string
	TicketID     string
	AssignmentID string
}

// AddReportRequest writes one worker report to the ticket Issue.
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

// FinishTicketRequest records the router's explicit ticket conclusion.
type FinishTicketRequest struct {
	ProjectID  string
	TicketID   string
	Resolution TicketResolution
	Summary    string
}

// TicketBridge is the narrow business interface shared by the future MCP tools
// and the management HTTP API. It owns the local ticket state and delegates all
// external writes to one IssueTracker per project.
type TicketBridge struct {
	store    *StateStore
	trackers map[string]IssueTracker
	now      func() time.Time
	newID    func(prefix string) (string, error)
}

// NewTicketBridge binds configured projects to their explicit Multica targets.
func NewTicketBridge(config Config, store *StateStore) (*TicketBridge, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, operationError("invalid_argument", "invalid_state_store", "state store is required")
	}
	trackers := make(map[string]IssueTracker, len(config.Projects))
	for _, project := range config.Projects {
		tracker, err := NewMulticaCLI(config.Multica.Executable, MulticaTarget{
			WorkspaceID: project.Multica.WorkspaceID,
			ProjectID:   project.Multica.ProjectID,
		})
		if err != nil {
			return nil, err
		}
		trackers[project.ID] = tracker
	}
	return &TicketBridge{
		store:    store,
		trackers: trackers,
		now:      func() time.Time { return time.Now().UTC() },
		newID:    newRecordID,
	}, nil
}

// CreateTicket creates the ticket child Issue and persists the ticket.
func (b *TicketBridge) CreateTicket(ctx context.Context, req CreateTicketRequest) (TicketState, error) {
	tracker, err := b.trackerFor(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	var parent TicketState
	if req.ParentTicketID != "" {
		parent, err = b.ticket(req.ProjectID, req.ParentTicketID)
		if err != nil {
			return TicketState{}, err
		}
	}
	if err := validateSingleLine("ticket title", req.Title, true); err != nil {
		return TicketState{}, err
	}
	if err := validateBlockText("ticket objective", req.Objective, true); err != nil {
		return TicketState{}, err
	}
	if err := validateBlockText("ticket acceptance", req.Acceptance, true); err != nil {
		return TicketState{}, err
	}

	ticketID, err := b.allocateTicketID(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	issue, err := tracker.CreateIssue(ctx, CreateIssueInput{
		Title:       strings.TrimSpace(req.Title),
		Description: ticketDescription(req.Objective, req.Acceptance),
		ParentID:    parent.Issue.ID,
	})
	if err != nil {
		return TicketState{}, err
	}
	metadata := map[string]string{
		MetadataProject:  req.ProjectID,
		MetadataTicketID: ticketID,
	}

	if err := tracker.SetMetadata(ctx, issue.ID, metadata); err != nil {
		return TicketState{}, createdButUnconfirmed("ticket issue", issue, err)
	}

	now := b.now()
	ticket := TicketState{
		Authorization: req.Authorization,
		ID:            ticketID,
		ProjectID:     req.ProjectID,
		Title:         strings.TrimSpace(req.Title),
		Objective:     strings.TrimSpace(req.Objective),
		Acceptance:    strings.TrimSpace(req.Acceptance),
		Issue:         IssueReference{ID: issue.ID, Identifier: issue.Identifier},
		Status:        issue.Status,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	ticket.RootTicketID = ticketID
	ticket.Namespace = WorkerNamespace(req.ProjectID, ticketID)
	if parent.ID != "" {
		ticket.ParentTicketID = parent.ID
		ticket.RootTicketID = parent.RootTicketID
		ticket.Namespace = parent.Namespace
		ticket.Depth = parent.Depth + 1
	}

	if err := b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		if _, exists := project.Tickets[ticketID]; exists {
			return operationError("failed_precondition", "duplicate_ticket", "ticket identifier already exists")
		}
		project.Tickets[ticketID] = ticket
		return nil
	}); err != nil {
		return TicketState{}, createdButUnconfirmed("ticket issue", issue, err)
	}
	return ticket, nil
}

// RecordAssignment binds the ticket to its current worker Assignment and writes
// the assignment metadata before the local record.
func (b *TicketBridge) RecordAssignment(ctx context.Context, req RecordAssignmentRequest) (TicketState, error) {
	tracker, err := b.trackerFor(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	if err := validateSingleLine("assignment ID", req.AssignmentID, true); err != nil {
		return TicketState{}, err
	}
	ticket, err := b.ticket(req.ProjectID, req.TicketID)
	if err != nil {
		return TicketState{}, err
	}
	if terminalBusinessState(ticket.BusinessState) {
		return TicketState{}, operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive a new assignment")
	}
	if ticket.AssignmentID == req.AssignmentID {
		return ticket, nil
	}
	previousAssignmentID := ticket.AssignmentID
	if err := tracker.SetMetadata(ctx, ticket.Issue.ID, map[string]string{
		MetadataAssignmentID: req.AssignmentID,
	}); err != nil {
		return TicketState{}, err
	}
	var updated TicketState
	err = b.store.updateProject(req.ProjectID, func(project *ProjectState) error {
		stored, exists := project.Tickets[req.TicketID]
		if !exists {
			return operationError("not_found", "ticket_not_found", "ticket was not found in this project")
		}
		if terminalBusinessState(stored.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive a new assignment")
		}
		if stored.AssignmentID != previousAssignmentID {
			return operationError("failed_precondition", "assignment_changed", "ticket assignment changed before metadata was confirmed")
		}
		stored.AssignmentID = req.AssignmentID
		stored.UpdatedAt = b.now()
		if previousAssignmentID != "" {
			if previous, exists := project.Assignments[previousAssignmentID]; exists {
				previous.Status = "superseded"
				previous.UpdatedAt = b.now()
				project.Assignments[previousAssignmentID] = previous
			}
			for key, binding := range project.RouteBindings {
				if binding.AssignmentID == previousAssignmentID {
					delete(project.RouteBindings, key)
				}
			}
		}
		project.Tickets[req.TicketID] = stored
		updated = stored
		return nil
	})
	if err != nil {
		return TicketState{}, issueConfirmed("ticket issue", ticket.Issue, err)
	}
	return updated, nil
}

// AddReport writes the worker report as a top-level ticket comment and records
// the report reference locally. It never changes the ticket status.
func (b *TicketBridge) AddReport(ctx context.Context, req AddReportRequest) (TicketState, error) {
	tracker, err := b.trackerFor(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	if err := validateSingleLine("assignment ID", req.AssignmentID, true); err != nil {
		return TicketState{}, err
	}
	if req.Session.SessionID == "" || req.Session.Namespace == "" || req.Session.Name == "" {
		return TicketState{}, operationError("invalid_argument", "invalid_session_ref", "report must reference a MASS Session ID, namespace, and name")
	}
	if req.SourceEventID != "" {
		if err := validateSingleLine("source event ID", req.SourceEventID, true); err != nil {
			return TicketState{}, err
		}
	}
	if err := validateBlockText("report body", req.Body, true); err != nil {
		return TicketState{}, err
	}
	ticket, err := b.ticket(req.ProjectID, req.TicketID)
	if err != nil {
		return TicketState{}, err
	}
	if terminalBusinessState(ticket.BusinessState) {
		return TicketState{}, operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive worker reports")
	}
	if ticket.AssignmentID != req.AssignmentID {
		return TicketState{}, operationError("failed_precondition", "assignment_mismatch", "report does not belong to the ticket's current assignment")
	}

	if req.SourceEventID != "" {
		if existing, ok := ticket.reportBySource(req.SourceEventID); ok {
			switch existing.Status {
			case "", "confirmed":
				if existing.CommentID != "" {
					return ticket, nil
				}
				return TicketState{}, operationError("outcome_unknown", "report_write_unconfirmed", "worker report was previously attempted but its comment is not confirmed")
			case "writing", "unknown":
				return TicketState{}, operationError("outcome_unknown", "report_write_unconfirmed", "worker report write outcome is unknown; inspect the Issue before retrying")
			default:
				return TicketState{}, operationError("failed_precondition", "report_already_failed", "worker report for this event previously failed")
			}
		}
	}

	intent := ReportState{
		AssignmentID:  req.AssignmentID,
		Session:       req.Session,
		SourceEventID: req.SourceEventID,
		SourceItemID:  req.SourceItemID,
		SourceTurnID:  req.SourceTurnID,
		Status:        "writing",
		Structured:    req.Structured,
		ParseError:    strings.TrimSpace(req.ParseError),
		ReportedAt:    b.now(),
	}
	if _, err := b.updateTicket(req.ProjectID, req.TicketID, func(stored *TicketState) error {
		if terminalBusinessState(stored.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive worker reports")
		}
		if stored.AssignmentID != req.AssignmentID {
			return operationError("failed_precondition", "assignment_mismatch", "report does not belong to the ticket's current assignment")
		}
		if req.SourceEventID != "" {
			if _, exists := stored.reportBySource(req.SourceEventID); exists {
				return operationError("already_exists", "report_exists", "worker report source event already exists")
			}
		}
		stored.Reports = append(stored.Reports, intent)
		stored.UpdatedAt = b.now()
		return nil
	}); err != nil {
		return TicketState{}, err
	}

	comment, err := tracker.AddComment(ctx, ticket.Issue.ID, workerReportBody(req, b.now()))
	if err != nil {
		unknown := IsErrorCode(err, "outcome_unknown")
		_, _ = b.updateTicket(req.ProjectID, req.TicketID, func(stored *TicketState) error {
			for index := range stored.Reports {
				if !sameReportSource(stored.Reports[index], req.SourceEventID, intent.ReportedAt) {
					continue
				}
				if unknown {
					stored.Reports[index].Status = "unknown"
					stored.Reports[index].ErrorReason = operationReason(err)
					stored.Reports[index].ReportedAt = b.now()
				} else {
					stored.Reports = append(stored.Reports[:index], stored.Reports[index+1:]...)
				}
				stored.UpdatedAt = b.now()
				break
			}
			return nil
		})
		return TicketState{}, err
	}
	updated, err := b.updateTicket(req.ProjectID, req.TicketID, func(stored *TicketState) error {
		if terminalBusinessState(stored.BusinessState) {
			return operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive worker reports")
		}
		if stored.AssignmentID != req.AssignmentID {
			return operationError("failed_precondition", "assignment_mismatch", "report does not belong to the ticket's current assignment")
		}
		found := false
		for index := range stored.Reports {
			if !sameReportSource(stored.Reports[index], req.SourceEventID, intent.ReportedAt) {
				continue
			}
			stored.Reports[index].Status = "confirmed"
			stored.Reports[index].CommentID = comment.ID
			stored.Reports[index].ErrorReason = ""
			stored.Reports[index].ReportedAt = b.now()
			found = true
			break
		}
		if !found {
			return operationError("failed_precondition", "report_intent_missing", "worker report intent changed before comment confirmation")
		}
		stored.UpdatedAt = b.now()
		return nil
	})
	if err != nil {
		return TicketState{}, issueConfirmed("ticket report comment", ticket.Issue, err)
	}
	return updated, nil
}

// FinishTicket maps the business resolution to the Multica status and confirms
// it by readback. The ticket stays open until this method succeeds.
func (b *TicketBridge) FinishTicket(ctx context.Context, req FinishTicketRequest) (TicketState, error) {
	tracker, err := b.trackerFor(req.ProjectID)
	if err != nil {
		return TicketState{}, err
	}
	if err := req.Resolution.validate(); err != nil {
		return TicketState{}, err
	}
	if err := validateBlockText("resolution summary", req.Summary, true); err != nil {
		return TicketState{}, err
	}
	ticket, err := b.ticket(req.ProjectID, req.TicketID)
	if err != nil {
		return TicketState{}, err
	}
	if err := b.beginTicketTransition(req); err != nil {
		return TicketState{}, err
	}
	comment, err := tracker.AddComment(ctx, ticket.Issue.ID, resolutionCommentBody(req, ticket, b.now()))
	if err != nil {
		return TicketState{}, err
	}
	if err := tracker.SetMetadata(ctx, ticket.Issue.ID, map[string]string{
		MetadataResolution: string(req.Resolution),
	}); err != nil {
		return TicketState{}, issueConfirmed("ticket resolution comment", ticket.Issue, err)
	}
	// This write-ahead fence contains enough confirmed evidence to finish by
	// readback after a crash. Recovery never repeats the status mutation.
	if _, err := b.updateTicket(req.ProjectID, req.TicketID, func(stored *TicketState) error {
		stored.Transition.CommentID = comment.ID
		stored.Transition.StatusPending = true
		return nil
	}); err != nil {
		return TicketState{}, err
	}
	issue, err := tracker.ChangeStatus(ctx, ticket.Issue.ID, req.Resolution.issueStatus())
	if err != nil {
		return TicketState{}, err
	}

	updated, err := b.confirmTicketTransition(req.ProjectID, req.TicketID, issue)
	if err != nil {
		return TicketState{}, issueConfirmed("ticket issue", ticket.Issue, err)
	}
	return updated, nil
}

func (b *TicketBridge) trackerFor(projectID string) (IssueTracker, error) {
	tracker, ok := b.trackers[projectID]
	if !ok {
		return nil, operationError("not_found", "unknown_project", "project is not configured")
	}
	return tracker, nil
}

func (b *TicketBridge) ticket(projectID, ticketID string) (TicketState, error) {
	if ticketID == "" {
		return TicketState{}, operationError("invalid_argument", "invalid_ticket_id", "ticket ID is required")
	}
	project := b.store.Snapshot().Projects[projectID]
	ticket, ok := project.Tickets[ticketID]
	if !ok {
		return TicketState{}, operationError("not_found", "ticket_not_found", "ticket was not found in this project")
	}
	return ticket, nil
}

func (b *TicketBridge) updateTicket(projectID, ticketID string, mutate func(*TicketState) error) (TicketState, error) {
	var updated TicketState
	err := b.store.updateProject(projectID, func(project *ProjectState) error {
		ticket, ok := project.Tickets[ticketID]
		if !ok {
			return operationError("not_found", "ticket_not_found", "ticket was not found in this project")
		}
		if err := mutate(&ticket); err != nil {
			return err
		}
		project.Tickets[ticketID] = ticket
		updated = ticket
		return nil
	})
	if err != nil {
		return TicketState{}, err
	}
	return updated, nil
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

func ticketDescription(objective, acceptance string) string {
	var description strings.Builder
	description.WriteString("## Objective\n\n")
	description.WriteString(strings.TrimSpace(objective))
	description.WriteString("\n\n## Acceptance\n\n")
	description.WriteString(strings.TrimSpace(acceptance))
	description.WriteString("\n")
	return description.String()
}

func workerReportBody(req AddReportRequest, now time.Time) string {
	var body strings.Builder
	body.WriteString("## Worker report\n\n")
	body.WriteString("- Assignment: `" + req.AssignmentID + "`\n")
	body.WriteString("- MASS Session: `" + req.Session.Namespace + "/" + req.Session.Name + "` (`" + req.Session.SessionID + "`)\n")
	if req.SourceEventID != "" {
		body.WriteString("- MASS event: `" + req.SourceEventID + "`\n")
	}
	if req.SourceTurnID != "" {
		body.WriteString("- MASS turn: `" + req.SourceTurnID + "`\n")
	}
	body.WriteString("- Structured result: `" + fmt.Sprintf("%t", req.Structured) + "`\n")
	if req.ParseError != "" {
		body.WriteString("- Structured parse: `failed`; the original final text is preserved below\n")
	}
	body.WriteString("- Reported at: " + now.Format(time.RFC3339) + "\n\n")
	body.WriteString(strings.TrimSpace(req.Body))
	body.WriteString("\n")
	return body.String()
}

func resolutionCommentBody(req FinishTicketRequest, ticket TicketState, now time.Time) string {
	var body strings.Builder
	body.WriteString("## Ticket resolution\n\n")
	body.WriteString("- Resolution: `" + string(req.Resolution) + "`\n")
	if ticket.AssignmentID != "" {
		body.WriteString("- Assignment: `" + ticket.AssignmentID + "`\n")
	}
	body.WriteString("- Accepted at: " + now.Format(time.RFC3339) + "\n\n")
	body.WriteString(strings.TrimSpace(req.Summary))
	body.WriteString("\n")
	return body.String()
}

func createdButUnconfirmed(kind string, issue IssueSnapshot, err error) error {
	return fmt.Errorf("%s %s was created but its local record was not confirmed: %w", kind, issueDisplay(issue), err)
}

func issueConfirmed(kind string, issue IssueReference, err error) error {
	return fmt.Errorf("%s %s was updated but its local record was not confirmed: %w", kind, issue.Identifier, err)
}

func issueDisplay(issue IssueSnapshot) string {
	if issue.Identifier != "" {
		return issue.Identifier
	}
	return issue.ID
}

func (ticket TicketState) reportBySource(sourceEventID string) (ReportState, bool) {
	for _, report := range ticket.Reports {
		if report.SourceEventID == sourceEventID {
			return report, true
		}
	}
	return ReportState{}, false
}

func sameReportSource(report ReportState, sourceEventID string, reportedAt time.Time) bool {
	if sourceEventID != "" {
		return report.SourceEventID == sourceEventID
	}
	return report.SourceEventID == "" && report.ReportedAt.Equal(reportedAt)
}

func operationReason(err error) string {
	var operation *OperationError
	if errors.As(err, &operation) {
		return operation.Reason
	}
	return "operation_failed"
}
