package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// This Thread namespace cannot be used as a configured collector source.
const pullRequestSource = "pr:url"

const pullRequestSchema = `
CREATE TABLE pull_requests (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 topic_id TEXT NOT NULL REFERENCES topics(id),
 issue_id TEXT REFERENCES issues(id),
 execution_id TEXT REFERENCES executions(id),
 thread_key TEXT NOT NULL REFERENCES threads(key),
 created_at TEXT NOT NULL,
 body BLOB NOT NULL
);
CREATE UNIQUE INDEX pull_request_scope ON pull_requests(topic_id,COALESCE(issue_id,''),COALESCE(execution_id,''),thread_key);
CREATE INDEX pull_request_issue ON pull_requests(issue_id,created_at,id);
`

var (
	githubPullPath = regexp.MustCompile(`^/([a-zA-Z0-9-]+)/([a-zA-Z0-9_.-]+)/pull/(\d+)/?$`)
	githubComment  = regexp.MustCompile(`^(issuecomment-|discussion_r|pullrequestreview-)\d+$`)
)

func canonicalPullRequestURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(raw, " \t\r\n\\") {
		return "", problem("invalid_argument", "PR URL must be an absolute HTTP(S) resource URL without credentials")
	}
	if _, err := url.QueryUnescape(u.RawQuery); err != nil {
		return "", problem("invalid_argument", "PR URL has invalid query escaping")
	}
	// Keep unknown platforms' explicitly supplied identity byte for byte.
	if !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "github.com") {
		return raw, nil
	}
	path := githubPullPath.FindStringSubmatch(u.EscapedPath())
	defaultPort := (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80")
	if len(path) == 0 || path[2] == "." || path[2] == ".." || (u.Port() != "" && !defaultPort) {
		return "", problem("invalid_argument", "GitHub PR URL must identify github.com/owner/repo/pull/number")
	}
	number := strings.TrimLeft(path[3], "0")
	if number == "" || (u.Fragment != "" && !githubComment.MatchString(u.Fragment)) {
		return "", problem("invalid_argument", "GitHub PR URL has an invalid number or unsupported fragment")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", problem("invalid_argument", "GitHub PR URL has an invalid query")
	}
	for field := range query {
		switch field {
		case "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "utm_id", "gclid", "fbclid":
		default:
			return "", problem("invalid_argument", "remove unsupported query fields from the GitHub PR URL")
		}
	}
	return "https://github.com/" + strings.ToLower(path[1]+"/"+path[2]) + "/pull/" + number, nil
}

type RegisterPRRequest struct {
	OperationID string `json:"operationId"`
	URL         string `json:"url"`
	IssueID     string `json:"issueId,omitempty"`
}

type PullRequest struct {
	ID             string `json:"id"`
	CanonicalURL   string `json:"canonicalUrl"`
	TopicID        string `json:"topicId"`
	IssueID        string `json:"issueId,omitempty"`
	ExecutionID    string `json:"executionId,omitempty"`
	ThreadKey      string `json:"threadKey"`
	SubscriptionID string `json:"subscriptionId"`
	CreatedAt      string `json:"createdAt"`
}

func (s *Store) registerPR(ctx context.Context, b Binding, raw []byte) (json.RawMessage, error) {
	if b.Role != "topic" && b.Role != "worker" {
		return nil, problem("forbidden", "only Topic and current Worker can register a PR")
	}
	var r RegisterPRRequest
	if b.Role == "worker" {
		var args struct {
			OperationID string `json:"operationId"`
			URL         string `json:"url"`
		}
		if err := decodeStrict(raw, &args); err != nil {
			return nil, err
		}
		r.OperationID, r.URL = args.OperationID, args.URL
	} else if err := decodeStrict(raw, &r); err != nil {
		return nil, err
	}
	canonical, err := canonicalPullRequestURL(r.URL)
	if err != nil {
		return nil, err
	}
	r.URL = canonical
	ref := ThreadRef{Source: pullRequestSource, ID: canonical}
	return s.commitWork(ctx, b, "pr_register", r.OperationID, r, func(ctx context.Context, tx *sql.Tx, binding Binding) (any, error) {
		pr := PullRequest{CanonicalURL: canonical, ThreadKey: ThreadKey(ref), TopicID: binding.OwnerID, IssueID: r.IssueID}
		if binding.Role == "worker" {
			// commitWork checks current ownership before replaying or recording a receipt.
			pr.TopicID, pr.IssueID, pr.ExecutionID = binding.TopicID, binding.IssueID, binding.OwnerID
		} else if r.IssueID != "" {
			if _, err := topicIssue(ctx, tx, binding, r.IssueID); err != nil {
				return nil, err
			}
		}
		var saved []byte
		err := tx.QueryRowContext(ctx, `SELECT body FROM pull_requests WHERE topic_id=?
 AND COALESCE(issue_id,'')=? AND COALESCE(execution_id,'')=? AND thread_key=?`,
			pr.TopicID, pr.IssueID, pr.ExecutionID, pr.ThreadKey).Scan(&saved)
		switch {
		case err == nil:
			return json.RawMessage(saved), nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, storageError()
		}
		pr.ID, pr.CreatedAt = newID("pr"), now()
		if err := upsertThread(ctx, tx, pr.ThreadKey, ref, pr.CreatedAt, ""); err != nil {
			return nil, err
		}
		sub := Subscription{TopicID: pr.TopicID, ThreadKey: pr.ThreadKey, CreatedAt: pr.CreatedAt, PullRequestID: pr.ID}
		pr.SubscriptionID, err = ensureSubscription(ctx, tx, sub)
		if err != nil {
			return nil, err
		}
		err = putJSON(ctx, tx, `INSERT INTO pull_requests(id,topic_id,issue_id,execution_id,thread_key,created_at,body)
 VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,?,?)`, pr, pr.ID, pr.TopicID, pr.IssueID, pr.ExecutionID, pr.ThreadKey, pr.CreatedAt)
		return pr, err
	})
}
