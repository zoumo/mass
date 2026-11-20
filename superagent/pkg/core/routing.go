package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

type Topic struct {
	ID                  string            `json:"id"`
	Title               string            `json:"title"`
	Description         string            `json:"description,omitempty"`
	Goal                string            `json:"goal"`
	PullRequests        []pullRequestLink `json:"pullRequests,omitempty"`
	Workspace           string            `json:"workspace"`
	Status              string            `json:"status"`
	Revision            int               `json:"revision"`
	BindingID           string            `json:"bindingId"`
	CreatedAt           string            `json:"createdAt"`
	UpdatedAt           string            `json:"updatedAt"`
	Completion          *Completion       `json:"completion,omitempty"`
	LifecycleGeneration int               `json:"lifecycleGeneration,omitempty"`
	Cancellation        *LifecycleChange  `json:"cancellation,omitempty"`
	Reopening           *LifecycleChange  `json:"reopening,omitempty"`
	Stop                *StopRequest      `json:"stop,omitempty"`
}
type Binding struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	OwnerID    string `json:"ownerId"`
	Generation int    `json:"generation"`
	Workspace  string `json:"workspace"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	ExecutionProfile
	AgentPromptVersion   int           `json:"agentPromptVersion,omitempty"`
	RuntimeType          string        `json:"runtimeType,omitempty"`
	RuntimeUID           string        `json:"runtimeUid,omitempty"`
	RuntimeVersion       string        `json:"runtimeResourceVersion,omitempty"`
	PreviousOwnerUnknown bool          `json:"previousOwnerUnknown,omitempty"`
	State                string        `json:"state"`
	CreatedAt            string        `json:"createdAt"`
	SessionID            string        `json:"sessionId,omitempty"`
	SessionUID           string        `json:"sessionUid,omitempty"`
	LastError            string        `json:"lastError,omitempty"`
	NextAttemptAt        string        `json:"nextAttemptAt,omitempty"`
	TopicID              string        `json:"topicId,omitempty"`
	IssueID              string        `json:"issueId,omitempty"`
	PreparationStarted   bool          `json:"preparationStarted"`
	CapabilityFileID     string        `json:"capabilityFileId,omitempty"`
	MCPWorkingDirectory  string        `json:"mcpWorkingDirectory,omitempty"`
	PreviousBindingID    string        `json:"previousBindingId,omitempty"`
	Close                *BindingClose `json:"close,omitempty"`
	ReuseError           string        `json:"reuseError,omitempty"`
	Stop                 *StopRequest  `json:"stop,omitempty"`
	Recovery             *Recovery     `json:"recovery,omitempty"`
}
type NewTopic struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Goal        string `json:"goal,omitempty"`
	Workspace   string `json:"workspace"`
}
type RouteTarget struct {
	TopicID     string            `json:"topicId,omitempty"`
	Create      *NewTopic         `json:"create,omitempty"`
	MessageIDs  []string          `json:"messageIds"`
	DeliveryIDs map[string]string `json:"deliveryIds,omitempty"`
}
type RouteRequest struct {
	OperationID string         `json:"operationId"`
	MessageIDs  []string       `json:"messageIds"`
	Rounds      map[string]int `json:"rounds"`
	Kind        string         `json:"kind"`
	Targets     []RouteTarget  `json:"targets,omitempty"`
	Reason      string         `json:"reason"`
}
type RouteDecision struct {
	ID string `json:"id"`
	RouteRequest
	CreatedAt string `json:"createdAt"`
}
type ReturnRequest struct {
	DeliveryID string `json:"deliveryId"`
	Reason     string `json:"reason"`
}
type Relation struct {
	ID         string `json:"id"`
	MessageID  string `json:"messageId"`
	TopicID    string `json:"topicId"`
	Action     string `json:"action"`
	Round      int    `json:"round"`
	Reason     string `json:"reason"`
	DecisionID string `json:"decisionId,omitempty"`
	CreatedAt  string `json:"createdAt"`
	Status     string `json:"status,omitempty"`
	DeliveryID string `json:"deliveryId,omitempty"`
	Returned   bool   `json:"returned,omitempty"`
}

type Subscription struct {
	ID            string `json:"id"`
	TopicID       string `json:"topicId"`
	ThreadKey     string `json:"threadKey"`
	MessageID     string `json:"messageId,omitempty"`
	PullRequestID string `json:"pullRequestId,omitempty"`
	CreatedAt     string `json:"createdAt"`
}

func putJSON(ctx context.Context, tx *sql.Tx, query string, value any, args ...any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return storageError()
	}
	args = append(args, b)
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return storageError()
	}
	return nil
}

func readJSON(ctx context.Context, tx *sql.Tx, query string, dest any, args ...any) error {
	var b []byte
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return problem("not_found", "resource not found")
		}
		return storageError()
	}
	if json.Unmarshal(b, dest) != nil {
		return storageError()
	}
	return nil
}

func validateRoute(r RouteRequest) error {
	if strings.TrimSpace(r.OperationID) == "" || len(r.MessageIDs) == 0 || len(r.Rounds) != len(r.MessageIDs) || strings.TrimSpace(r.Reason) == "" {
		return problem("invalid_argument", "operationId, explicit messageIds, rounds and reason are required")
	}
	if r.Kind != "route" && r.Kind != "ignore" {
		return problem("invalid_argument", "kind must be route or ignore; clarification is delivered separately by Outbox")
	}
	ids := map[string]bool{}
	for _, id := range r.MessageIDs {
		if id == "" || ids[id] || r.Rounds[id] < 1 {
			return problem("invalid_argument", "messageIds must be unique and each needs a positive round")
		}
		ids[id] = true
	}
	if r.Kind == "ignore" {
		if len(r.Targets) > 0 {
			return problem("invalid_argument", "ignore cannot have targets")
		}
		return nil
	}
	covered := map[string]bool{}
	targets := map[string]bool{}
	for _, target := range r.Targets {
		if len(target.DeliveryIDs) != 0 || (target.Create == nil) == (target.TopicID == "") || len(target.MessageIDs) == 0 {
			return problem("invalid_argument", "each target needs one topicId or create and explicit messageIds")
		}
		if target.Create != nil {
			if strings.TrimSpace(target.Create.Title) == "" || !filepath.IsAbs(target.Create.Workspace) {
				return problem("invalid_argument", "new Topic needs title and an absolute workspace selected by Router")
			}
		} else {
			if targets[target.TopicID] {
				return problem("invalid_argument", "combine duplicate target Topic entries")
			}
			targets[target.TopicID] = true
		}
		seen := map[string]bool{}
		for _, id := range target.MessageIDs {
			if !ids[id] || seen[id] {
				return problem("invalid_argument", "target messageIds must be a unique subset of decision messageIds")
			}
			seen[id] = true
			covered[id] = true
		}
	}
	if len(covered) != len(ids) {
		return problem("invalid_argument", "route must cover every decision Message")
	}
	return nil
}
