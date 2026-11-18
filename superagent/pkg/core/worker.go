package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
)

type Report struct {
	ID          string   `json:"id"`
	TopicID     string   `json:"topicId"`
	IssueID     string   `json:"issueId"`
	ExecutionID string   `json:"executionId"`
	BindingID   string   `json:"bindingId"`
	Generation  int      `json:"generation"`
	Revision    int      `json:"goalRevision,omitempty"`
	Sequence    int64    `json:"sequence"`
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary"`
	Details     string   `json:"details,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Questions   []string `json:"questions,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	EventID     string   `json:"eventId"`
	Historical  bool     `json:"historical"`
}

type ReportRequest struct {
	OperationID  string   `json:"operationId"`
	Kind         string   `json:"kind"`
	Summary      string   `json:"summary"`
	Details      string   `json:"details,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Questions    []string `json:"questions,omitempty"`
	Revision     int      `json:"requirementsRevision,omitempty"`
	GoalRevision int      `json:"goalRevision,omitempty"`
}

func workerExecution(ctx context.Context, tx *sql.Tx, b Binding) (Execution, error) {
	var x Execution
	if b.Role != "worker" {
		return x, problem("forbidden", "only Worker can access its bound Execution")
	}
	if err := readJSON(ctx, tx, `SELECT e.body FROM executions e JOIN session_bindings b ON b.id=e.binding_id
 WHERE e.id=? AND e.binding_id=? AND e.issue_id=? AND e.generation=?
 AND (b.state<>'revoked' OR json_extract(e.body,'$.authority')='historical')`, &x, b.OwnerID, b.ID, b.IssueID, b.Generation); err != nil {
		return x, err
	}
	if x.TopicID != b.TopicID {
		return x, problem("stale_binding", "Worker binding scope changed")
	}
	return x, nil
}

func issueForExecution(ctx context.Context, tx *sql.Tx, x Execution) (Issue, error) {
	var issue Issue
	if x.Authority != "current" {
		return issue, problem("stale_binding", "Worker Execution is historical")
	}
	if err := readJSON(ctx, tx, "SELECT body FROM issues WHERE id=? AND topic_id=?", &issue, x.IssueID, x.TopicID); err != nil {
		return issue, err
	}
	if issue.CurrentExecutionID != x.ID || terminal(issue.Status) {
		return issue, problem("stale_binding", "Worker no longer owns the current Issue")
	}
	return issue, nil
}

func currentWorkerIssue(ctx context.Context, tx *sql.Tx, b Binding) (Issue, Execution, error) {
	x, err := workerExecution(ctx, tx, b)
	if err != nil {
		return Issue{}, x, err
	}
	issue, err := issueForExecution(ctx, tx, x)
	return issue, x, err
}

func (s *Store) workerQuery(ctx context.Context, b Binding, tool string, raw []byte) (any, error) {
	if b.Role != "worker" {
		return nil, problem("forbidden", "only Worker can read its bound Issue")
	}
	var query struct {
		MessageID string `json:"messageId"`
	}
	if tool == "issue_get_current" {
		if err := decodeStrict(raw, &struct{}{}); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrict(raw, &query); err != nil {
			return nil, err
		}
		if query.MessageID == "" {
			return nil, problem("invalid_argument", "messageId is required")
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	issue, x, err := currentWorkerIssue(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	if tool == "issue_get_current" {
		return WorkReceipt{Issue: issue, Execution: &x}, nil
	}
	if !slices.ContainsFunc(issue.Materials, func(m Material) bool { return m.MessageID == query.MessageID }) {
		return nil, problem("not_found", "Message is not a material of the bound Issue")
	}
	var message Message
	err = readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE id=?", &message, query.MessageID)
	return message, err
}

func (s *Store) workerReport(ctx context.Context, b Binding, raw []byte) (json.RawMessage, error) {
	if b.Role != "worker" {
		return nil, problem("forbidden", "only Worker can report")
	}
	var r ReportRequest
	if err := decodeStrict(raw, &r); err != nil {
		return nil, err
	}
	switch r.Kind {
	case "progress", "ready", "blocked", "failed":
	default:
		return nil, problem("invalid_argument", "invalid Report kind")
	}
	revision := r.GoalRevision
	if revision == 0 {
		revision = r.Revision
	}
	if strings.TrimSpace(r.Summary) == "" || (r.Revision != 0 && r.Revision != revision) {
		return nil, problem("invalid_argument", "Report summary and one positive goalRevision are required")
	}
	return s.commitWork(ctx, b, "report_submit", r.OperationID, func(ctx context.Context, tx *sql.Tx, binding Binding) (any, error) {
		x, err := workerExecution(ctx, tx, binding)
		if err != nil {
			return nil, err
		}
		historical := x.Authority == "historical"
		var issue Issue
		if historical {
			err = readJSON(ctx, tx, "SELECT body FROM issues WHERE id=? AND topic_id=?", &issue, x.IssueID, x.TopicID)
		} else {
			issue, err = issueForExecution(ctx, tx, x)
		}
		if err != nil {
			return nil, err
		}
		key := creationKey("report", x.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "reports", key, id); err != nil {
			return nil, err
		} else if existing {
			var report Report
			err := readJSON(ctx, tx, "SELECT body FROM reports WHERE id=?", &report, id)
			return report, err
		}
		upper := issue.RequirementsRevision
		if historical && x.RetiredGoalRevision != 0 {
			upper = x.RetiredGoalRevision
		}
		if revision < 1 || revision < x.Revision || revision > upper {
			return nil, problem("invalid_argument", "goalRevision is outside the bound Execution's goal revisions")
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM reports").Scan(&seq); err != nil {
			return nil, storageError()
		}
		report := Report{
			ID: id, TopicID: x.TopicID, IssueID: x.IssueID, ExecutionID: x.ID, BindingID: binding.ID,
			Generation: x.Generation, Revision: revision, Sequence: seq, Kind: r.Kind, Summary: r.Summary, Details: r.Details,
			Evidence: r.Evidence, Questions: r.Questions, CreatedAt: now(), EventID: newID("delivery"), Historical: historical,
		}
		issue.Reports = append(issue.Reports, report)
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		if !historical && x.ReminderEventID != "" {
			if err := supersedeInternalDelivery(ctx, tx, x.ReminderEventID); err != nil {
				return nil, err
			}
		}
		commitGate("report_before_notification")
		kind := reportSubmittedKind
		if historical {
			kind = contextSupplement
		}
		err = enqueueInternalDelivery(ctx, tx, internalDelivery{
			ID: report.EventID, Kind: kind,
			Sender:    deliveryIdentity{Kind: "worker", ExecutionID: x.ID},
			Recipient: deliveryIdentity{Kind: "topic", TopicID: x.TopicID},
			Content:   deliveryContent{Refs: []deliveryReference{{Kind: "report", ID: report.ID, IssueID: issue.ID, GoalRevision: revision}}},
		})
		return report, err
	})
}
