package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type UpdateRequirementsRequest struct {
	OperationID      string `json:"operationId"`
	IssueID          string `json:"issueId"`
	ExpectedRevision int    `json:"expectedRevision"`
	Requirements     string `json:"requirements"`
	NotifyWorker     bool   `json:"notifyWorker,omitempty"`
}

func editableIssue(ctx context.Context, tx *sql.Tx, b Binding, id string, expected int) (Issue, error) {
	if _, err := currentTopic(ctx, tx, b); err != nil {
		return Issue{}, err
	}
	issue, err := topicIssue(ctx, tx, b, id)
	if err != nil {
		return issue, err
	}
	if err := checkRevision(issue.ID, expected, issue.Revision); err != nil {
		return issue, err
	}
	if terminal(issue.Status) {
		return issue, problem("failed_precondition", "Issue must not be terminal")
	}
	return issue, nil
}

func updateRequirements(r UpdateRequirementsRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.Requirements) == "" {
			return nil, problem("invalid_argument", "requirements must not be blank")
		}
		issue.Requirements, issue.UpdatedAt = r.Requirements, now()
		issue.Revision++
		issue.RequirementsRevision++
		if !r.NotifyWorker {
			return WorkReceipt{Issue: issue}, saveIssue(ctx, tx, issue)
		}
		if issue.CurrentExecutionID == "" {
			return nil, problem("failed_precondition", "notifyWorker requires a current Execution")
		}
		var x Execution
		if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=?", &x, issue.CurrentExecutionID); err != nil {
			return nil, err
		}
		if x.Authority != "current" {
			return nil, problem("failed_precondition", "notifyWorker requires a current Execution")
		}
		eventID := newID("evt")
		issue.LatestRequirementsEventID = eventID
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		commitGate("requirements_before_notification")
		payload, err := json.Marshal(struct {
			TopicID      string `json:"topicId"`
			IssueID      string `json:"issueId"`
			ExecutionID  string `json:"executionId"`
			Requirements string `json:"requirements"`
		}{issue.TopicID, issue.ID, x.ID, issue.Requirements})
		if err != nil {
			return nil, err
		}
		err = EnqueueEvent(ctx, tx, Event{
			ID: eventID, Type: "issue.requirements_updated", At: issue.UpdatedAt, Intent: "act",
			BindingID: x.BindingID, TargetID: x.ID, TargetRole: "worker", TargetGeneration: x.Generation,
			Revision: issue.RequirementsRevision, Payload: payload,
		})
		return WorkReceipt{Issue: issue, NotificationEventID: eventID}, err
	}
}
