package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/shurcooL/githubv4"
)

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
		nativeID := strconv.FormatInt(int64(id), 10)
		key := "review/" + nativeID
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
		} else if !known && !review.UpdatedAt.Before(scope.EnabledAt) {
			kind, at = "review_edited", review.UpdatedAt
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
		meta := map[string]any{"objectType": "review", "objectId": nativeID, "number": number, "state": review.State, "submittedAt": review.SubmittedAt}
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
