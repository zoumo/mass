package core

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// This Thread namespace cannot be used as a configured collector source.
const pullRequestSource = "pr:url"

var (
	githubPullPath = regexp.MustCompile(`^/([a-zA-Z0-9-]+)/([a-zA-Z0-9_.-]+)/pull/(\d+)/?$`)
	githubComment  = regexp.MustCompile(`^(issuecomment-|discussion_r|pullrequestreview-)\d+$`)
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

// canonicalPullRequestURL is used only by the legacy ThreadRef contract.
func canonicalPullRequestURL(raw string) (string, error) {
	u, err := parsePullRequestURL(raw)
	if err != nil {
		return "", err
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

type PullRequest struct {
	ID               string `json:"id"`
	CanonicalURL     string `json:"canonicalUrl"`
	TopicID          string `json:"topicId"`
	IssueID          string `json:"issueId,omitempty"`
	SessionBindingID string `json:"sessionBindingId,omitempty"`
	ThreadKey        string `json:"threadKey"`
	SubscriptionID   string `json:"subscriptionId"`
	CreatedAt        string `json:"createdAt"`
}

type pullRequestLink struct {
	PullRequest
	Sequence int64 `json:"sequence"`
}

func registerWorkPullRequest(ctx context.Context, tx *sql.Tx, topic Topic, issue Issue, key, sessionBindingID string) error {
	matches := func(link pullRequestLink) bool { return link.CanonicalURL == key }
	topicIndex := slices.IndexFunc(topic.PullRequests, matches)
	if issue.ID == "" && topicIndex >= 0 {
		return nil
	}
	if i := slices.IndexFunc(issue.PullRequests, matches); i >= 0 {
		return nil
	}

	link := pullRequestLink{PullRequest: PullRequest{
		CanonicalURL: key, TopicID: topic.ID, ThreadKey: key, CreatedAt: now(),
	}}
	if err := upsertThread(ctx, tx, link.ThreadKey, ThreadRef{}, link.CreatedAt, ""); err != nil {
		return err
	}
	subID, err := ensureSubscription(ctx, tx, Subscription{
		TopicID: topic.ID, ThreadKey: link.ThreadKey, CreatedAt: link.CreatedAt,
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
		if err := putJSON(ctx, tx, "UPDATE topics SET body=?2 WHERE id=?1", topic, topic.ID); err != nil {
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
