package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const workSchema = `
CREATE TABLE issues (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, topic_id TEXT NOT NULL REFERENCES topics(id),
 created_at TEXT NOT NULL, body BLOB NOT NULL
);
CREATE INDEX issues_topic ON issues(topic_id,created_at,id);
CREATE TABLE executions (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, issue_id TEXT NOT NULL REFERENCES issues(id),
 binding_id TEXT NOT NULL UNIQUE REFERENCES bindings(id), generation INTEGER NOT NULL,
 created_at TEXT NOT NULL, body BLOB NOT NULL, UNIQUE(issue_id,generation)
);
CREATE INDEX executions_stop ON executions(binding_id) WHERE json_extract(body,'$.stop.status')='requested';
CREATE INDEX batches_binding ON batches(binding_id);
CREATE TABLE reports (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, execution_id TEXT NOT NULL REFERENCES executions(id),
 issue_id TEXT NOT NULL REFERENCES issues(id), created_at TEXT NOT NULL, body BLOB NOT NULL
);
CREATE INDEX reports_issue ON reports(issue_id,created_at,id);
`

type Material struct {
	Label     string `json:"label,omitempty"`
	URI       string `json:"uri,omitempty"`
	MessageID string `json:"messageId,omitempty"`
}

type Completion struct {
	Reason      string   `json:"reason"`
	Evidence    []string `json:"evidence"`
	CompletedAt string   `json:"completedAt"`
	BindingID   string   `json:"bindingId"`
}

type Issue struct {
	ID                        string           `json:"id"`
	TopicID                   string           `json:"topicId"`
	Title                     string           `json:"title"`
	Requirements              string           `json:"requirements"`
	RequirementsRevision      int              `json:"requirementsRevision"`
	LatestRequirementsEventID string           `json:"latestRequirementsEventId,omitempty"`
	Blockers                  []Blocker        `json:"blockers,omitempty"`
	Workspace                 string           `json:"workspace"`
	Materials                 []Material       `json:"materials,omitempty"`
	Status                    string           `json:"status"`
	Revision                  int              `json:"revision"`
	CurrentExecutionID        string           `json:"currentExecutionId,omitempty"`
	CreatedAt                 string           `json:"createdAt"`
	UpdatedAt                 string           `json:"updatedAt"`
	Completion                *Completion      `json:"completion,omitempty"`
	Cancellation              *LifecycleChange `json:"cancellation,omitempty"`
	Reopening                 *LifecycleChange `json:"reopening,omitempty"`
}

type Execution struct {
	ID                      string           `json:"id"`
	IssueID                 string           `json:"issueId"`
	TopicID                 string           `json:"topicId"`
	BindingID               string           `json:"bindingId"`
	Generation              int              `json:"generation"`
	CreatedAt               string           `json:"createdAt"`
	Revision                int              `json:"requirementsRevision"`
	StartEventID            string           `json:"startEventId"`
	Authority               string           `json:"authority"`
	RetiredAt               string           `json:"retiredAt,omitempty"`
	PreparationAtRetirement string           `json:"preparationAtRetirement,omitempty"`
	Stop                    *StopRequest     `json:"stop,omitempty"`
	WorkObservation         *WorkObservation `json:"workObservation,omitempty"`
	ReminderEventID         string           `json:"reminderEventId,omitempty"`
}

type CreateIssueRequest struct {
	OperationID  string     `json:"operationId"`
	Title        string     `json:"title"`
	Requirements string     `json:"requirements"`
	Workspace    string     `json:"workspace"`
	Materials    []Material `json:"materials,omitempty"`
	Start        bool       `json:"start,omitempty"`
}

type StartIssueRequest struct {
	OperationID       string   `json:"operationId"`
	IssueID           string   `json:"issueId"`
	ExpectedRevision  int      `json:"expectedRevision"`
	ResolveBlockerIDs []string `json:"resolveBlockerIds,omitempty"`
}

type CompleteRequest struct {
	OperationID      string   `json:"operationId"`
	ExpectedRevision int      `json:"expectedRevision"`
	Reason           string   `json:"reason"`
	Evidence         []string `json:"evidence"`
}

type CompleteIssueRequest struct {
	CompleteRequest
	IssueID string `json:"issueId"`
}

type WorkReceipt struct {
	Issue               Issue      `json:"issue"`
	Execution           *Execution `json:"execution,omitempty"`
	NotificationEventID string     `json:"notificationEventId,omitempty"`
}

func (s *Store) workCommand(ctx context.Context, b Binding, tool string, raw []byte) (json.RawMessage, error) {
	if b.Role != "topic" {
		return nil, problem("forbidden", "only Topic can manage its work")
	}
	switch tool {
	case "issue_create":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r CreateIssueRequest) string { return r.OperationID }, s.createIssue)
	case "issue_start":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r StartIssueRequest) string { return r.OperationID }, s.startIssue)
	case "issue_update_requirements":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r UpdateRequirementsRequest) string { return r.OperationID }, updateRequirements)
	case "issue_update_blockers":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r UpdateBlockersRequest) string { return r.OperationID }, updateBlockers)
	case "issue_complete":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r CompleteIssueRequest) string { return r.OperationID }, completeIssue)
	case "topic_complete":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r CompleteRequest) string { return r.OperationID }, completeTopic)
	case "timer_set":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r SetTimerRequest) string { return r.OperationID }, setTimer)
	case "timer_update":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r UpdateTimerRequest) string { return r.OperationID },
			func(r UpdateTimerRequest) workMutation { return changeTimer(r.CancelTimerRequest, &r) })
	case "timer_cancel":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r CancelTimerRequest) string { return r.OperationID },
			func(r CancelTimerRequest) workMutation { return changeTimer(r, nil) })
	case "issue_cancel", "issue_reopen":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r IssueLifecycleRequest) string { return r.OperationID },
			func(r IssueLifecycleRequest) workMutation { return changeIssueLifecycle(tool, r) })
	case "topic_cancel", "topic_reopen":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r LifecycleRequest) string { return r.OperationID },
			func(r LifecycleRequest) workMutation { return changeTopicLifecycle(tool, r) })
	default:
		return nil, problem("forbidden", "tool is outside current role capability")
	}
}

func decodeWorkCommand[T any](ctx context.Context, s *Store, b Binding, kind string, raw []byte, operationID func(T) string, apply func(T) workMutation) (json.RawMessage, error) {
	var r T
	if err := decodeStrict(raw, &r); err != nil {
		return nil, err
	}
	return s.commitWork(ctx, b, kind, operationID(r), r, apply(r))
}

type workMutation func(context.Context, *sql.Tx, Binding) (any, error)

func (s *Store) commitWork(ctx context.Context, b Binding, kind, operationID string, request any, apply workMutation) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if kind != "report_submit" && kind != "topic_cancel" {
		if err := checkBinding(ctx, tx, b); err != nil {
			return nil, err
		}
	}
	saved, fp, err := roleReplay(ctx, tx, b.ID, kind, operationID, request)
	if err != nil || saved != nil {
		return saved, err
	}
	if kind == "topic_cancel" {
		if err := checkBinding(ctx, tx, b); err != nil {
			return nil, err
		}
	}
	result, err := apply(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	if err := roleReceipt(ctx, tx, b.ID, kind, operationID, fp, result); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	switch kind {
	case "report_submit":
		commitGate("report_after_commit")
	case "issue_update_requirements":
		commitGate("requirements_after_commit")
	case "issue_cancel", "issue_reopen", "topic_cancel", "topic_reopen":
		commitGate("lifecycle_after_commit")
	}
	return json.Marshal(result)
}

func currentTopic(ctx context.Context, tx *sql.Tx, b Binding) (Topic, error) {
	var t Topic
	if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &t, b.OwnerID); err != nil {
		return t, err
	}
	if t.Status != "active" {
		return t, problem("failed_precondition", "Topic must be active")
	}
	return t, nil
}

func topicIssue(ctx context.Context, tx *sql.Tx, b Binding, id string) (Issue, error) {
	var issue Issue
	if id == "" {
		return issue, problem("invalid_argument", "issueId is required")
	}
	err := readJSON(ctx, tx, "SELECT body FROM issues WHERE id=? AND topic_id=?", &issue, id, b.OwnerID)
	return issue, err
}

func checkRevision(id string, expected, current int) error {
	if expected < 1 {
		return problem("invalid_argument", "expectedRevision is required")
	}
	if expected != current {
		return &Error{Code: "conflict", Message: "revision changed; read current state", Resource: id, CurrentRevision: current}
	}
	return nil
}

func terminal(status string) bool {
	return status == "completed" || status == "canceled"
}

func validateMaterials(ctx context.Context, tx *sql.Tx, topicID string, materials []Material) error {
	for _, m := range materials {
		if (m.URI == "") == (m.MessageID == "") || (m.URI != "" && strings.TrimSpace(m.URI) == "") || (m.MessageID != "" && strings.TrimSpace(m.MessageID) == "") {
			return problem("invalid_argument", "each material requires exactly one of uri or messageId")
		}
		if m.MessageID != "" {
			var scoped bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM message_topics WHERE topic_id=? AND message_id=?)", topicID, m.MessageID).Scan(&scoped); err != nil {
				return storageError()
			}
			if !scoped {
				return problem("not_found", "material Message not found in Topic scope")
			}
		}
	}
	return nil
}

func (s *Store) createIssue(r CreateIssueRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		t, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Requirements) == "" || !filepath.IsAbs(r.Workspace) {
			return nil, problem("invalid_argument", "title, requirements and an absolute workspace are required")
		}
		if err := validateMaterials(ctx, tx, t.ID, r.Materials); err != nil {
			return nil, err
		}
		at := now()
		issue := Issue{
			ID: newID("issue"), TopicID: t.ID, Title: r.Title, Requirements: r.Requirements, Workspace: filepath.Clean(r.Workspace),
			Materials: r.Materials, Status: "pending", Revision: 1, RequirementsRevision: 1, CreatedAt: at, UpdatedAt: at,
		}
		if err := checkWorkspace(issue.Workspace); err != nil {
			return nil, err
		}
		if err := putJSON(ctx, tx, "INSERT INTO issues(id,topic_id,created_at,body) VALUES(?,?,?,?)", issue, issue.ID, t.ID, at); err != nil {
			return nil, err
		}
		if r.Start {
			return s.scheduleIssue(ctx, tx, issue)
		}
		return WorkReceipt{Issue: issue}, nil
	}
}

func checkWorkspace(path string) error {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return problem("invalid_argument", "workspace must be an existing directory")
	}
	return nil
}

func (s *Store) startIssue(r StartIssueRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if err := checkWorkspace(issue.Workspace); err != nil {
			return nil, err
		}
		if len(r.ResolveBlockerIDs) != 0 {
			issue.Blockers, err = removeBlockers(issue.Blockers, r.ResolveBlockerIDs)
			if err != nil {
				return nil, err
			}
		}
		return s.scheduleIssue(ctx, tx, issue)
	}
}

func (s *Store) scheduleIssue(ctx context.Context, tx *sql.Tx, issue Issue) (WorkReceipt, error) {
	if issue.CurrentExecutionID != "" {
		return WorkReceipt{}, problem("conflict", "Issue already has a current Execution")
	}
	previous, err := previousWorker(ctx, tx, issue.ID)
	if err != nil {
		return WorkReceipt{}, err
	}
	at := now()
	x := Execution{
		ID: newID("exec"), IssueID: issue.ID, TopicID: issue.TopicID, Generation: previous.Generation + 1,
		CreatedAt: at, StartEventID: newID("evt"), Authority: "current",
	}
	b, err := createWorkerBinding(ctx, tx, s.config, x, issue.Workspace, previous)
	if err != nil {
		return WorkReceipt{}, err
	}
	x.BindingID = b.ID
	issue.Status = "active"
	if len(issue.Blockers) != 0 {
		issue.Status = "blocked"
	}
	issue.Revision++
	issue.UpdatedAt, issue.CurrentExecutionID = at, x.ID
	x.Revision = issue.RequirementsRevision
	if err := putJSON(ctx, tx, "INSERT INTO executions(id,issue_id,binding_id,generation,created_at,body) VALUES(?,?,?,?,?,?)", x, x.ID, issue.ID, b.ID, x.Generation, at); err != nil {
		return WorkReceipt{}, err
	}
	if err := saveIssue(ctx, tx, issue); err != nil {
		return WorkReceipt{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"topicId": issue.TopicID, "issueId": issue.ID, "executionId": x.ID,
		"previousExecutionId": previous.OwnerID, "materials": issue.Materials, "completion": issue.Completion,
		"instruction": "Read issue_get_current for current requirements and materials. Check retained workspace results before repeating work. Verify and submit a Report.",
	})
	if err != nil {
		return WorkReceipt{}, err
	}
	err = EnqueueEvent(ctx, tx, Event{
		ID: x.StartEventID, Type: "issue.started", At: at, Intent: "act",
		BindingID: b.ID, TargetRole: "worker", TargetID: x.ID, TargetGeneration: x.Generation, Revision: x.Revision, Payload: payload,
	})
	return WorkReceipt{Issue: issue, Execution: &x}, err
}

func saveIssue(ctx context.Context, tx *sql.Tx, issue Issue) error {
	return putJSON(ctx, tx, "UPDATE issues SET body=?2 WHERE id=?1", issue, issue.ID)
}

func (r CompleteRequest) completion(b Binding, at string) (*Completion, error) {
	if strings.TrimSpace(r.Reason) == "" || len(r.Evidence) == 0 {
		return nil, problem("invalid_argument", "completion requires a reason and evidence references")
	}
	for _, ref := range r.Evidence {
		if strings.TrimSpace(ref) == "" {
			return nil, problem("invalid_argument", "evidence references must not be empty")
		}
	}
	return &Completion{Reason: r.Reason, Evidence: r.Evidence, BindingID: b.ID, CompletedAt: at}, nil
}

func completeIssue(r CompleteIssueRequest) workMutation {
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
		if terminal(issue.Status) {
			return nil, problem("failed_precondition", "Issue is already terminal")
		}
		at := now()
		issue.Completion, err = r.completion(b, at)
		if err != nil {
			return nil, err
		}
		var x *Execution
		if issue.CurrentExecutionID != "" {
			retired, err := retireExecution(ctx, tx, issue.CurrentExecutionID, at, false)
			if err != nil {
				return nil, err
			}
			x = &retired
		}
		previousStatus := issue.Status
		issue.Status, issue.UpdatedAt, issue.CurrentExecutionID = "completed", at, ""
		issue.Revision++
		return WorkReceipt{Issue: issue, Execution: x}, saveIssueLifecycleTransition(ctx, tx, issue, previousStatus)
	}
}

func completeTopic(r CompleteRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		t, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		if err := checkRevision(t.ID, r.ExpectedRevision, t.Revision); err != nil {
			return nil, err
		}
		at := now()
		t.Completion, err = r.completion(b, at)
		if err != nil {
			return nil, err
		}
		var unfinished bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM issues WHERE topic_id=? AND json_extract(body,'$.status') NOT IN ('completed','canceled'))", t.ID).Scan(&unfinished); err != nil {
			return nil, storageError()
		}
		if unfinished {
			return nil, problem("failed_precondition", "every Issue must be terminal before Topic completion")
		}
		t.Status, t.UpdatedAt = "completed", at
		t.Revision++
		if err := invalidateTimers(ctx, tx, t.ID, at); err != nil {
			return nil, err
		}
		if err := supersedeEvents(ctx, tx, `SELECT json_extract(body,'$.followupEventId') FROM timers WHERE topic_id=?`, t.ID); err != nil {
			return nil, err
		}
		return t, putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", t, t.ID)
	}
}
