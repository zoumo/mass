package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
)

type UpdateRequirementsRequest struct {
	OperationID      string `json:"operationId"`
	IssueID          string `json:"issueId"`
	ExpectedRevision int    `json:"expectedRevision"`
	Requirements     string `json:"requirements,omitempty"`
	Goal             string `json:"goal,omitempty"`
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

const (
	workerStarted       = "worker_started"
	workerUpdateKind    = "worker_update"
	reportSubmittedKind = "report_submitted"
	reportReminderKind  = "report_reminder"
)

type WorkerUpdateRequest struct {
	OperationID      string     `json:"operationId"`
	IssueID          string     `json:"issueId"`
	ExpectedRevision int        `json:"expectedRevision"`
	Mode             string     `json:"mode"`
	Note             string     `json:"note,omitempty"`
	Materials        []Material `json:"materials,omitempty"`
}

func issueGoal(goal, legacy string) (string, error) {
	if goal == "" {
		goal = legacy
	}
	if strings.TrimSpace(goal) == "" || (legacy != "" && legacy != goal) {
		return "", problem("invalid_argument", "supply one nonblank goal")
	}
	return goal, nil
}

func issueGoalRef(issue Issue) deliveryReference {
	return deliveryReference{Kind: "issue", ID: issue.ID, GoalRevision: issue.RequirementsRevision}
}

func updateRequirements(r UpdateRequirementsRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		goal, err := issueGoal(r.Goal, r.Requirements)
		if err != nil {
			return nil, err
		}
		if goal != issue.Requirements {
			issue.Requirements, issue.UpdatedAt = goal, now()
			issue.Revision++
			issue.RequirementsRevision++
		}
		if !r.NotifyWorker {
			return WorkReceipt{Issue: issue}, saveIssue(ctx, tx, issue)
		}
		key := creationKey("goal-update", b.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "internal_deliveries", key, id); err != nil {
			return nil, err
		} else if existing {
			return WorkReceipt{Issue: issue, NotificationEventID: id}, nil
		}
		issue.LatestRequirementsEventID = id
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		commitGate("requirements_before_notification")
		return notifyWorker(ctx, tx, issue, issue.LatestRequirementsEventID, workerUpdateKind, "", nil)
	}
}

func workerUpdate(r WorkerUpdateRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		key := creationKey("delivery", b.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "internal_deliveries", key, id); err != nil {
			return nil, err
		} else if existing {
			issue, err := topicIssue(ctx, tx, b, r.IssueID)
			return WorkReceipt{Issue: issue, NotificationEventID: id}, err
		}
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		kind := workerUpdateKind
		switch r.Mode {
		case "continue":
		case contextSupplement:
			kind = contextSupplement
			if strings.TrimSpace(r.Note) == "" && len(r.Materials) == 0 {
				return nil, problem("invalid_argument", "context supplement requires a note or materials")
			}
		default:
			return nil, problem("invalid_argument", "mode must be continue or context_supplement")
		}
		if err := validateMaterials(ctx, tx, issue.TopicID, r.Materials); err != nil {
			return nil, err
		}
		if appendMaterials(&issue, r.Materials) {
			issue.Revision++
			issue.UpdatedAt = now()
			if err := saveIssue(ctx, tx, issue); err != nil {
				return nil, err
			}
		}
		return notifyWorker(ctx, tx, issue, id, kind, r.Note, r.Materials)
	}
}

func appendMaterials(issue *Issue, materials []Material) bool {
	before := len(issue.Materials)
	for _, material := range materials {
		if !slices.Contains(issue.Materials, material) {
			issue.Materials = append(issue.Materials, material)
		}
	}
	return len(issue.Materials) != before
}

func canContinueIssue(issue Issue) bool {
	return issue.Status == "active" && len(issue.Blockers) == 0
}

func notifyWorker(ctx context.Context, tx *sql.Tx, issue Issue, id, kind, note string, materials []Material) (WorkReceipt, error) {
	if issue.CurrentExecutionID == "" {
		return WorkReceipt{}, problem("failed_precondition", "Worker update requires a current Execution")
	}
	if kind == workerUpdateKind && !canContinueIssue(issue) {
		return WorkReceipt{}, problem("failed_precondition", "Worker continuation requires an active Issue with all blockers resolved")
	}
	var x Execution
	if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=? AND issue_id=?", &x, issue.CurrentExecutionID, issue.ID); err != nil {
		return WorkReceipt{}, err
	}
	if x.Authority != "current" {
		return WorkReceipt{}, problem("failed_precondition", "Worker update requires a current Execution")
	}
	refs := []deliveryReference{issueGoalRef(issue)}
	for _, material := range materials {
		if material.MessageID != "" {
			refs = append(refs, deliveryReference{Kind: "trigger_message", ID: material.MessageID})
		}
	}
	var body json.RawMessage
	if note != "" || len(materials) != 0 {
		var err error
		body, err = json.Marshal(struct {
			Note      string     `json:"note,omitempty"`
			Materials []Material `json:"materials,omitempty"`
		}{note, materials})
		if err != nil {
			return WorkReceipt{}, err
		}
	}
	err := enqueueInternalDelivery(ctx, tx, internalDelivery{
		ID: id, Kind: kind,
		Sender:    deliveryIdentity{Kind: "topic", TopicID: issue.TopicID},
		Recipient: deliveryIdentity{Kind: "worker", ExecutionID: x.ID},
		Content:   deliveryContent{Body: body, Refs: refs},
	})
	return WorkReceipt{Issue: issue, NotificationEventID: id}, err
}

func workDeliveryCurrent(ctx context.Context, tx *sql.Tx, d internalDelivery) (bool, error) {
	switch d.Kind {
	case workerUpdateKind, reportReminderKind:
	default:
		return true, nil
	}
	var x Execution
	if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=?", &x, d.Recipient.ExecutionID); err != nil {
		return false, err
	}
	issue, err := issueForExecution(ctx, tx, x)
	if errorCode(err) == "stale_binding" || errorCode(err) == "not_found" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if d.Kind == reportReminderKind {
		return d.ID == x.ReminderEventID && !issueHasExecutionReport(issue, x.ID), nil
	}
	return canContinueIssue(issue) && slices.Contains(d.Content.Refs, issueGoalRef(issue)), nil
}
