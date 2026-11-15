package sa

import (
	"encoding/json"
	"fmt"
	"time"
)

// ChildLifecycleFact is a confirmed business fact, not an external Trigger or
// an instruction granting new work. Its inbox recipient is the parent Ticket.
type ChildLifecycleFact struct {
	TicketID            string         `json:"ticketId"`
	ParentTicketID      string         `json:"parentTicketId"`
	RootTicketID        string         `json:"rootTicketId"`
	Revision            uint64         `json:"revision"`
	State               string         `json:"state"`
	Summary             string         `json:"summary"`
	Issue               IssueReference `json:"issue"`
	ResultCommentID     string         `json:"resultCommentId,omitempty"`
	Report              *ReportState   `json:"report,omitempty"`
	CreatedByAssignment string         `json:"createdByAssignment"`
	CreatedBySession    SessionRef     `json:"createdBySession"`
}

// Called only inside the transaction confirming creation or a business change.
// A stable revision identity also makes recovery confirmation idempotent.
func recordChildLifecycle(p *ProjectState, ticket TicketState, state, summary string, now time.Time) {
	if ticket.ParentTicketID == "" {
		return
	}
	id := fmt.Sprintf("child/%s/%d/%s", ticket.ID, ticket.Revision, state)
	if _, exists := p.Inbox[id]; exists {
		return
	}
	fact := &ChildLifecycleFact{TicketID: ticket.ID, ParentTicketID: ticket.ParentTicketID, RootTicketID: ticket.RootTicketID, Revision: ticket.Revision, State: state, Summary: summary, Issue: ticket.Issue, ResultCommentID: ticket.BusinessCommentID, CreatedByAssignment: ticket.CreatedByAssignment, CreatedBySession: ticket.CreatedBySession}
	for i := len(ticket.Reports) - 1; i >= 0; i-- {
		if ticket.Reports[i].Status == "confirmed" {
			report := ticket.Reports[i]
			fact.Report = &report
			break
		}
	}
	payload, _ := json.Marshal(fact)
	if p.Inbox == nil {
		p.Inbox = map[string]InboxEventState{}
	}
	p.NextInboxSeq++
	p.Inbox[id] = InboxEventState{ID: id, Sequence: p.NextInboxSeq, ProjectID: ticket.ProjectID, RootTicketID: ticket.RootTicketID, TicketID: ticket.ParentTicketID, ChildLifecycle: fact, Source: "child_ticket", SourceEventID: id, Kind: "child_ticket." + state, Summary: summary, Payload: payload, RoutingPath: "direct", State: "queued", AcceptedAt: now, UpdatedAt: now}
}

// Explicitly resolve children first, leaves before parents. No relationship or
// authorization is rewritten to make closing possible. Uncertain creation and
// transition reservations are work responsibilities too.
func requireChildrenDisposed(p ProjectState, parentID string) error {
	for _, d := range p.Delegations {
		if d.ParentID == parentID && d.State != "confirmed" {
			return operationError("outcome_unknown", "child_creation_unconfirmed", "reconcile the reserved child Issue before closing its parent")
		}
	}
	for _, child := range p.Tickets {
		if child.ParentTicketID != parentID {
			continue
		}
		if child.Transition != nil || !terminalBusinessState(child.BusinessState) {
			return operationError("failed_precondition", "unfinished_child_tickets", "explicitly resolve or cancel each unfinished direct child before closing its parent; dispose descendants first")
		}
		if err := requireChildrenDisposed(p, child.ID); err != nil {
			return err
		}
	}
	return nil
}
