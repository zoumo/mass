package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
)

type githubIssue struct {
	ID        githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Number    int
	Title     string
	Body      string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	githubMatchFields
}

type githubIssueRecord struct {
	Number     int      `json:"number"`
	State      string   `json:"state"`
	ContentSHA [32]byte `json:"contentSHA"`
	Excluded   bool     `json:"excluded,omitempty"`
}

func pollGitHubIssues(ctx context.Context, api *githubAPI, repo string, scope *githubStreamState, since time.Time) ([]githubObservation, error) {
	variables := githubRepositoryVariables(repo)
	scope.Match.variables(variables)
	var sinceDate *githubv4.DateTime
	if !since.IsZero() {
		sinceDate = &githubv4.DateTime{Time: since}
	}
	variables["since"] = sinceDate
	issues, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubIssue], error) {
		var q struct {
			Repository struct {
				Issues githubConnection[githubIssue] `graphql:"issues(first:100,after:$cursor,filterBy:{since:$since},orderBy:{field:UPDATED_AT,direction:ASC})"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		variables["cursor"] = cursor
		err := api.query(ctx, &q, variables)
		return q.Repository.Issues, err
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[githubDatabaseID]githubIssue)
	for _, item := range issues {
		item.State = strings.ToLower(item.State)
		if item.ID <= 0 || item.Number <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) || (item.State != "open" && item.State != "closed") {
			return nil, errors.New("github_invalid_issue")
		}
		if old, ok := byID[item.ID]; !ok || !item.UpdatedAt.Before(old.UpdatedAt) {
			byID[item.ID] = item
		}
	}
	observations := make([]githubObservation, 0, len(byID))
	for _, item := range byID {
		key := strconv.FormatInt(int64(item.ID), 10)
		previous, known := scope.Items[key]
		if known && item.UpdatedAt.Before(previous.UpdatedAt) {
			continue
		}
		content, _ := json.Marshal([2]string{item.Title, item.Body}) //nolint:errchkjson // A fixed array of strings is always encodable.
		var old githubIssueRecord
		if known && json.Unmarshal(previous.Data, &old) != nil {
			return nil, errors.New("invalid saved GitHub issue record")
		}
		matched, err := item.matches(ctx, api, repo, true, item.Number, scope.Match)
		if err != nil {
			return nil, err
		}
		record := githubIssueRecord{item.Number, item.State, sha256.Sum256(content), !matched}
		if known && old == record && item.UpdatedAt.Equal(previous.UpdatedAt) {
			continue
		}
		kind := ""
		if matched && !old.Excluded {
			kind = githubIssueChange(scope.EnabledAt, known, old, record, item.CreatedAt)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		issueURL := githubObjectURL(repo, "issues", item.Number)
		observations = append(observations, githubObservation{
			Key: key, Data: data, Kind: kind, UpdatedAt: item.UpdatedAt, At: item.UpdatedAt,
			URL: issueURL, Thread: ThreadRef{githubSource, issueURL},
			Title: fmt.Sprintf("GitHub Issue %s#%d %s\n%s\n%s", repo, item.Number, kind, item.Title, issueURL), Body: item.Body,
			Meta:     map[string]any{"objectType": "issue", "objectId": key, "number": item.Number, "state": item.State},
			Excluded: !matched, Rebaseline: matched && (old.Excluded || !known && !scope.Match.empty() && item.CreatedAt.Before(scope.EnabledAt)),
		})
	}
	if since.IsZero() && (!scope.Match.empty() || scope.MatchKey != "") {
		for key, previous := range scope.Items {
			id, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return nil, errors.New("invalid saved GitHub issue key")
			}
			if _, present := byID[githubDatabaseID(id)]; present {
				continue
			}
			var old githubIssueRecord
			if json.Unmarshal(previous.Data, &old) != nil {
				return nil, errors.New("invalid saved GitHub issue record")
			}
			if !old.Excluded {
				old.Excluded = true
				data, err := json.Marshal(old)
				if err != nil {
					return nil, err
				}
				observations = append(observations, githubObservation{Key: key, Data: data, UpdatedAt: previous.UpdatedAt, Excluded: true})
			}
		}
	}
	githubSortObservations(observations)
	return observations, nil
}

func githubIssueChange(enabled time.Time, known bool, old, current githubIssueRecord, created time.Time) string {
	if !known {
		if !created.Before(enabled) {
			return "opened"
		}
		return ""
	}
	if old.State != current.State {
		if current.State == "closed" {
			return "closed"
		}
		return "reopened"
	}
	if old.ContentSHA != current.ContentSHA {
		return "edited"
	}
	return ""
}

func githubObjectURL(repo, collection string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/%d", repo, collection, number)
}

func githubSortObservations(items []githubObservation) {
	slices.SortFunc(items, func(a, b githubObservation) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
}
