package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	coreent "github.com/zoumo/mass/superagent/pkg/core/internal/ent"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/sessionbinding"
)

// Both lifecycle reads must seek the partial index built at startup. SQLite
// can otherwise prefer scanning the Topic's full status index for EXISTS.
const unfinishedIssuesInTopic = "FROM issues INDEXED BY issues_unfinished WHERE topic_id=? AND " + unfinishedIssue

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
	Reason           string `json:"reason"`
	At               string `json:"at"`
	SessionBindingID string `json:"sessionBindingId"`
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
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
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
		change := LifecycleChange{Reason: r.Reason, At: now(), SessionBindingID: b.ID}
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
	var retired *SessionBinding
	if issue.CurrentSessionBindingID != "" {
		x, err := retireIssueBinding(ctx, tx, issue, change.At, true)
		if err != nil {
			return WorkReceipt{}, err
		}
		retired = &x
	}
	prior := issue.Status
	issue.Status, issue.UpdatedAt, issue.CurrentSessionBindingID, issue.Cancellation = "canceled", change.At, "", &change
	issue.Revision++
	commitGate("lifecycle_before_transition")
	return WorkReceipt{Issue: issue, SessionBinding: retired}, saveIssueLifecycleTransition(ctx, tx, issue, prior)
}

func changeTopicLifecycle(tool string, r LifecycleRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		t, err := readTopic(ctx, tx, b.OwnerID)
		if err != nil {
			return nil, err
		}
		if err := checkRevision(t.ID, r.ExpectedRevision, t.Revision); err != nil {
			return nil, err
		}
		change := LifecycleChange{Reason: r.Reason, At: now(), SessionBindingID: b.ID}
		if strings.TrimSpace(change.Reason) == "" {
			return nil, problem("invalid_argument", "a lifecycle reason is required")
		}
		if tool == "topic_cancel" {
			if terminal(t.Status) {
				return nil, problem("failed_precondition", "Topic is already terminal")
			}
			t.Status, t.Cancellation = "canceled", &change
			if err := cancelTopicWork(ctx, tx, b, t.LifecycleGeneration, change); err != nil {
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
		return t, saveTopic(ctx, tx, t)
	}
}

func cancelTopicWork(ctx context.Context, tx *sql.Tx, b SessionBinding, topicGeneration int, change LifecycleChange) error {
	if err := closeTopicScheduledTasks(ctx, tx, b.OwnerID, topicGeneration, change.At); err != nil {
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
	rows, err := tx.QueryContext(ctx, "SELECT body "+unfinishedIssuesInTopic+" ORDER BY seq", b.OwnerID)
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

func previousIssueBinding(ctx context.Context, tx *sql.Tx, issueID string) (SessionBinding, error) {
	var b SessionBinding
	row, err := entClientForTx(tx).SessionBinding.Query().Where(sessionbinding.RoleEQ("issue"), sessionbinding.OwnerIDEQ(issueID)).
		Order(sessionbinding.ByGeneration(entsql.OrderDesc())).Select(sessionbinding.FieldBody).First(ctx)
	if err != nil {
		if coreent.IsNotFound(err) {
			return b, nil
		}
		return b, storageError()
	}
	b, err = decodeEntityJSON[SessionBinding](row.Body, nil)
	if err != nil {
		return b, err
	}
	if b.State != "revoked" || b.PreparationStarted && b.Close == nil {
		return b, &Error{Code: "failed_precondition", Message: "Previous Issue Session release is unconfirmed; inspect and recover the retired binding", Resource: b.ID}
	}
	return b, nil
}
