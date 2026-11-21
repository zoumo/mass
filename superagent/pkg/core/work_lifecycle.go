package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type LifecycleRequest struct {
	OperationID      string `json:"operationId"`
	ExpectedRevision int    `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

type IssueLifecycleRequest struct {
	LifecycleRequest
	IssueID string `json:"issueId"`
}

type LifecycleChange struct {
	Reason    string `json:"reason"`
	At        string `json:"at"`
	BindingID string `json:"bindingId"`
}

// BindingClose records verified resource release separately from observation of
// a particular native turn. Never infer a turn outcome from this resource fact.
type BindingClose struct {
	At              string `json:"at"`
	ResourceVersion string `json:"resourceVersion"`
	Method          string `json:"method"`
	Reusable        bool   `json:"reusable"`
}

func changeIssueLifecycle(tool string, r IssueLifecycleRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		if _, err := currentTopic(ctx, tx, b); err != nil {
			return nil, err
		}
		issue, err := topicIssue(ctx, tx, b, r.IssueID)
		if err != nil {
			return nil, err
		}
		if err := checkRevision(issue.ID, r.ExpectedRevision, issue.Revision); err != nil {
			return nil, err
		}
		change := LifecycleChange{Reason: r.Reason, At: now(), BindingID: b.ID}
		if strings.TrimSpace(change.Reason) == "" {
			return nil, problem("invalid_argument", "a lifecycle reason is required")
		}
		if tool == "issue_cancel" {
			return cancelIssue(ctx, tx, issue, change)
		}
		if !terminal(issue.Status) {
			return nil, problem("failed_precondition", "Issue must be terminal before reopening")
		}
		prior := issue.Status
		issue.Status, issue.UpdatedAt, issue.Reopening = "pending", change.At, &change
		if len(issue.Blockers) != 0 {
			issue.Status = "blocked"
		}
		issue.Revision++
		commitGate("lifecycle_before_transition")
		return WorkReceipt{Issue: issue}, saveIssueLifecycleTransition(ctx, tx, issue, prior)
	}
}

func cancelIssue(ctx context.Context, tx *sql.Tx, issue Issue, change LifecycleChange) (WorkReceipt, error) {
	if terminal(issue.Status) {
		return WorkReceipt{}, problem("failed_precondition", "Issue is already terminal")
	}
	var execution *Execution
	if issue.CurrentExecutionID != "" {
		x, err := retireExecution(ctx, tx, issue.CurrentExecutionID, change.At, true)
		if err != nil {
			return WorkReceipt{}, err
		}
		execution = &x
	}
	prior := issue.Status
	issue.Status, issue.UpdatedAt, issue.CurrentExecutionID, issue.Cancellation = "canceled", change.At, "", &change
	issue.Revision++
	commitGate("lifecycle_before_transition")
	return WorkReceipt{Issue: issue, Execution: execution}, saveIssueLifecycleTransition(ctx, tx, issue, prior)
}

func changeTopicLifecycle(tool string, r LifecycleRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		var t Topic
		if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &t, b.OwnerID); err != nil {
			return nil, err
		}
		if err := checkRevision(t.ID, r.ExpectedRevision, t.Revision); err != nil {
			return nil, err
		}
		change := LifecycleChange{Reason: r.Reason, At: now(), BindingID: b.ID}
		if strings.TrimSpace(change.Reason) == "" {
			return nil, problem("invalid_argument", "a lifecycle reason is required")
		}
		if tool == "topic_cancel" {
			if terminal(t.Status) {
				return nil, problem("failed_precondition", "Topic is already terminal")
			}
			t.Status, t.Cancellation = "canceled", &change
			if err := cancelTopicWork(ctx, tx, b, change); err != nil {
				return nil, err
			}
		} else {
			if !terminal(t.Status) {
				return nil, problem("failed_precondition", "Topic must be terminal before reopening")
			}
			t.Status, t.Reopening = "active", &change
		}
		t.Revision++
		t.LifecycleGeneration++
		t.UpdatedAt = change.At
		return t, putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", t, t.ID)
	}
}

func cancelTopicWork(ctx context.Context, tx *sql.Tx, b Binding, change LifecycleChange) error {
	if err := invalidateTimers(ctx, tx, b.OwnerID, change.At); err != nil {
		return err
	}
	// Fence lifecycle commands immediately. Undelivered source and report facts
	// remain available for the next Topic binding.
	if err := revokeBinding(ctx, tx, b.ID); err != nil {
		return err
	}
	if _, err := requestBindingStop(ctx, tx, b.ID, change.At); err != nil {
		return err
	}
	var items []Issue
	rows, err := tx.QueryContext(ctx, `SELECT body FROM issues WHERE topic_id=?
 AND json_extract(body,'$.status') NOT IN ('completed','canceled') ORDER BY seq`, b.OwnerID)
	if err != nil {
		return storageError()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var issue Issue
		var raw []byte
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &issue) != nil {
			return storageError()
		}
		items = append(items, issue)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return storageError()
	}
	for _, issue := range items {
		if _, err := cancelIssue(ctx, tx, issue, change); err != nil {
			return err
		}
	}
	return nil
}

func revokeBinding(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE session_bindings SET state='revoked',next_attempt_at='',
 body=json_set(body,'$.state','revoked','$.nextAttemptAt','') WHERE id=?`, id)
	if err != nil {
		return storageError()
	}
	return nil
}

func previousIssue(ctx context.Context, tx *sql.Tx, issueID string) (Binding, error) {
	var b Binding
	err := readJSON(ctx, tx, `SELECT b.body FROM executions e JOIN session_bindings b ON b.id=e.binding_id
 WHERE e.issue_id=? ORDER BY e.generation DESC LIMIT 1`, &b, issueID)
	var p *Error
	if errors.As(err, &p) && p.Code == "not_found" {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if b.State != "revoked" || b.PreparationStarted && b.Close == nil {
		return b, &Error{Code: "failed_precondition", Message: "Previous Issue Session release is unconfirmed; inspect and recover the retired binding", Resource: b.ID}
	}
	return b, nil
}
