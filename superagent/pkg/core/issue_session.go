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
	ID               string   `json:"id"`
	TopicID          string   `json:"topicId"`
	IssueID          string   `json:"issueId"`
	SessionBindingID string   `json:"sessionBindingId"`
	Revision         int      `json:"goalRevision,omitempty"`
	Sequence         int64    `json:"sequence"`
	Kind             string   `json:"kind"`
	Summary          string   `json:"summary"`
	Details          string   `json:"details,omitempty"`
	Evidence         []string `json:"evidence,omitempty"`
	Questions        []string `json:"questions,omitempty"`
	CreatedAt        string   `json:"createdAt"`
	EventID          string   `json:"eventId"`
	Historical       bool     `json:"historical"`
	Current          bool     `json:"-"`
}

func reportIsCurrent(report Report, issue Issue) bool {
	return !report.Historical && issue.CurrentSessionBindingID == report.SessionBindingID && issue.RequirementsRevision == report.Revision
}

var _ AgentPromptView = Report{}

func (r Report) MarshalAgentPromptJSON() ([]byte, error) {
	view := map[string]any{
		"id": r.ID, "issueId": r.IssueID, "kind": r.Kind, "summary": r.Summary,
		"historical": r.Historical, "current": r.Current,
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

func loadIssueBinding(ctx context.Context, tx *sql.Tx, id string) (SessionBinding, error) {
	var binding SessionBinding
	err := readJSON(ctx, tx, "SELECT body FROM session_bindings WHERE id=? AND role='issue'", &binding, id)
	return binding, err
}

func boundIssue(ctx context.Context, tx *sql.Tx, b SessionBinding) (Issue, error) {
	var issue Issue
	if b.Role != "issue" || b.OwnerID != b.IssueID {
		return issue, problem("forbidden", "SessionBinding does not own an Issue")
	}
	err := readJSON(ctx, tx, "SELECT body FROM issues WHERE id=? AND topic_id=?", &issue, b.OwnerID, b.TopicID)
	return issue, err
}

func issueForBinding(ctx context.Context, tx *sql.Tx, b SessionBinding) (Issue, error) {
	issue, err := boundIssue(ctx, tx, b)
	if err != nil {
		return issue, err
	}
	if b.State == "revoked" || issue.CurrentSessionBindingID != b.ID || terminal(issue.Status) {
		return issue, problem("stale_binding", "Issue Session no longer owns the current Issue")
	}
	return issue, nil
}

func currentBoundIssue(ctx context.Context, tx *sql.Tx, b SessionBinding) (Issue, SessionBinding, error) {
	stored, err := loadIssueBinding(ctx, tx, b.ID)
	if err != nil {
		return Issue{}, stored, err
	}
	if !sameBinding(stored, b) {
		return Issue{}, stored, problem("stale_binding", "Issue Session scope changed")
	}
	issue, err := issueForBinding(ctx, tx, stored)
	return issue, stored, err
}

func (s *Store) queryBoundIssue(ctx context.Context, b SessionBinding, tool string, raw []byte) (any, error) {
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
	issue, _, err := currentBoundIssue(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	if tool == "get_issue" {
		return WorkReceipt{Issue: issue}, nil
	}
	if !slices.ContainsFunc(issue.Materials, func(m Material) bool { return m.MessageID == query.MessageID }) {
		return nil, problem("not_found", "Message is not a material of the bound Issue")
	}
	var message Message
	err = readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE id=?", &message, query.MessageID)
	if err != nil {
		return nil, err
	}
	view, err := message.MarshalAgentPromptJSON()
	return json.RawMessage(view), err
}

func (s *Store) submitIssueReport(ctx context.Context, b SessionBinding, raw []byte) (json.RawMessage, error) {
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
	return s.commitWork(ctx, b, "submit_report", r.OperationID, func(ctx context.Context, tx *sql.Tx, binding SessionBinding) (any, error) {
		stored, err := loadIssueBinding(ctx, tx, binding.ID)
		if err != nil {
			return nil, err
		}
		if !sameBinding(stored, binding) {
			return nil, problem("stale_binding", "Issue Session scope changed")
		}
		historical := stored.State == "revoked"
		var issue Issue
		if historical && stored.RetiredGoalRevision != 0 {
			issue, err = boundIssue(ctx, tx, stored)
		} else {
			issue, err = issueForBinding(ctx, tx, stored)
		}
		if err != nil {
			return nil, err
		}
		key := "$." + creationKey("report", stored.ID, r.OperationID)
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
		if historical {
			upper = stored.RetiredGoalRevision
		}
		if r.GoalRevision < stored.StartGoalRevision || r.GoalRevision > upper {
			return nil, problem("invalid_argument", "goalRevision is outside the SessionBinding's goal revisions")
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM reports").Scan(&seq); err != nil {
			return nil, storageError()
		}
		report := Report{
			ID: issue.ID + "-r" + strconv.Itoa(len(issue.Reports)+1), TopicID: issue.TopicID, IssueID: issue.ID, SessionBindingID: stored.ID,
			Revision: r.GoalRevision, Sequence: seq, Kind: r.Kind, Summary: r.Summary, Details: r.Details,
			Evidence: r.Evidence, Questions: r.Questions, CreatedAt: now(), EventID: newID("evt"), Historical: historical,
		}
		issue.Reports = append(issue.Reports, report)
		if err := putJSON(ctx, tx, `UPDATE issues SET body=?5,
 report_creations=json_set(report_creations,?2,json_object('id',?3,'digest',?4))
 WHERE id=?1`, issue, issue.ID, key, report.ID, digest); err != nil {
			return nil, err
		}
		if reportIsCurrent(report, issue) && stored.ReminderEventID != "" {
			if err := supersedeInternalEvent(ctx, tx, stored.ReminderEventID); err != nil {
				return nil, err
			}
		}
		commitGate("report_before_notification")
		kind := reportSubmittedKind
		if !reportIsCurrent(report, issue) {
			kind = contextSupplement
		}
		err = enqueueInternalEvent(ctx, tx, internalEvent{
			ID: report.EventID, Kind: kind,
			Sender:    eventIdentity{Kind: "issue", SessionBindingID: stored.ID},
			Recipient: eventIdentity{Kind: "topic", TopicID: issue.TopicID},
			Refs:      []eventReference{{Kind: "report", ID: report.ID, IssueID: issue.ID, GoalRevision: r.GoalRevision}},
		})
		if err == nil {
			markMCPMutation(ctx, false, true)
		}
		return report, err
	})
}
