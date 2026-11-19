package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

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
	ID                        string            `json:"id"`
	TopicID                   string            `json:"topicId"`
	Title                     string            `json:"title"`
	Requirements              string            `json:"goal"`
	RequirementsRevision      int               `json:"goalRevision"`
	LatestRequirementsEventID string            `json:"latestRequirementsEventId,omitempty"`
	Blockers                  []Blocker         `json:"blockers,omitempty"`
	ResolvedBlockers          []Blocker         `json:"resolvedBlockers,omitempty"`
	Reports                   []Report          `json:"reports,omitempty"`
	PullRequests              []pullRequestLink `json:"pullRequests,omitempty"`
	Workspace                 string            `json:"workspace"`
	ExecutionProfile          ExecutionProfile  `json:"executionProfile"`
	Materials                 []Material        `json:"materials,omitempty"`
	Status                    string            `json:"status"`
	Revision                  int               `json:"revision"`
	CurrentExecutionID        string            `json:"currentExecutionId,omitempty"`
	CreatedAt                 string            `json:"createdAt"`
	UpdatedAt                 string            `json:"updatedAt"`
	Completion                *Completion       `json:"completion,omitempty"`
	Cancellation              *LifecycleChange  `json:"cancellation,omitempty"`
	Reopening                 *LifecycleChange  `json:"reopening,omitempty"`
}

type Execution struct {
	ID                      string           `json:"id"`
	IssueID                 string           `json:"issueId"`
	TopicID                 string           `json:"topicId"`
	BindingID               string           `json:"bindingId"`
	Generation              int              `json:"generation"`
	CreatedAt               string           `json:"createdAt"`
	Revision                int              `json:"goalRevision"`
	StartEventID            string           `json:"startEventId"`
	Authority               string           `json:"authority"`
	RetiredGoalRevision     int              `json:"retiredGoalRevision,omitempty"`
	RetiredAt               string           `json:"retiredAt,omitempty"`
	PreparationAtRetirement string           `json:"preparationAtRetirement,omitempty"`
	Stop                    *StopRequest     `json:"stop,omitempty"`
	WorkObservation         *WorkObservation `json:"workObservation,omitempty"`
	ReminderEventID         string           `json:"reminderEventId,omitempty"`
}

type CreateIssueRequest struct {
	OperationID  string     `json:"operationId"`
	Title        string     `json:"title"`
	Requirements string     `json:"requirements,omitempty"`
	Goal         string     `json:"goal,omitempty"`
	Workspace    string     `json:"workspace"`
	RuntimeRef   string     `json:"runtimeRef"`
	Model        string     `json:"model"`
	Materials    []Material `json:"materials,omitempty"`
	Start        bool       `json:"start,omitempty"`
}

type StartIssueRequest struct {
	OperationID      string              `json:"operationId"`
	IssueID          string              `json:"issueId"`
	ExpectedRevision int                 `json:"expectedRevision"`
	Resolve          []BlockerResolution `json:"resolve,omitempty"`
	RuntimeRef       *string             `json:"runtimeRef,omitempty"`
	Model            *string             `json:"model,omitempty"`
}

// UnmarshalJSON distinguishes omitted selections from explicit null values.
func (r *StartIssueRequest) UnmarshalJSON(data []byte) error {
	type request StartIssueRequest
	wire := struct {
		*request
		RuntimeRef json.RawMessage `json:"runtimeRef"`
		Model      json.RawMessage `json:"model"`
	}{request: (*request)(r)}
	if err := decodeStrict(data, &wire); err != nil {
		return err
	}
	for _, field := range []struct {
		raw  json.RawMessage
		dest **string
	}{{wire.RuntimeRef, &r.RuntimeRef}, {wire.Model, &r.Model}} {
		if len(field.raw) == 0 {
			*field.dest = nil
			continue
		}
		if err := json.Unmarshal(field.raw, field.dest); err != nil || *field.dest == nil {
			return problem("invalid_argument", "runtimeRef and model must be strings")
		}
	}
	return nil
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
	case "issue_worker_update":
		return decodeWorkCommand(ctx, s, b, tool, raw, func(r WorkerUpdateRequest) string { return r.OperationID }, workerUpdate)
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
	return s.commitWork(ctx, b, kind, operationID(r), apply(r))
}

type workMutation func(context.Context, *sql.Tx, Binding) (any, error)

func (s *Store) commitWork(ctx context.Context, b Binding, kind, operationID string, apply workMutation) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if strings.TrimSpace(operationID) == "" {
		return nil, problem("invalid_argument", "operationId is required")
	}
	if kind != "report_submit" {
		if err := checkBinding(ctx, tx, b); err != nil {
			return nil, err
		}
	}
	result, err := apply(ctx, tx, b)
	if err != nil {
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
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM topic_message_history WHERE topic_id=? AND message_id=?)", topicID, m.MessageID).Scan(&scoped); err != nil {
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
		key := creationKey("issue", b.ID, r.OperationID)
		id := creationID(key, r)
		if existing, err := existingCreation(ctx, tx, "issues", key, id); err != nil {
			return nil, err
		} else if existing {
			issue, err := topicIssue(ctx, tx, b, id)
			if err != nil || !r.Start {
				return WorkReceipt{Issue: issue}, err
			}
			var x Execution
			err = readJSON(ctx, tx, "SELECT body FROM executions WHERE issue_id=? AND generation=1", &x, id)
			return WorkReceipt{Issue: issue, Execution: &x}, err
		}
		t, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		goal, err := issueGoal(r.Goal, r.Requirements)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.Title) == "" || !filepath.IsAbs(r.Workspace) {
			return nil, problem("invalid_argument", "title and an absolute workspace are required")
		}
		profile, err := s.config.Execution.workerProfile(r.RuntimeRef, r.Model)
		if err != nil {
			return nil, err
		}
		if err := validateMaterials(ctx, tx, t.ID, r.Materials); err != nil {
			return nil, err
		}
		at := now()
		issue := Issue{
			ID: id, TopicID: t.ID, Title: r.Title, Requirements: goal, Workspace: filepath.Clean(r.Workspace),
			ExecutionProfile: profile, Materials: r.Materials, Status: "pending", Revision: 1, RequirementsRevision: 1, CreatedAt: at, UpdatedAt: at,
		}
		if err := checkWorkspace(issue.Workspace); err != nil {
			return nil, err
		}
		if err := putJSON(ctx, tx, "INSERT INTO issues(id,topic_id,created_at,body) VALUES(?,?,?,?)", issue, issue.ID, t.ID, at); err != nil {
			return nil, err
		}
		if r.Start {
			return s.scheduleIssue(ctx, tx, issue, deliveryIdentity{Kind: "topic", TopicID: b.OwnerID})
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
		if (r.RuntimeRef == nil) != (r.Model == nil) {
			return nil, problem("invalid_argument", "provide both runtimeRef and model, or omit both")
		}
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if err := checkWorkspace(issue.Workspace); err != nil {
			return nil, err
		}
		if issue.CurrentExecutionID != "" {
			return nil, problem("conflict", "Issue already has a current Execution")
		}
		if r.RuntimeRef != nil {
			issue.ExecutionProfile, err = s.config.Execution.workerProfile(*r.RuntimeRef, *r.Model)
			if err != nil {
				return nil, err
			}
		}
		if err := resolveBlockers(ctx, tx, &issue, r.Resolve); err != nil {
			return nil, err
		}
		return s.scheduleIssue(ctx, tx, issue, deliveryIdentity{Kind: "topic", TopicID: b.OwnerID})
	}
}

func (s *Store) scheduleIssue(ctx context.Context, tx *sql.Tx, issue Issue, sender deliveryIdentity) (WorkReceipt, error) {
	if issue.CurrentExecutionID != "" {
		return WorkReceipt{}, problem("conflict", "Issue already has a current Execution")
	}
	if len(issue.Blockers) != 0 {
		return WorkReceipt{}, problem("failed_precondition", "starting an Execution requires resolving the Issue's blockers")
	}
	previous, err := previousWorker(ctx, tx, issue.ID)
	if err != nil {
		return WorkReceipt{}, err
	}
	at := now()
	x := Execution{
		ID: newID("exec"), IssueID: issue.ID, TopicID: issue.TopicID, Generation: previous.Generation + 1,
		CreatedAt: at, StartEventID: newID("delivery"), Authority: "current",
	}
	b, err := createWorkerBinding(ctx, tx, s.config, x, issue, previous)
	if err != nil {
		return WorkReceipt{}, err
	}
	x.BindingID = b.ID
	issue.Status = "active"
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
	err = enqueueInternalDelivery(ctx, tx, internalDelivery{
		ID: x.StartEventID, Kind: workerStarted,
		Sender:    sender,
		Recipient: deliveryIdentity{Kind: "worker", ExecutionID: x.ID},
		Content:   deliveryContent{Body: payload, Refs: []deliveryReference{issueGoalRef(issue)}},
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

		return t, putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", t, t.ID)
	}
}
