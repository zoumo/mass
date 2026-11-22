package triggers

import (
	"context"
	"crypto/sha256"
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

const (
	githubPullsScope     = "pulls"
	githubPRThreadSource = "pr:url"
)

type githubPull struct {
	ID                   githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Number               int
	Title                string
	Body                 string
	State                string
	Draft                bool `graphql:"isDraft" json:"isDraft"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Mergeable            string
	PotentialMergeCommit *githubCommitOID
	BaseRefOID           string `graphql:"baseRefOid" json:"baseRefOid"`
	BaseRefName          string
	HeadRefOID           string `graphql:"headRefOid" json:"headRefOid"`
	HeadRepository       *githubRepositoryName
	githubMatchFields
}

type githubCommitOID struct {
	OID string `graphql:"oid" json:"oid"`
}

type githubRepositoryName struct{ NameWithOwner string }

type githubPullRecord struct {
	ID         int64    `json:"id"`
	Number     int      `json:"number"`
	State      string   `json:"state"`
	Draft      bool     `json:"draft"`
	BaseSHA    string   `json:"baseSHA"`
	BaseRef    string   `json:"baseRef"`
	HeadSHA    string   `json:"headSHA"`
	HeadRepo   string   `json:"headRepository,omitempty"`
	MergeSHA   string   `json:"mergeSHA,omitempty"`
	ContentSHA [32]byte `json:"contentSHA"`
	Excluded   bool     `json:"excluded,omitempty"`
}

func (p githubPull) record() githubPullRecord {
	state := strings.ToLower(p.State)
	content, _ := json.Marshal([2]string{p.Title, p.Body}) //nolint:errchkjson // A fixed array of strings is always encodable.
	var headRepo, mergeSHA string
	if p.HeadRepository != nil {
		headRepo = strings.ToLower(p.HeadRepository.NameWithOwner)
	}
	if state == "open" && p.PotentialMergeCommit != nil && p.Mergeable == "MERGEABLE" {
		mergeSHA = p.PotentialMergeCommit.OID
	}
	return githubPullRecord{
		ID: int64(p.ID), Number: p.Number, State: state, Draft: p.Draft, BaseSHA: p.BaseRefOID,
		BaseRef: p.BaseRefName, HeadSHA: p.HeadRefOID, HeadRepo: headRepo, MergeSHA: mergeSHA, ContentSHA: sha256.Sum256(content),
	}
}

func (p githubPull) valid() bool {
	return p.ID > 0 && p.Number > 0 && !p.CreatedAt.IsZero() && !p.UpdatedAt.Before(p.CreatedAt) &&
		p.BaseRefOID != "" && p.HeadRefOID != "" && (p.State == "OPEN" || p.State == "CLOSED" || p.State == "MERGED")
}

func pollGitHubPulls(ctx context.Context, api *githubAPI, repo string, scope *githubStreamState, since time.Time) ([]githubObservation, error) {
	var open []githubPull
	// A full listing already samples every open pull. In an incremental round,
	// scan all open pulls separately because changes to refs or the test merge
	// commit need not update the pull's timestamp.
	if !since.IsZero() {
		var err error
		open, err = readGitHubPulls(ctx, api, repo, []githubv4.PullRequestState{githubv4.PullRequestStateOpen}, time.Time{}, scope.Match)
		if err != nil {
			return nil, err
		}
	}
	all, err := readGitHubPulls(ctx, api, repo, []githubv4.PullRequestState{githubv4.PullRequestStateOpen, githubv4.PullRequestStateClosed, githubv4.PullRequestStateMerged}, since, scope.Match)
	if err != nil {
		return nil, err
	}
	candidates := make(map[int]githubPull)
	for _, pull := range slices.Concat(open, all) {
		if current, ok := candidates[pull.Number]; !ok || !pull.UpdatedAt.Before(current.UpdatedAt) {
			candidates[pull.Number] = pull
		}
	}
	previous, err := githubSavedPulls(scope)
	if err != nil {
		return nil, err
	}
	for number, stored := range previous {
		if _, found := candidates[number]; stored.State == "open" && !stored.Excluded && !found {
			pull, err := readGitHubPull(ctx, api, repo, number, stored.ID, scope.Match)
			if err != nil {
				return nil, err
			}
			candidates[number] = pull
		}
	}
	var observations []githubObservation
	for _, number := range slices.Sorted(maps.Keys(candidates)) {
		candidate := candidates[number]
		key := strconv.Itoa(number)
		old, known := previous[number]
		stored := scope.Items[key]
		if known && (int64(candidate.ID) != old.ID || candidate.UpdatedAt.Before(stored.UpdatedAt)) {
			continue
		}
		record := candidate.record()
		matched, err := candidate.matches(ctx, api, repo, false, number, scope.Match)
		if err != nil {
			return nil, err
		}
		record.Excluded = !matched
		// GitHub calculates mergeability asynchronously. Keep the last confirmed
		// test merge until the revisions change or the calculation completes.
		if record.State == "open" && candidate.Mergeable == "UNKNOWN" && old.HeadSHA == record.HeadSHA && old.BaseSHA == record.BaseSHA {
			record.MergeSHA = old.MergeSHA
		}
		if known && old == record && candidate.UpdatedAt.Equal(stored.UpdatedAt) {
			continue
		}
		var kind string
		var changes []string
		if matched && !old.Excluded {
			kind, changes = githubPullChange(scope.EnabledAt, known, old, record, candidate.CreatedAt)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		prURL := githubObjectURL(repo, "pull", number)
		observations = append(observations, githubObservation{
			Key: key, Kind: kind, Data: data, UpdatedAt: candidate.UpdatedAt, At: candidate.UpdatedAt,
			URL: prURL, Thread: ThreadRef{githubPRThreadSource, prURL},
			Title: fmt.Sprintf("GitHub PR %s#%d %s\n%s\n%s", repo, number, strings.Join(changes, ", "), candidate.Title, prURL), Body: candidate.Body,
			Meta: map[string]any{
				"objectType": "pull_request", "objectId": strconv.FormatInt(int64(candidate.ID), 10), "number": number, "state": record.State,
				"changes": changes, "draft": record.Draft, "baseSha": record.BaseSHA, "baseRef": record.BaseRef, "headSha": record.HeadSHA, "headRepository": record.HeadRepo,
			},
			Excluded: !matched, Rebaseline: matched && (old.Excluded || !known && !scope.Match.empty() && candidate.CreatedAt.Before(scope.EnabledAt)),
		})
	}
	if since.IsZero() && (!scope.Match.empty() || scope.MatchKey != "") {
		for number, old := range previous {
			if _, present := candidates[number]; present || old.Excluded {
				continue
			}
			key := strconv.Itoa(number)
			old.Excluded = true
			data, err := json.Marshal(old)
			if err != nil {
				return nil, err
			}
			observations = append(observations, githubObservation{Key: key, Data: data, UpdatedAt: scope.Items[key].UpdatedAt, Excluded: true})
		}
	}
	githubSortObservations(observations)
	return observations, nil
}

func readGitHubPulls(ctx context.Context, api *githubAPI, repo string, states []githubv4.PullRequestState, since time.Time, match githubMatch) ([]githubPull, error) {
	variables := githubRepositoryVariables(repo)
	match.variables(variables)
	variables["states"] = states
	var items []githubPull
	err := githubPages(func(cursor *githubv4.String) (githubConnection[githubPull], error) {
		var q struct {
			Repository struct {
				PullRequests githubConnection[githubPull] `graphql:"pullRequests(first:100,after:$cursor,states:$states,orderBy:{field:UPDATED_AT,direction:DESC})"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		variables["cursor"] = cursor
		err := api.query(ctx, &q, variables)
		return q.Repository.PullRequests, err
	}, func(nodes []githubPull) error {
		for _, pull := range nodes {
			if !pull.valid() || len(states) == 1 && pull.State != string(states[0]) {
				return errors.New("github_invalid_pull")
			}
			if pull.UpdatedAt.Before(since) {
				return errGitHubStopPagination
			}
			items = append(items, pull)
		}
		return nil
	})
	return items, err
}

func readGitHubPull(ctx context.Context, api *githubAPI, repo string, number int, id int64, match githubMatch) (githubPull, error) {
	variables := githubRepositoryVariables(repo)
	match.variables(variables)
	variables["number"] = githubv4.Int(number)
	var q struct {
		Repository struct {
			PullRequest githubPull `graphql:"pullRequest(number:$number)"`
		} `graphql:"repository(owner:$owner,name:$name)"`
	}
	if err := api.query(ctx, &q, variables); err != nil {
		return githubPull{}, err
	}
	item := q.Repository.PullRequest
	if !item.valid() || item.Number != number || int64(item.ID) != id {
		return githubPull{}, errors.New("github_invalid_pull_detail")
	}
	return item, nil
}

func githubSavedPulls(scope *githubStreamState) (map[int]githubPullRecord, error) {
	pulls := make(map[int]githubPullRecord)
	for _, previous := range scope.Items {
		var pr githubPullRecord
		if json.Unmarshal(previous.Data, &pr) != nil || pr.Number <= 0 || pr.ID <= 0 {
			return nil, errors.New("invalid saved GitHub pull record")
		}
		pulls[pr.Number] = pr
	}
	return pulls, nil
}

func githubPullChange(enabled time.Time, known bool, old, current githubPullRecord, created time.Time) (string, []string) {
	var changes []string
	if !known {
		if created.Before(enabled) {
			return "", nil
		}
		changes = append(changes, "opened")
		if current.State != "open" {
			changes = append(changes, current.State)
		}
		return changes[0], changes
	}
	if old.State != current.State {
		change := current.State
		if current.State == "open" {
			change = "reopened"
		}
		changes = append(changes, change)
	}
	if old.HeadSHA != current.HeadSHA || old.HeadRepo != current.HeadRepo {
		changes = append(changes, "head_updated")
	}
	if old.Draft != current.Draft {
		if current.Draft {
			changes = append(changes, "converted_to_draft")
		} else {
			changes = append(changes, "ready_for_review")
		}
	}
	if old.ContentSHA != current.ContentSHA || old.BaseRef != "" && old.BaseRef != current.BaseRef {
		changes = append(changes, "edited")
	}
	if len(changes) == 0 {
		return "", nil
	}
	return changes[0], changes
}
