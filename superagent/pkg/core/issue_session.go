package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
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

var _ AgentPromptView = Report{}

func (r Report) MarshalAgentPromptJSON() ([]byte, error) {
	view := map[string]any{
		"id": r.ID, "issueId": r.IssueID, "kind": r.Kind, "summary": r.Summary,
		"executionId": r.ExecutionID, "historical": r.Historical,
	}
	if r.Revision != 0 {
		view["goalRevision"] = r.Revision
	}
	if r.Details != "" {
		view["details"] = r.Details
	}
	if len(r.Evidence) != 0 {
		view["evidence"] = r.Evidence
	}
	if len(r.Questions) != 0 {
		view["questions"] = r.Questions
	}
	return json.Marshal(view)
}

type ReportRequest struct {
	OperationID  string   `json:"operationId"`
	Kind         string   `json:"kind"`
	Summary      string   `json:"summary"`
	Details      string   `json:"details,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Questions    []string `json:"questions,omitempty"`
	GoalRevision int      `json:"goalRevision,omitempty"`
}

func boundIssueExecution(ctx context.Context, tx *sql.Tx, b Binding) (Execution, error) {
	var x Execution
	if b.Role != "issue" {
		return x, problem("forbidden", "only Issue Session can access its bound Execution")
	}
	if err := readJSON(ctx, tx, `SELECT e.body FROM executions e JOIN session_bindings b ON b.id=e.binding_id
 WHERE e.id=? AND e.binding_id=? AND e.issue_id=? AND e.generation=?
 AND (b.state<>'revoked' OR json_extract(e.body,'$.authority')='historical')`, &x, b.OwnerID, b.ID, b.IssueID, b.Generation); err != nil {
		return x, err
	}
	if x.TopicID != b.TopicID {
		return x, problem("stale_binding", "Issue Session binding scope changed")
	}
	return x, nil
}

func issueForExecution(ctx context.Context, tx *sql.Tx, x Execution) (Issue, error) {
	var issue Issue
	if x.Authority != "current" {
		return issue, problem("stale_binding", "Issue Session Execution is historical")
	}
	if err := readJSON(ctx, tx, "SELECT body FROM issues WHERE id=? AND topic_id=?", &issue, x.IssueID, x.TopicID); err != nil {
		return issue, err
	}
	if issue.CurrentExecutionID != x.ID || terminal(issue.Status) {
		return issue, problem("stale_binding", "Issue Session no longer owns the current Issue")
	}
	return issue, nil
}

func currentBoundIssue(ctx context.Context, tx *sql.Tx, b Binding) (Issue, Execution, error) {
	x, err := boundIssueExecution(ctx, tx, b)
	if err != nil {
		return Issue{}, x, err
	}
	issue, err := issueForExecution(ctx, tx, x)
	return issue, x, err
}

func (s *Store) queryBoundIssue(ctx context.Context, b Binding, tool string, raw []byte) (any, error) {
	if b.Role != "issue" {
		return nil, problem("forbidden", "only Issue Session can read its bound Issue")
	}
	var query struct {
		MessageID string `json:"messageId"`
	}
	if tool == "get_issue" {
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
	issue, x, err := currentBoundIssue(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	if tool == "get_issue" {
		return WorkReceipt{Issue: issue, Execution: &x}, nil
	}
	if !slices.ContainsFunc(issue.Materials, func(m Material) bool { return m.MessageID == query.MessageID }) {
		return nil, problem("not_found", "Message is not a material of the bound Issue")
	}
	if b.AgentPromptVersion == 1 {
		var message json.RawMessage
		err = readJSON(ctx, tx, `SELECT json_patch(
 json_object('id',id,'text',body->>'$.text','actor',body->>'$.actor'),
 json_object('at',body->>'$.at','url',body->>'$.url'))
 FROM trigger_messages WHERE id=?`, &message, query.MessageID)
		return message, err
	}
	var message Message
	err = readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE id=?", &message, query.MessageID)
	if err != nil || b.AgentPromptVersion == 0 {
		return message, err
	}
	view, err := message.MarshalAgentPromptJSON()
	return json.RawMessage(view), err
}

func (s *Store) submitIssueReport(ctx context.Context, b Binding, raw []byte) (json.RawMessage, error) {
	if b.Role != "issue" {
		return nil, problem("forbidden", "only Issue Session can report")
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
	if strings.TrimSpace(r.Summary) == "" || r.GoalRevision < 1 {
		return nil, problem("invalid_argument", "Report summary and a positive goalRevision are required")
	}
	return s.commitWork(ctx, b, "submit_report", r.OperationID, func(ctx context.Context, tx *sql.Tx, binding Binding) (any, error) {
		x, err := boundIssueExecution(ctx, tx, binding)
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
		key := "$." + creationKey("report", x.ID, r.OperationID)
		digest := creationDigest(r)
		id, err := numberedCreation(ctx, tx, digest, `SELECT json_extract(creation,'$.id'),json_extract(creation,'$.digest')
 FROM (SELECT json_extract(report_creations,?2) AS creation FROM issues WHERE id=?1)
 WHERE creation IS NOT NULL`, issue.ID, key)
		if err != nil {
			return nil, err
		}
		if id != "" {
			var report Report
			err := readJSON(ctx, tx, "SELECT body FROM reports WHERE id=?", &report, id)
			if err == nil {
				markMCPMutation(ctx, true, false)
			}
			return report, err
		}
		upper := issue.RequirementsRevision
		if historical && x.RetiredGoalRevision != 0 {
			upper = x.RetiredGoalRevision
		}
		if r.GoalRevision < x.Revision || r.GoalRevision > upper {
			return nil, problem("invalid_argument", "goalRevision is outside the bound Execution's goal revisions")
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM reports").Scan(&seq); err != nil {
			return nil, storageError()
		}
		report := Report{
			ID: issue.ID + "-r" + strconv.Itoa(len(issue.Reports)+1), TopicID: x.TopicID, IssueID: x.IssueID, ExecutionID: x.ID, BindingID: binding.ID,
			Generation: x.Generation, Revision: r.GoalRevision, Sequence: seq, Kind: r.Kind, Summary: r.Summary, Details: r.Details,
			Evidence: r.Evidence, Questions: r.Questions, CreatedAt: now(), EventID: newID("evt"), Historical: historical,
		}
		issue.Reports = append(issue.Reports, report)
		if err := putJSON(ctx, tx, `UPDATE issues SET body=?5,
 report_creations=json_set(report_creations,?2,json_object('id',?3,'digest',?4))
 WHERE id=?1`, issue, issue.ID, key, report.ID, digest); err != nil {
			return nil, err
		}
		if !historical && x.ReminderEventID != "" {
			if err := supersedeInternalEvent(ctx, tx, x.ReminderEventID); err != nil {
				return nil, err
			}
		}
		commitGate("report_before_notification")
		kind := reportSubmittedKind
		if historical {
			kind = contextSupplement
		}
		err = enqueueInternalEvent(ctx, tx, internalEvent{
			ID: report.EventID, Kind: kind,
			Sender:    eventIdentity{Kind: "issue", ExecutionID: x.ID},
			Recipient: eventIdentity{Kind: "topic", TopicID: x.TopicID},
			Refs:      []eventReference{{Kind: "report", ID: report.ID, IssueID: issue.ID, GoalRevision: r.GoalRevision}},
		})
		if err == nil {
			markMCPMutation(ctx, false, true)
		}
		return report, err
	})
}
