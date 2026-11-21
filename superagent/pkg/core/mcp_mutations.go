package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const mcpMutationSchema = `
CREATE TABLE IF NOT EXISTS mcp_mutation_receipts (
 session_binding_id TEXT NOT NULL REFERENCES session_bindings(id), tool TEXT NOT NULL,
 operation_id TEXT NOT NULL, digest TEXT NOT NULL, result BLOB NOT NULL,
 PRIMARY KEY(session_binding_id, tool, operation_id)
);`

type mutationRevision struct {
	OperationID      string `json:"operationId"`
	ExpectedRevision int    `json:"expectedRevision"`
}

type addMaterials struct {
	Add []Material `json:"add"`
}
type addPullRequests struct {
	Add []string `json:"add"`
}
type changeTransition struct {
	Action   string   `json:"action"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
}

type updateTopicArgs struct {
	mutationRevision
	Title        *string           `json:"title,omitempty"`
	Description  *string           `json:"description,omitempty"`
	Goal         *string           `json:"goal,omitempty"`
	PullRequests *addPullRequests  `json:"pullRequests,omitempty"`
	Transition   *changeTransition `json:"transition,omitempty"`
}

type editIssueFields struct {
	mutationRevision
	Title        *string          `json:"title,omitempty"`
	Materials    *addMaterials    `json:"materials,omitempty"`
	PullRequests *addPullRequests `json:"pullRequests,omitempty"`
}

type changeBlockers struct {
	Add             []BlockerInput      `json:"add,omitempty"`
	Resolve         []BlockerResolution `json:"resolve,omitempty"`
	UnblockedStatus string              `json:"unblockedStatus,omitempty"`
}

type sessionInput struct {
	Mode    string             `json:"mode"`
	Content *StructuredContent `json:"content,omitempty"`
}

type updateIssueArgs struct {
	editIssueFields
	IssueID      string            `json:"issueId"`
	Goal         *string           `json:"goal,omitempty"`
	Blockers     *changeBlockers   `json:"blockers,omitempty"`
	SessionInput *sessionInput     `json:"sessionInput,omitempty"`
	Transition   *changeTransition `json:"transition,omitempty"`
}

// Save the response with the mutation in the same transaction, so a lost
// response can be replayed without applying another write or checking a stale revision.
func (s *Store) commitMCPMutation(ctx context.Context, b SessionBinding, tool, operationID string, args any, apply workMutation) (json.RawMessage, error) {
	replayed := false
	var committed bool
	result, err := s.commitWork(ctx, b, tool, operationID, func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		digest := creationDigest(args)
		var recorded string
		var raw json.RawMessage
		err := tx.QueryRowContext(ctx, `SELECT digest,result FROM mcp_mutation_receipts WHERE session_binding_id=? AND tool=? AND operation_id=?`, b.ID, tool, operationID).Scan(&recorded, &raw)
		if err == nil {
			if recorded != digest {
				return nil, problem("conflict", "operationId was already used with other parameters")
			}
			if b.Role == "topic" && (tool == "update_issue" || tool == "create_issue" || tool == "start_issue") {
				var receipt WorkReceipt
				if json.Unmarshal(raw, &receipt) == nil {
					markMCPIssueTarget(ctx, receipt.Issue.ID)
				}
			}
			replayed = true
			return raw, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, storageError()
		}
		value, err := apply(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		raw, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO mcp_mutation_receipts VALUES(?,?,?,?,?)`, b.ID, tool, operationID, digest, raw); err != nil {
			return nil, storageError()
		}
		committed = true
		return raw, nil
	})
	if err == nil {
		markMCPMutation(ctx, replayed, committed)
		if committed {
			switch r := args.(type) {
			case updateTopicArgs:
				if r.Transition != nil {
					commitGate("lifecycle_after_commit")
				}
			case updateIssueArgs:
				if r.Transition != nil {
					commitGate("lifecycle_after_commit")
				}
				if r.Goal != nil && r.SessionInput != nil {
					commitGate("requirements_after_commit")
				}
			}
		}
	}
	return result, err
}

func setMCPAction(ctx context.Context, t *changeTransition) {
	if u := mcpUsageFrom(ctx); u != nil {
		u.action = "edit"
		if t != nil {
			switch t.Action {
			case "complete", "cancel", "reopen":
				u.action = t.Action
			default:
				u.action = "_invalid"
			}
		}
	}
}

func validateTransition(t *changeTransition) error {
	if t == nil {
		return nil
	}
	if strings.TrimSpace(t.Reason) == "" {
		return problem("invalid_argument", "transition requires a reason")
	}
	switch t.Action {
	case "complete":
		if len(t.Evidence) == 0 {
			return problem("invalid_argument", "completion requires evidence references")
		}
	case "cancel", "reopen":
		if len(t.Evidence) != 0 {
			return problem("invalid_argument", "only completion accepts evidence references")
		}
	default:
		return problem("invalid_argument", "invalid transition action")
	}
	return nil
}

func canonicalMCPPRs(input *addPullRequests) ([]string, error) {
	if input == nil {
		return nil, nil
	}
	if len(input.Add) == 0 {
		return nil, problem("invalid_argument", "pullRequests.add requires at least one URL")
	}
	urls := make([]string, 0, len(input.Add))
	for _, raw := range input.Add {
		canonical, err := canonicalPullRequestURL(raw)
		if err != nil {
			return nil, err
		}
		urls = append(urls, canonical)
	}
	return urls, nil
}

func (s *Store) updateMCPTopic(ctx context.Context, b SessionBinding, raw []byte) (any, error) {
	var r updateTopicArgs
	if err := decodeStrict(raw, &r); err != nil {
		return nil, err
	}
	setMCPAction(ctx, r.Transition)
	if err := validateTransition(r.Transition); err != nil {
		return nil, err
	}
	urls, err := canonicalMCPPRs(r.PullRequests)
	if err != nil {
		return nil, err
	}
	if r.Title == nil && r.Description == nil && r.Goal == nil && len(urls) == 0 && r.Transition == nil {
		return nil, problem("invalid_argument", "provide at least one Topic update")
	}
	if r.PullRequests != nil {
		r.PullRequests.Add = urls
	}
	return s.commitMCPMutation(ctx, b, "update_topic", r.OperationID, r, func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		var topic Topic
		if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, b.OwnerID); err != nil {
			return nil, err
		}
		if err := checkRevision(topic.ID, r.ExpectedRevision, topic.Revision); err != nil {
			return nil, err
		}
		if terminal(topic.Status) && (r.Transition == nil || r.Transition.Action != "reopen") {
			return nil, problem("failed_precondition", "reopen a terminal Topic before editing")
		}
		before := creationDigest(topic)
		if r.Title != nil {
			if strings.TrimSpace(*r.Title) == "" {
				return nil, problem("invalid_argument", "title must not be blank")
			}
			topic.Title = *r.Title
		}
		if r.Description != nil {
			topic.Description = *r.Description
		}
		if r.Goal != nil {
			if err := validateGoal(*r.Goal); err != nil {
				return nil, err
			}
			topic.Goal = *r.Goal
		}
		for _, canonical := range urls {
			if err := registerWorkPullRequest(ctx, tx, topic, Issue{}, canonical, ""); err != nil {
				return nil, err
			}
			var persisted Topic
			if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &persisted, topic.ID); err != nil {
				return nil, err
			}
			topic.PullRequests = persisted.PullRequests
		}
		if r.Transition != nil {
			if err := putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", topic, topic.ID); err != nil {
				return nil, err
			}
			t := r.Transition
			if t.Action == "complete" {
				return completeTopic(CompleteRequest{OperationID: r.OperationID, ExpectedRevision: r.ExpectedRevision, Reason: t.Reason, Evidence: t.Evidence})(ctx, tx, b)
			}
			return changeTopicLifecycle("topic_"+t.Action, LifecycleRequest{OperationID: r.OperationID, ExpectedRevision: r.ExpectedRevision, Reason: t.Reason})(ctx, tx, b)
		}
		if before == creationDigest(topic) {
			if u := mcpUsageFrom(ctx); u != nil {
				u.noChange = true
			}
			return topic, nil
		}
		topic.Revision++
		topic.UpdatedAt = now()
		return topic, putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", topic, topic.ID)
	})
}

func (s *Store) updateMCPIssue(ctx context.Context, b SessionBinding, raw []byte) (any, error) {
	var r updateIssueArgs
	if b.Role == "issue" {
		if err := decodeStrict(raw, &r.editIssueFields); err != nil {
			return nil, err
		}
	} else if err := decodeStrict(raw, &r); err != nil {
		return nil, err
	}
	setMCPAction(ctx, r.Transition)
	if err := validateTransition(r.Transition); err != nil {
		return nil, err
	}
	if r.Transition != nil && r.SessionInput != nil {
		return nil, problem("invalid_argument", "a Session input cannot accompany a lifecycle transition")
	}
	urls, err := canonicalMCPPRs(r.PullRequests)
	if err != nil {
		return nil, err
	}
	if r.Materials != nil && len(r.Materials.Add) == 0 {
		return nil, problem("invalid_argument", "materials.add requires at least one material")
	}
	if r.Title == nil && r.Goal == nil && r.Materials == nil && len(urls) == 0 && r.Blockers == nil && r.SessionInput == nil && r.Transition == nil {
		return nil, problem("invalid_argument", "provide at least one Issue update")
	}
	if r.PullRequests != nil {
		r.PullRequests.Add = urls
	}
	return s.commitMCPMutation(ctx, b, "update_issue", r.OperationID, r, func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		var issue Issue
		var topic Topic
		var sessionBindingID string
		if b.Role == "issue" {
			var binding SessionBinding
			issue, binding, err = currentBoundIssue(ctx, tx, b)
			sessionBindingID = binding.ID
			if err == nil {
				err = readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, issue.TopicID)
			}
		} else {
			issue, err = topicIssue(ctx, tx, b, r.IssueID)
			if err == nil {
				markMCPIssueTarget(ctx, issue.ID)
				topic, err = currentTopic(ctx, tx, b)
			}
		}
		if err != nil {
			return nil, err
		}
		if err := checkRevision(issue.ID, r.ExpectedRevision, issue.Revision); err != nil {
			return nil, err
		}
		if terminal(issue.Status) && (r.Transition == nil || r.Transition.Action != "reopen") {
			return nil, problem("failed_precondition", "reopen a terminal Issue before editing")
		}
		before := creationDigest(issue)
		if r.Title != nil {
			if strings.TrimSpace(*r.Title) == "" {
				return nil, problem("invalid_argument", "title must not be blank")
			}
			issue.Title = *r.Title
		}
		if r.Goal != nil {
			if err := validateGoal(*r.Goal); err != nil {
				return nil, err
			}
			if *r.Goal != issue.Requirements {
				issue.Requirements = *r.Goal
				issue.RequirementsRevision++
			}
		}
		var materials []Material
		if r.Materials != nil {
			materials = r.Materials.Add
			if err := validateMaterials(ctx, tx, issue.TopicID, materials); err != nil {
				return nil, err
			}
			appendMaterials(&issue, materials)
		}
		for _, canonical := range urls {
			if err := registerWorkPullRequest(ctx, tx, topic, issue, canonical, sessionBindingID); err != nil {
				return nil, err
			}
			if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, topic.ID); err != nil {
				return nil, err
			}
			var persisted Issue
			if err := readJSON(ctx, tx, "SELECT body FROM issues WHERE id=?", &persisted, issue.ID); err != nil {
				return nil, err
			}
			issue.PullRequests = persisted.PullRequests
		}
		var retired *SessionBinding
		if r.Blockers != nil {
			var err error
			retired, err = applyBlockerChanges(ctx, tx, b, &issue, *r.Blockers)
			if err != nil {
				return nil, err
			}
			for _, resolved := range r.Blockers.Resolve {
				if resolved.MessageID != "" {
					materials = append(materials, Material{MessageID: resolved.MessageID})
				}
			}
		}
		if r.Transition != nil {
			if err := saveIssue(ctx, tx, issue); err != nil {
				return nil, err
			}
			t := r.Transition
			if t.Action == "complete" {
				return completeIssue(CompleteIssueRequest{CompleteRequest: CompleteRequest{OperationID: r.OperationID, ExpectedRevision: r.ExpectedRevision, Reason: t.Reason, Evidence: t.Evidence}, IssueID: issue.ID})(ctx, tx, b)
			}
			return changeIssueLifecycle("issue_"+t.Action, IssueLifecycleRequest{LifecycleRequest: LifecycleRequest{OperationID: r.OperationID, ExpectedRevision: r.ExpectedRevision, Reason: t.Reason}, IssueID: issue.ID})(ctx, tx, b)
		}
		if before == creationDigest(issue) && r.SessionInput == nil {
			if u := mcpUsageFrom(ctx); u != nil {
				u.noChange = true
			}
			return WorkReceipt{Issue: issue}, nil
		}
		issue.Revision++
		issue.UpdatedAt = now()
		if r.SessionInput == nil {
			return WorkReceipt{Issue: issue, SessionBinding: retired}, saveIssue(ctx, tx, issue)
		}
		input := r.SessionInput
		if input.Content != nil {
			if err := input.Content.normalize(); err != nil {
				return nil, err
			}
		}
		kind := issueSessionUpdateKind
		switch input.Mode {
		case "continue":
		case contextSupplement:
			kind = contextSupplement
			if input.Content == nil && len(materials) == 0 && r.Goal == nil {
				return nil, problem("invalid_argument", "context supplement requires content, materials or a goal update")
			}
		default:
			return nil, problem("invalid_argument", "sessionInput.mode must be continue or context_supplement")
		}
		prefix := "evt"
		if r.Goal != nil && kind == issueSessionUpdateKind {
			prefix = "goal-update"
		}
		id := newID(prefix)
		if r.Goal != nil {
			issue.LatestRequirementsEventID = id
		}
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		if r.Goal != nil {
			commitGate("requirements_before_notification")
		}
		return notifyIssueSession(ctx, tx, issue, id, kind, input.Content, materials)
	})
}
