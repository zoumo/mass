package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type BlockerInput struct {
	Kind              string `json:"kind"`
	Reason            string `json:"reason"`
	DependencyIssueID string `json:"dependencyIssueId,omitempty"`
}

type Blocker struct {
	ID string `json:"id"`
	BlockerInput
}

type UpdateBlockersRequest struct {
	OperationID       string         `json:"operationId"`
	IssueID           string         `json:"issueId"`
	ExpectedRevision  int            `json:"expectedRevision"`
	Add               []BlockerInput `json:"add,omitempty"`
	ResolveBlockerIDs []string       `json:"resolveBlockerIds,omitempty"`
	UnblockedStatus   string         `json:"unblockedStatus,omitempty"`
}

func updateBlockers(r UpdateBlockersRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if len(r.Add) == 0 && len(r.ResolveBlockerIDs) == 0 {
			return nil, problem("invalid_argument", "supply blockers to add or resolve")
		}
		issue.Blockers, err = removeBlockers(issue.Blockers, r.ResolveBlockerIDs)
		if err != nil {
			return nil, err
		}
		for _, input := range r.Add {
			if err := validateBlocker(ctx, tx, b, issue.ID, input); err != nil {
				return nil, err
			}
			issue.Blockers = append(issue.Blockers, Blocker{ID: newID("blocker"), BlockerInput: input})
		}
		if len(issue.Blockers) == 0 {
			if r.UnblockedStatus != "pending" && r.UnblockedStatus != "active" {
				return nil, problem("invalid_argument", "resolving the last blocker requires unblockedStatus pending or active")
			}
			issue.Status = r.UnblockedStatus
		} else {
			if r.UnblockedStatus != "" {
				return nil, problem("invalid_argument", "unblockedStatus is only available when resolving the last blocker")
			}
			issue.Status = "blocked"
		}
		issue.Revision++
		issue.UpdatedAt = now()
		return WorkReceipt{Issue: issue}, saveIssue(ctx, tx, issue)
	}
}

func removeBlockers(blockers []Blocker, ids []string) ([]Blocker, error) {
	if len(ids) == 0 {
		return blockers, nil
	}
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || remove[id] {
			return nil, problem("invalid_argument", "resolveBlockerIds must contain distinct nonempty IDs")
		}
		remove[id] = true
	}
	var remaining []Blocker
	for _, blocker := range blockers {
		if remove[blocker.ID] {
			delete(remove, blocker.ID)
		} else {
			remaining = append(remaining, blocker)
		}
	}
	if len(remove) != 0 {
		return nil, problem("not_found", "blocker not found on this Issue")
	}
	return remaining, nil
}

func validateBlocker(ctx context.Context, tx *sql.Tx, b Binding, issueID string, input BlockerInput) error {
	if strings.TrimSpace(input.Reason) == "" {
		return problem("invalid_argument", "blocker reason must not be blank")
	}
	switch input.Kind {
	case "human":
		if input.DependencyIssueID != "" {
			return problem("invalid_argument", "a human blocker cannot have a dependencyIssueId")
		}
		return nil
	case "dependency":
		if input.DependencyIssueID == issueID {
			return problem("invalid_argument", "Issue cannot depend on itself")
		}
	default:
		return problem("invalid_argument", "blocker kind must be human or dependency")
	}
	if _, err := topicIssue(ctx, tx, b, input.DependencyIssueID); err != nil {
		return err
	}
	// The mutation changes only edges leaving issueID. A path back to issueID
	// from any added target closes a cycle independently of those outgoing edges.
	var cycle bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(id) AS (
 SELECT ?1
 UNION
 SELECT json_extract(blocker.value,'$.dependencyIssueId')
 FROM reachable JOIN issues ON issues.id=reachable.id, json_each(issues.body,'$.blockers') blocker
 WHERE issues.topic_id=?2 AND reachable.id<>?3 AND json_extract(blocker.value,'$.kind')='dependency'
) SELECT EXISTS (SELECT 1 FROM reachable WHERE id=?3)`, input.DependencyIssueID, b.OwnerID, issueID).Scan(&cycle)
	if err != nil {
		return storageError()
	}
	if cycle {
		return problem("conflict", "Issue dependency would create a cycle")
	}
	return nil
}

// saveIssueLifecycleTransition stores a completion, cancellation or reopening
// and the corresponding dependency fact in the caller's work transaction.
// It never changes dependent Issues or grants execution authority.
func saveIssueLifecycleTransition(ctx context.Context, tx *sql.Tx, issue Issue, previousStatus string) error {
	if err := saveIssue(ctx, tx, issue); err != nil {
		return err
	}
	if previousStatus == issue.Status || (!terminal(issue.Status) && !terminal(previousStatus)) {
		return nil
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT json_group_array(id) FROM (
 SELECT id FROM issues WHERE topic_id=? AND EXISTS (
  SELECT 1 FROM json_each(issues.body,'$.blockers') blocker
  WHERE json_extract(blocker.value,'$.kind')='dependency' AND json_extract(blocker.value,'$.dependencyIssueId')=?
 ) ORDER BY seq
)`, issue.TopicID, issue.ID).Scan(&raw); err != nil {
		return storageError()
	}
	if raw == "[]" {
		return nil
	}
	payload, err := json.Marshal(struct {
		TopicID           string          `json:"topicId"`
		DependencyIssueID string          `json:"dependencyIssueId"`
		PreviousStatus    string          `json:"previousStatus"`
		Status            string          `json:"status"`
		DependentIssueIDs json.RawMessage `json:"dependentIssueIds"`
	}{issue.TopicID, issue.ID, previousStatus, issue.Status, json.RawMessage(raw)})
	if err != nil {
		return err
	}
	return EnqueueEvent(ctx, tx, Event{
		ID: newID("evt"), Type: "issue.dependency_changed", At: issue.UpdatedAt, Intent: "context",
		TargetID: issue.TopicID, TargetRole: "topic", Revision: issue.Revision, Payload: payload,
	})
}
