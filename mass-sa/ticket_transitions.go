package sa

import (
	"context"
	"log/slog"
)

// A non-nil transition fences all subsequent mutations until its remote
// outcome is confirmed. StatusPending means comment and metadata readbacks
// succeeded and a status write may have happened.
type TicketTransition struct {
	State         string `json:"state"`
	Summary       string `json:"summary"`
	CommentID     string `json:"commentId,omitempty"`
	StatusPending bool   `json:"statusPending"`
}

func terminalBusinessState(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func (b *TicketBridge) beginTicketTransition(req FinishTicketRequest) error {
	err := b.store.updateProject(req.ProjectID, func(p *ProjectState) error {
		ticket, exists := p.Tickets[req.TicketID]
		if !exists {
			return operationError("not_found", "ticket_not_found", "ticket was not found in this project")
		}
		if ticket.Transition != nil {
			return operationError("outcome_unknown", "ticket_transition_unconfirmed", "previous business transition needs readback; do not repeat external writes")
		}
		if req.Resolution == ResolutionReopened {
			for parentID := ticket.ParentTicketID; parentID != ""; {
				parent := p.Tickets[parentID]
				if parent.Transition != nil {
					return operationError("failed_precondition", "parent_transition_pending", "wait for the ancestor's pending closure to be reconciled before reopening child work")
				}
				parentID = parent.ParentTicketID
			}
			if ticket.BusinessState == "" || ticket.BusinessState == "reopened" {
				return operationError("failed_precondition", "ticket_not_reopenable", "only an explicitly blocked, waiting, or terminated Ticket can be reopened")
			}
		} else if terminalBusinessState(ticket.BusinessState) {
			return operationError("failed_precondition", "ticket_already_resolved", "ticket already has a business resolution")
		}
		if ticket.BusinessState == string(req.Resolution) {
			return operationError("failed_precondition", "ticket_state_unchanged", "Ticket already has this business state")
		}
		if terminalBusinessState(string(req.Resolution)) {
			if err := requireChildrenDisposed(*p, ticket.ID); err != nil {
				return err
			}
		}
		ticket.Transition = &TicketTransition{State: string(req.Resolution), Summary: req.Summary}
		p.Tickets[ticket.ID] = ticket
		return nil
	})
	return err
}

func (b *TicketBridge) confirmTicketTransition(projectID, ticketID string, issue IssueSnapshot) (TicketState, error) {
	var result TicketState
	err := b.store.updateProject(projectID, func(p *ProjectState) error {
		ticket := p.Tickets[ticketID]
		transition := ticket.Transition
		if transition == nil || !transition.StatusPending || issue.ID != ticket.Issue.ID || issue.Status != TicketResolution(transition.State).issueStatus() {
			return operationError("outcome_unknown", "ticket_transition_unconfirmed", "business transition is not confirmed by Issue readback")
		}
		ticket.BusinessState = transition.State
		ticket.Status = issue.Status
		ticket.BusinessCommentID = transition.CommentID
		ticket.Revision++
		ticket.UpdatedAt = b.now()
		ticket.Transition = nil
		if terminalBusinessState(transition.State) {
			if assignment, ok := p.Assignments[ticket.AssignmentID]; ok {
				assignment.Status = transition.State
				if transition.State == "completed" {
					assignment.ExecutionState = "completed"
				}
				assignment.UpdatedAt = ticket.UpdatedAt
				p.Assignments[assignment.ID] = assignment
			}
			for key, binding := range p.RouteBindings {
				if binding.AssignmentID == ticket.AssignmentID {
					delete(p.RouteBindings, key)
				}
			}
		}
		recordTicketFact(p, ticket, *transition, ticket.UpdatedAt)
		recordChildLifecycle(p, ticket, transition.State, transition.Summary, ticket.UpdatedAt)
		p.Tickets[ticket.ID], result = ticket, ticket
		return nil
	})
	return result, err
}

// ReconcileTicketTransition only reads remote state. It cannot retry a remote
// mutation or resume execution, even if the original call failed or was lost.
func (b *TicketBridge) ReconcileTicketTransition(ctx context.Context, projectID, ticketID string) (TicketState, error) {
	tracker, err := b.trackerFor(projectID)
	if err != nil {
		return TicketState{}, err
	}
	ticket, err := b.ticket(projectID, ticketID)
	if err != nil {
		return TicketState{}, err
	}
	if ticket.Transition == nil {
		return ticket, nil
	}
	if !ticket.Transition.StatusPending {
		return TicketState{}, operationError("outcome_unknown", "ticket_transition_unconfirmed", "comment or metadata outcome needs operator reconciliation; no write was repeated")
	}
	issue, err := tracker.GetIssue(ctx, ticket.Issue.ID)
	if err != nil {
		return TicketState{}, err
	}
	return b.confirmTicketTransition(projectID, ticketID, issue)
}

func (b *TicketBridge) recoverTicketTransitions(ctx context.Context) {
	for projectID, project := range b.store.Snapshot().Projects {
		for _, ticket := range project.Tickets {
			if ticket.Transition == nil {
				continue
			}
			if _, err := b.ReconcileTicketTransition(ctx, projectID, ticket.ID); err != nil {
				slog.Warn("business transition still needs reconciliation", "project", projectID, "ticket", ticket.ID, "reason", operationReason(err))
			}
		}
	}
}
