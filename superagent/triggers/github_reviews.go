package triggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
)

const githubReviewsPrefix = "reviews/"

type githubReview struct {
	ID          githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Body        string
	URL         string
	Author      *githubAuthor
	State       string
	SubmittedAt time.Time
	UpdatedAt   time.Time
	Commit      *githubCommitOID
}

type githubReviewRecord struct {
	State   string          `json:"state"`
	BodySHA json.RawMessage `json:"bodySHA"`
}

func reconcileGitHubSchedules(tx *sql.Tx, repo, parent string, stream githubStreamState, item githubObservation) error {
	priority := !stream.LastSuccessAt.IsZero() || !item.UpdatedAt.Before(stream.EnabledAt.Add(-githubOverlap))
	switch parent {
	case "issues":
		var issue githubIssueRecord
		if json.Unmarshal(item.Data, &issue) != nil || issue.Number <= 0 {
			return errors.New("invalid GitHub issue discussion subject")
		}
		scope := githubIssueCommentsPrefix + strconv.Itoa(issue.Number)
		return reconcileGitHubDiscussion(tx, repo, scope, stream.EnabledAt, issue.State != "open", priority, item)
	case githubPullsScope:
		var pull githubPullRecord
		if json.Unmarshal(item.Data, &pull) != nil || pull.Number <= 0 {
			return errors.New("invalid GitHub pull discussion subject")
		}
		for _, prefix := range []string{githubPullCommentsPrefix, githubInlineCommentsPrefix, githubReviewsPrefix} {
			if err := reconcileGitHubDiscussion(tx, repo, prefix+item.Key, stream.EnabledAt, pull.State != "open", priority, item); err != nil {
				return err
			}
		}
		if err := scheduleGitHubCI(tx, repo, item.Key, stream.EnabledAt, !item.Excluded && pull.State == "open"); err != nil {
			return err
		}
		if item.Rebaseline {
			return resetGitHubBaseline(tx, repo, githubCIPrefix+item.Key)
		}
	}
	return nil
}

func reconcileGitHubDiscussion(tx *sql.Tx, repo, scope string, enabled time.Time, closed, priority bool, item githubObservation) error {
	if item.Excluded {
		_, err := tx.ExecContext(context.Background(), `UPDATE github_streams SET active = 0 WHERE repository = ? AND scope = ? AND active = 1`, repo, scope)
		return err
	}
	if err := scheduleGitHubDiscussion(tx, repo, scope, enabled, closed, priority); err != nil {
		return err
	}
	if item.Rebaseline {
		return resetGitHubBaseline(tx, repo, scope)
	}
	return nil
}

func resetGitHubBaseline(tx *sql.Tx, repo, scope string) error {
	_, err := tx.ExecContext(context.Background(), `UPDATE github_streams SET silent_baseline = 1, next_attempt_at = 0,
		last_error = '', failures = 0 WHERE repository = ? AND scope = ?`, repo, scope)
	return err
}

func scheduleGitHubDiscussion(tx *sql.Tx, repo, scope string, enabled time.Time, closed, priority bool) error {
	_, err := tx.ExecContext(context.Background(), `INSERT INTO github_streams (repository, scope, kind, active, enabled_at, discussion_closed, discussion_priority)
		VALUES (?, ?, 'discussion', 1, ?, ?, ?)
		ON CONFLICT (repository, scope) DO UPDATE SET
			active = 1,
			discussion_closed = excluded.discussion_closed,
			discussion_priority = discussion_priority OR excluded.discussion_priority,
			next_attempt_at = CASE WHEN excluded.discussion_priority AND last_error = '' THEN 0 ELSE next_attempt_at END`,
		repo, scope, githubTimeValue(enabled), closed, priority)
	return err
}

func githubDiscussionScope(name string) (prefix string, isDiscussion bool) {
	for _, prefix := range []string{githubIssueCommentsPrefix, githubPullCommentsPrefix, githubInlineCommentsPrefix, githubReviewsPrefix} {
		if strings.HasPrefix(name, prefix) {
			return prefix, true
		}
	}
	return "", false
}

func (t *githubTrigger) pollNextGitHubDiscussion(ctx context.Context, repo githubRepository) error {
	now := time.Now()
	if ctx.Err() != nil || now.Before(t.api.pauseUntil) {
		return nil //nolint:nilerr // Cancellation ends polling normally.
	}
	// Historical baselines and periodic reconciliations keep advancing while
	// changes observed on active parents can refresh their discussion promptly.
	for _, priority := range []bool{true, false} {
		next, err := t.store.nextScope(repo.Repository, "discussion", priority, now, t.matchKey)
		if err != nil {
			return errors.New("cannot read GitHub discussion schedule")
		}
		if next != "" && ctx.Err() == nil && !time.Now().Before(t.api.pauseUntil) {
			if err := t.pollGitHubDiscussionScope(ctx, repo, next); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *githubTrigger) pollGitHubDiscussionScope(ctx context.Context, repo githubRepository, next string) error {
	prefix, _ := githubDiscussionScope(next)
	number, err := strconv.Atoi(strings.TrimPrefix(next, prefix))
	if err != nil || number <= 0 {
		return errors.New("invalid saved GitHub discussion scope")
	}
	scope := githubScope{name: next, poll: func(ctx context.Context, api *githubAPI, repo string, stream *githubStreamState, _ time.Time) ([]githubObservation, error) {
		if prefix == githubReviewsPrefix {
			return pollGitHubReviews(ctx, api, repo, number, stream)
		}
		if prefix == githubInlineCommentsPrefix {
			comments, err := readGitHubInlineComments(ctx, api, repo, number)
			if err != nil {
				return nil, err
			}
			return githubCommentObservations(repo, number, stream, "review_comment", comments)
		}
		comments, err := readGitHubMainComments(ctx, api, repo, number, prefix == githubIssueCommentsPrefix)
		if err != nil {
			return nil, err
		}
		return githubCommentObservations(repo, number, stream, "issue_comment", comments)
	}}
	return t.poll(ctx, repo, scope)
}

func pollGitHubReviews(ctx context.Context, api *githubAPI, repo string, number int, scope *githubStreamState) ([]githubObservation, error) {
	variables := githubRepositoryVariables(repo)
	variables["number"] = githubv4.Int(number)
	reviews, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubReview], error) {
		var q struct {
			Repository struct {
				PullRequest struct {
					Reviews githubConnection[githubReview] `graphql:"reviews(first:100,after:$cursor)"`
				} `graphql:"pullRequest(number:$number)"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		variables["cursor"] = cursor
		err := api.query(ctx, &q, variables)
		return q.Repository.PullRequest.Reviews, err
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[githubDatabaseID]githubReview, len(reviews))
	for _, review := range reviews {
		if review.State == "PENDING" {
			continue
		}
		if review.ID <= 0 || review.SubmittedAt.IsZero() || review.State == "" {
			return nil, errors.New("github_invalid_review")
		}
		byID[review.ID] = review
	}
	var observations []githubObservation
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		review := byID[id]
		key := strconv.FormatInt(int64(id), 10)
		previous, known := scope.Items[key]
		current := githubReviewRecord{review.State, githubBodyDigest(review.Body)}
		data, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		if known && string(data) == string(previous.Data) {
			continue
		}
		suppliedNumber, thread, err := githubDiscussionThread(review.URL, repo, "review", int64(id))
		if err != nil || suppliedNumber != number {
			return nil, errors.New("github_invalid_review_thread")
		}
		kind := ""
		at := review.SubmittedAt
		if !known && !review.SubmittedAt.Before(scope.EnabledAt) {
			kind = "review_submitted"
			if review.State == "DISMISSED" {
				kind = "review_dismissed"
			}
		} else if known {
			var old githubReviewRecord
			if json.Unmarshal(previous.Data, &old) != nil {
				return nil, errors.New("invalid saved GitHub review record")
			}
			kind = "review_edited"
			if review.State == "DISMISSED" && old.State != review.State {
				kind = "review_dismissed"
			}
			// Review responses include submission time, but no edit/dismissal time.
			at = review.UpdatedAt
			if !at.After(previous.UpdatedAt) {
				at = time.Now().UTC()
			}
		}
		meta := map[string]any{"objectType": "review", "objectId": key, "number": number, "state": review.State, "submittedAt": review.SubmittedAt}
		if review.Commit != nil {
			meta["commitSha"] = review.Commit.OID
		}
		actor, human := githubDiscussionAuthor(review.Author, meta)
		observations = append(observations, githubObservation{
			Key: key, Data: data, Kind: kind, UpdatedAt: at, At: at, Actor: actor, Human: human, URL: review.URL, Thread: thread,
			Title: fmt.Sprintf("GitHub review %s#%d %s (%s)\n%s", repo, number, kind, review.State, review.URL), Body: review.Body, Meta: meta,
		})
	}
	githubSortObservations(observations)
	return observations, nil
}
