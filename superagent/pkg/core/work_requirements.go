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
	Goal             string `json:"goal,omitempty"`
	NotifyIssueSession     bool   `json:"notifyIssueSession,omitempty"`
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
	issueSessionStarted       = "issue_session_started"
	issueSessionUpdateKind    = "issue_session_update"
	reportSubmittedKind = "report_submitted"
	reportReminderKind  = "report_reminder"
)

type IssueSessionInputRequest struct {
	OperationID      string             `json:"operationId"`
	IssueID          string             `json:"issueId"`
	ExpectedRevision int                `json:"expectedRevision"`
	Mode             string             `json:"mode"`
	Content          *StructuredContent `json:"content,omitempty"`
	Materials        []Material         `json:"materials,omitempty"`
}

func validateGoal(goal string) error {
	if strings.TrimSpace(goal) == "" {
		return problem("invalid_argument", "goal is required")
	}
	return nil
}

func issueGoalRef(issue Issue) eventReference {
	return eventReference{Kind: "issue", ID: issue.ID, GoalRevision: issue.RequirementsRevision}
}

func updateRequirements(r UpdateRequirementsRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if err := validateGoal(r.Goal); err != nil {
			return nil, err
		}
		if r.Goal != issue.Requirements {
			issue.Requirements, issue.UpdatedAt = r.Goal, now()
			issue.Revision++
			issue.RequirementsRevision++
		}
		if !r.NotifyIssueSession {
			return WorkReceipt{Issue: issue}, saveIssue(ctx, tx, issue)
		}
		key := creationKey("goal-update", b.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "internal_events", "event_id", key, id); err != nil {
			return nil, err
		} else if existing {
			return WorkReceipt{Issue: issue, NotificationEventID: id}, nil
		}
		issue.LatestRequirementsEventID = id
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		commitGate("requirements_before_notification")
		return notifyIssueSession(ctx, tx, issue, issue.LatestRequirementsEventID, issueSessionUpdateKind, nil, nil)
	}
}

func issueSessionInput(r IssueSessionInputRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		if r.Content != nil {
			if err := r.Content.normalize(); err != nil {
				return nil, err
			}
		}
		key := creationKey("evt", b.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "internal_events", "event_id", key, id); err != nil {
			return nil, err
		} else if existing {
			issue, err := topicIssue(ctx, tx, b, r.IssueID)
			return WorkReceipt{Issue: issue, NotificationEventID: id}, err
		}
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		kind := issueSessionUpdateKind
		switch r.Mode {
		case "continue":
		case contextSupplement:
			kind = contextSupplement
			if r.Content == nil && len(r.Materials) == 0 {
				return nil, problem("invalid_argument", "context supplement requires content or materials")
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
		return notifyIssueSession(ctx, tx, issue, id, kind, r.Content, r.Materials)
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

func notifyIssueSession(ctx context.Context, tx *sql.Tx, issue Issue, id, kind string, content *StructuredContent, materials []Material) (WorkReceipt, error) {
	if issue.CurrentExecutionID == "" {
		return WorkReceipt{}, problem("failed_precondition", "Issue Session update requires a current Execution")
	}
	if kind == issueSessionUpdateKind && !canContinueIssue(issue) {
		return WorkReceipt{}, problem("failed_precondition", "Issue Session continuation requires an active Issue with all blockers resolved")
	}
	var x Execution
	if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=? AND issue_id=?", &x, issue.CurrentExecutionID, issue.ID); err != nil {
		return WorkReceipt{}, err
	}
	if x.Authority != "current" {
		return WorkReceipt{}, problem("failed_precondition", "Issue Session update requires a current Execution")
	}
	refs := []eventReference{issueGoalRef(issue)}
	for _, material := range materials {
		if material.MessageID != "" {
			refs = append(refs, eventReference{Kind: "trigger_message", ID: material.MessageID})
		}
	}
	var body json.RawMessage
	if len(materials) != 0 {
		var err error
		body, err = json.Marshal(struct {
			Materials []Material `json:"materials"`
		}{materials})
		if err != nil {
			return WorkReceipt{}, err
		}
	}
	err := enqueueInternalEvent(ctx, tx, internalEvent{
		ID: id, Kind: kind,
		Sender:    eventIdentity{Kind: "topic", TopicID: issue.TopicID},
		Recipient: eventIdentity{Kind: "issue", ExecutionID: x.ID},
		Content:   content,
		Body:      body,
		Refs:      refs,
	})
	return WorkReceipt{Issue: issue, NotificationEventID: id}, err
}

func workEventCurrent(ctx context.Context, tx *sql.Tx, d internalEvent) (bool, error) {
	switch d.Kind {
	case issueSessionUpdateKind, reportReminderKind:
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
	return canContinueIssue(issue) && slices.Contains(d.Refs, issueGoalRef(issue)), nil
}
