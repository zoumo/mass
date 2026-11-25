package core

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"strings"
)

func parsePullRequestURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(raw, " \t\r\n\\") {
		return nil, problem("invalid_argument", "PR URL must be an absolute HTTP(S) resource URL without credentials")
	}
	if _, err := url.QueryUnescape(u.RawQuery); err != nil {
		return nil, problem("invalid_argument", "PR URL has invalid query escaping")
	}
	return u, nil
}

type PullRequest struct {
	ID               string `json:"id"`
	CanonicalURL     string `json:"canonicalUrl"`
	TopicID          string `json:"topicId"`
	IssueID          string `json:"issueId,omitempty"`
	SessionBindingID string `json:"sessionBindingId,omitempty"`
	ThreadID         string `json:"threadId"`
	SubscriptionID   string `json:"subscriptionId"`
	CreatedAt        string `json:"createdAt"`
}

type pullRequestLink struct {
	PullRequest
	Sequence int64 `json:"sequence"`
}

func registerWorkPullRequest(ctx context.Context, tx *sql.Tx, topic Topic, issue Issue, canonicalURL, sessionBindingID string) error {
	matches := func(link pullRequestLink) bool { return link.CanonicalURL == canonicalURL }
	topicIndex := slices.IndexFunc(topic.PullRequests, matches)
	if issue.ID == "" && topicIndex >= 0 {
		return nil
	}
	if i := slices.IndexFunc(issue.PullRequests, matches); i >= 0 {
		return nil
	}

	link := pullRequestLink{PullRequest: PullRequest{
		CanonicalURL: canonicalURL, TopicID: topic.ID, ThreadID: canonicalURL, CreatedAt: now(),
	}}
	if err := upsertThread(ctx, tx, link.ThreadID, link.CreatedAt, ""); err != nil {
		return err
	}
	subID, err := ensureSubscription(ctx, tx, Subscription{
		TopicID: topic.ID, ThreadID: link.ThreadID, CreatedAt: link.CreatedAt,
	})
	if err != nil {
		return err
	}
	link.SubscriptionID = subID
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM work_pull_requests").Scan(&link.Sequence); err != nil {
		return storageError()
	}
	if topicIndex < 0 {
		link.ID = newID("pr")
		topic.PullRequests = append(topic.PullRequests, link)
		if err := saveTopic(ctx, tx, topic); err != nil {
			return err
		}
		link.Sequence++
	}
	if issue.ID != "" {
		link.ID, link.IssueID, link.SessionBindingID = newID("pr"), issue.ID, sessionBindingID
		issue.PullRequests = append(issue.PullRequests, link)
		if err := saveIssue(ctx, tx, issue); err != nil {
			return err
		}
	}
	return nil
}
