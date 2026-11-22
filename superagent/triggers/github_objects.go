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

const githubPRThreadSource = "pr:url"

type githubIssue struct {
	ID        githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Number    int
	Title     string
	Body      string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

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
}

type githubCommitOID struct {
	OID string `graphql:"oid" json:"oid"`
}

type githubRepositoryName struct{ NameWithOwner string }

type githubObjectRecord struct {
	ID         int64    `json:"id"`
	State      string   `json:"state"`
	ContentSHA [32]byte `json:"contentSHA"`
}

type githubPullRecord struct {
	githubObjectRecord
	Number    int       `json:"number"`
	Draft     bool      `json:"draft"`
	BaseSHA   string    `json:"baseSHA"`
	BaseRef   string    `json:"baseRef"`
	HeadSHA   string    `json:"headSHA"`
	HeadRepo  string    `json:"headRepository,omitempty"`
	MergeSHA  string    `json:"mergeSHA,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func githubObjectRecordFor(id githubDatabaseID, state, title, body string) githubObjectRecord {
	content, _ := json.Marshal([2]string{title, body}) //nolint:errchkjson // A fixed array of strings is always encodable.
	return githubObjectRecord{ID: int64(id), State: strings.ToLower(state), ContentSHA: sha256.Sum256(content)}
}

func (p githubPull) record(old githubPullRecord) githubPullRecord {
	state := strings.ToLower(p.State)
	var headRepo, mergeSHA string
	if p.HeadRepository != nil {
		headRepo = strings.ToLower(p.HeadRepository.NameWithOwner)
	}
	if state == "open" {
		if p.PotentialMergeCommit != nil && p.Mergeable == "MERGEABLE" {
			mergeSHA = p.PotentialMergeCommit.OID
		} else if p.Mergeable == "UNKNOWN" && p.HeadRefOID == old.HeadSHA && p.BaseRefOID == old.BaseSHA {
			// GitHub calculates the test merge asynchronously.
			mergeSHA = old.MergeSHA
		}
	}
	return githubPullRecord{
		githubObjectRecord: githubObjectRecordFor(p.ID, p.State, p.Title, p.Body),
		Number:             p.Number, Draft: p.Draft, BaseSHA: p.BaseRefOID, BaseRef: p.BaseRefName,
		HeadSHA: p.HeadRefOID, HeadRepo: headRepo, MergeSHA: mergeSHA, UpdatedAt: p.UpdatedAt,
	}
}

func readGitHubIssue(ctx context.Context, api *githubAPI, repo string, number int) (githubIssue, error) {
	variables := githubRepositoryVariables(repo)
	variables["number"] = githubv4.Int(number)
	var q struct {
		Repository struct {
			Issue githubIssue `graphql:"issue(number:$number)"`
		} `graphql:"repository(owner:$owner,name:$name)"`
	}
	if err := api.query(ctx, &q, variables); err != nil {
		return githubIssue{}, err
	}
	item := q.Repository.Issue
	if item.ID <= 0 || item.Number != number || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) ||
		(item.State != "OPEN" && item.State != "CLOSED") {
		return githubIssue{}, errors.New("github_invalid_issue_detail")
	}
	return item, nil
}

func readGitHubPull(ctx context.Context, api *githubAPI, repo string, number int, expectedID int64) (githubPull, error) {
	variables := githubRepositoryVariables(repo)
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
	if item.ID <= 0 || item.Number != number || expectedID != 0 && int64(item.ID) != expectedID ||
		item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) ||
		(item.State != "OPEN" && item.State != "CLOSED" && item.State != "MERGED") ||
		item.State == "OPEN" && (item.BaseRefOID == "" || item.HeadRefOID == "") {
		return githubPull{}, errors.New("github_invalid_pull_detail")
	}
	return item, nil
}

func githubObjectChanges(enabled, created time.Time, old, current githubObjectRecord) []string {
	if old.ID == 0 {
		if created.Before(enabled) {
			return nil
		}
		if current.State != "open" {
			return []string{"opened", current.State}
		}
		return []string{"opened"}
	}
	var changes []string
	if old.State != current.State {
		change := current.State
		if current.State == "open" {
			change = "reopened"
		}
		changes = append(changes, change)
	}
	if old.ContentSHA != current.ContentSHA {
		changes = append(changes, "edited")
	}
	return changes
}

func githubPullChanges(enabled, created time.Time, old, current githubPullRecord) []string {
	changes := githubObjectChanges(enabled, created, old.githubObjectRecord, current.githubObjectRecord)
	if old.ID == 0 {
		return changes
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
	if old.BaseRef != current.BaseRef && !slices.Contains(changes, "edited") {
		changes = append(changes, "edited")
	}
	return changes
}

func githubObjectURL(repo, collection string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/%d", repo, collection, number)
}

func githubSortObservations(items []githubObservation) {
	slices.SortFunc(items, func(a, b githubObservation) int {
		if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
}

const githubObjectKey = "object"

func readGitHubNotification(ctx context.Context, api *githubAPI, repo string, notice githubNotice, stream *githubStreamState) ([]githubObservation, *githubPullRecord, error) {
	object, pull, err := readGitHubNotifiedObject(ctx, api, repo, notice, stream)
	if err != nil {
		return nil, nil, err
	}
	discussion, err := readGitHubNotifiedDiscussion(ctx, api, repo, notice, stream)
	if err != nil {
		return nil, nil, err
	}
	observations := append([]githubObservation{object}, discussion...)
	githubSortObservations(observations)
	for i := range observations {
		observations[i] = observations[i].withNoticeContext(notice)
	}
	for i := range observations {
		if observations[i].Kind != "" {
			if _, seen := stream.Items[githubObjectKey]; !seen {
				observations[i].Body += githubHistoricalDiscussion(discussion)
			}
			return observations, pull, nil
		}
	}
	fallback := object
	fallback.Key = "event"
	fallback.UpdatedAt, fallback.At = notice.UpdatedAt, notice.UpdatedAt
	fallback.Kind = "notification_received"
	fallback.Title = fmt.Sprintf("%s\nGitHub notification reason: %s", object.Title, notice.Reason)
	fallback.Body += githubHistoricalDiscussion(discussion)
	return append(observations, fallback), pull, nil
}

func (o githubObservation) withNoticeContext(notice githubNotice) githubObservation {
	o.Meta["notificationThreadId"] = notice.ID
	o.Meta["notificationReason"] = notice.Reason
	o.Meta["notificationUpdatedAt"] = notice.UpdatedAt
	o.SourceEventID = "github:notification:" + notice.ID + ":" + notice.UpdatedAt.UTC().Format(time.RFC3339Nano)
	return o
}

func readGitHubNotifiedObject(ctx context.Context, api *githubAPI, repo string, notice githubNotice, stream *githubStreamState) (githubObservation, *githubPullRecord, error) {
	previous := stream.Items[githubObjectKey]
	thread := notice.thread(repo)
	object := githubObservation{Key: githubObjectKey, URL: thread.ID, Thread: thread}
	var record any
	var pull *githubPullRecord
	var base githubObjectRecord
	var changes []string
	var label, objectType, title string
	if notice.Pull {
		var old githubPullRecord
		if len(previous.Data) != 0 && json.Unmarshal(previous.Data, &old) != nil {
			return object, nil, errors.New("invalid saved GitHub pull record")
		}
		p, err := readGitHubPull(ctx, api, repo, notice.Number, old.ID)
		if err != nil {
			return object, nil, err
		}
		current := p.record(old)
		pull, record, base = &current, current, current.githubObjectRecord
		changes = githubPullChanges(stream.EnabledAt, p.CreatedAt, old, current)
		object.Body, object.UpdatedAt, title, label, objectType = p.Body, p.UpdatedAt, p.Title, "PR", "pull_request"
	} else {
		var old githubObjectRecord
		if len(previous.Data) != 0 && json.Unmarshal(previous.Data, &old) != nil {
			return object, nil, errors.New("invalid saved GitHub issue record")
		}
		p, err := readGitHubIssue(ctx, api, repo, notice.Number)
		if err != nil {
			return object, nil, err
		}
		base = githubObjectRecordFor(p.ID, p.State, p.Title, p.Body)
		if old.ID != 0 && old.ID != base.ID {
			return object, nil, errors.New("github_issue_identity_changed")
		}
		record = base
		changes = githubObjectChanges(stream.EnabledAt, p.CreatedAt, old, base)
		object.Body, object.UpdatedAt, title, label, objectType = p.Body, p.UpdatedAt, p.Title, "Issue", "issue"
	}
	if object.UpdatedAt.Before(previous.UpdatedAt) {
		return object, nil, errors.New("github_outdated_object_detail")
	}
	object.At = object.UpdatedAt
	object.Meta = map[string]any{"objectType": objectType, "objectId": strconv.FormatInt(base.ID, 10), "number": notice.Number, "state": base.State}
	changeText := ""
	if len(changes) > 0 {
		object.Kind = changes[0]
		object.Meta["changes"] = changes
		changeText = " " + strings.Join(changes, ", ")
	}
	object.Title = fmt.Sprintf("GitHub %s %s#%d%s\n%s\n%s", label, repo, notice.Number, changeText, title, object.URL)
	var err error
	object.Data, err = json.Marshal(record)
	return object, pull, err
}

func readGitHubNotifiedDiscussion(ctx context.Context, api *githubAPI, repo string, notice githubNotice, stream *githubStreamState) ([]githubObservation, error) {
	comments, err := readGitHubMainComments(ctx, api, repo, notice.Number, !notice.Pull)
	if err != nil {
		return nil, err
	}
	items, err := githubCommentObservations(repo, notice.Number, stream, "issue_comment", comments)
	if err != nil || !notice.Pull {
		return items, err
	}
	reviews, err := pollGitHubReviews(ctx, api, repo, notice.Number, stream)
	if err != nil {
		return nil, err
	}
	inline, err := readGitHubInlineComments(ctx, api, repo, notice.Number)
	if err != nil {
		return nil, err
	}
	observations, err := githubCommentObservations(repo, notice.Number, stream, "review_comment", inline)
	return slices.Concat(items, reviews, observations), err
}

func githubHistoricalDiscussion(discussion []githubObservation) string {
	var b strings.Builder
	githubSortObservations(discussion)
	count := 0
	for _, item := range slices.Backward(discussion) {
		if item.Kind != "" {
			continue
		}
		if count == 0 {
			b.WriteString("\n\nEarlier discussion")
		}
		excerpt := []rune(item.Body)
		fmt.Fprintf(&b, "\n- %s (%s): %s", item.Actor, item.URL, string(excerpt[:min(len(excerpt), 256)]))
		count++
		if count == 3 {
			break
		}
	}
	return b.String()
}
