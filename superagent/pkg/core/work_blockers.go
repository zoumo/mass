package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
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
	Resolution *BlockerResolutionRecord `json:"resolution,omitempty"`
}

type BlockerResolution struct {
	BlockerID string `json:"blockerId"`
	Reason    string `json:"reason"`
	MessageID string `json:"messageId,omitempty"`
}

type BlockerResolutionRecord struct {
	BlockerResolution
	At string `json:"at"`
}
type UpdateBlockersRequest struct {
	OperationID      string              `json:"operationId"`
	IssueID          string              `json:"issueId"`
	ExpectedRevision int                 `json:"expectedRevision"`
	Add              []BlockerInput      `json:"add,omitempty"`
	Resolve          []BlockerResolution `json:"resolve,omitempty"`
	NotifyWorker     bool                `json:"notifyWorker,omitempty"`
	UnblockedStatus  string              `json:"unblockedStatus,omitempty"`
}

func updateBlockers(r UpdateBlockersRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		issue, err := editableIssue(ctx, tx, b, r.IssueID, r.ExpectedRevision)
		if err != nil {
			return nil, err
		}
		if len(r.Add) == 0 && len(r.Resolve) == 0 {
			return nil, problem("invalid_argument", "supply blockers to add or resolve")
		}
		if err := resolveBlockers(ctx, tx, &issue, r.Resolve); err != nil {
			return nil, err
		}
		for _, input := range r.Add {
			if err := validateBlocker(ctx, tx, b, issue.ID, input); err != nil {
				return nil, err
			}
			// Resolved blockers retain their numbers and count toward the next one.
			number := len(issue.Blockers) + len(issue.ResolvedBlockers) + 1
			id := issue.ID + "-b" + strconv.Itoa(number)
			issue.Blockers = append(issue.Blockers, Blocker{ID: id, BlockerInput: input})
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
		if r.UnblockedStatus == "active" && issue.CurrentExecutionID == "" {
			return nil, problem("failed_precondition", "active requires a current Execution")
		}
		var retired *Execution
		if r.UnblockedStatus == "pending" && issue.CurrentExecutionID != "" {
			x, err := retireExecution(ctx, tx, issue.CurrentExecutionID, issue.UpdatedAt, false)
			if err != nil {
				return nil, err
			}
			retired, issue.CurrentExecutionID = &x, ""
		}
		if err := saveIssue(ctx, tx, issue); err != nil {
			return nil, err
		}
		if r.NotifyWorker {
			var materials []Material
			for _, item := range r.Resolve {
				if item.MessageID != "" {
					materials = append(materials, Material{MessageID: item.MessageID})
				}
			}
			return notifyWorker(ctx, tx, issue, newID("delivery"), workerUpdateKind, nil, materials)
		}
		return WorkReceipt{Issue: issue, Execution: retired}, nil
	}
}

// resolveBlockers only resolves blockers currently open on this Issue. A
// caller may quote an associated Message as a resolution basis and material.
func resolveBlockers(ctx context.Context, tx *sql.Tx, issue *Issue, resolutions []BlockerResolution) error {
	if len(resolutions) == 0 {
		return nil
	}
	matches := make(map[string]BlockerResolution, len(resolutions))
	for _, item := range resolutions {
		if _, duplicate := matches[item.BlockerID]; duplicate || item.BlockerID == "" || strings.TrimSpace(item.Reason) == "" {
			return problem("invalid_argument", "resolve requires distinct blocker IDs and a reason for each")
		}
		matches[item.BlockerID] = item
	}
	var remaining []Blocker
	for _, blocker := range issue.Blockers {
		item, matched := matches[blocker.ID]
		if !matched {
			remaining = append(remaining, blocker)
			continue
		}
		if item.MessageID != "" {
			materials := []Material{{MessageID: item.MessageID}}
			if err := validateMaterials(ctx, tx, issue.TopicID, materials); err != nil {
				return err
			}
			if err := checkQuotedBlocker(ctx, tx, item.MessageID, issue.ID, item.BlockerID); err != nil {
				return err
			}
			appendMaterials(issue, materials)
		}
		blocker.Resolution = &BlockerResolutionRecord{BlockerResolution: item, At: now()}
		issue.ResolvedBlockers = append(issue.ResolvedBlockers, blocker)
		delete(matches, blocker.ID)
	}
	if len(matches) != 0 {
		return problem("not_found", "open blocker not found on this Issue")
	}
	issue.Blockers = remaining
	return nil
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
		Type              string          `json:"type"`
		TopicID           string          `json:"topicId"`
		DependencyIssueID string          `json:"dependencyIssueId"`
		PreviousStatus    string          `json:"previousStatus"`
		Status            string          `json:"status"`
		DependentIssueIDs json.RawMessage `json:"dependentIssueIds"`
	}{"issue.dependency_changed", issue.TopicID, issue.ID, previousStatus, issue.Status, json.RawMessage(raw)})
	if err != nil {
		return err
	}
	if err := enqueueInternalDelivery(ctx, tx, internalDelivery{
		ID: newID("delivery"), Kind: contextSupplement,
		Sender: deliveryIdentity{Kind: "core"}, Recipient: deliveryIdentity{Kind: "topic", TopicID: issue.TopicID},
		Body: payload, Refs: []deliveryReference{{Kind: "issue", ID: issue.ID}},
	}); err != nil {
		return err
	}
	commitGate("dependency_before_commit")
	return nil
}
