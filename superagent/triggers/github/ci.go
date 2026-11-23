package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
)

const (
	githubTrackedPrefix = "tracked/"
	githubTrackedPull   = "pull"
	githubCISnapshotKey = "snapshot"
)

var githubCISHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

type githubCISnapshot struct {
	Head  githubCIRevision  `json:"head"`
	Merge *githubCIRevision `json:"testMerge,omitempty"`
}

type githubCIRevision struct {
	SHA                 string           `json:"sha"`
	Actions             []githubCISignal `json:"actions"`
	UnassociatedActions int              `json:"unassociatedActions,omitempty"`
}

type githubCISignal struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
	Attempt    int    `json:"attempt"`
	Event      string `json:"event,omitempty"`
}

func trackGitHubPull(tx *sql.Tx, repo string, pull githubPullRecord, enabled time.Time) error {
	ctx := context.Background()
	scope := githubTrackedPrefix + strconv.Itoa(pull.Number)
	var previous []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM github_snapshots WHERE repository = ? AND scope = ? AND object_key = ?`,
		repo, scope, githubTrackedPull).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(previous) > 0 {
		var old githubPullRecord
		if json.Unmarshal(previous, &old) != nil || old.ID != pull.ID {
			return errors.New("invalid tracked GitHub pull snapshot")
		}
		if pull.UpdatedAt.Before(old.UpdatedAt) {
			// Do not reactivate a PR using a read older than the tracked state.
			return nil
		}
	}
	if pull.State != "open" && len(previous) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_streams (repository, scope, kind, active, enabled_at)
		VALUES (?, ?, 'ci', ?, ?)
		ON CONFLICT (repository, scope) DO UPDATE SET
			next_attempt_at = CASE WHEN active = 0 AND excluded.active = 1 THEN 0 ELSE next_attempt_at END,
			last_success_at = CASE WHEN active = 0 AND excluded.active = 1 THEN 0 ELSE last_success_at END,
			active = excluded.active`, repo, scope, pull.State == "open", githubTimeValue(enabled)); err != nil {
		return err
	}
	data, err := json.Marshal(pull)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, githubUpsertSnapshot, repo, scope, githubTrackedPull, 0, githubTimeValue(pull.UpdatedAt), data)
	return err
}

func readGitHubTrackedPR(ctx context.Context, api *githubAPI, repo string, stream *githubStreamState) ([]githubObservation, *githubPullRecord, error) {
	var old githubPullRecord
	if json.Unmarshal(stream.Items[githubTrackedPull].Data, &old) != nil || old.ID <= 0 || old.Number <= 0 {
		return nil, nil, errors.New("invalid tracked GitHub pull snapshot")
	}
	p, err := readGitHubPull(ctx, api, repo, old.Number, old.ID)
	if err != nil {
		return nil, nil, err
	}
	pull := p.record(old)
	if pull.UpdatedAt.Before(old.UpdatedAt) {
		return nil, nil, errors.New("github_outdated_pull_detail")
	}
	if pull.State != "open" {
		return nil, &pull, nil
	}
	head, err := readGitHubCIRevision(ctx, api, repo, pull.HeadSHA, pull)
	if err != nil {
		return nil, nil, err
	}
	snapshot := githubCISnapshot{Head: head}
	if pull.MergeSHA != "" && pull.MergeSHA != pull.HeadSHA {
		merge, err := readGitHubCIRevision(ctx, api, repo, pull.MergeSHA, pull)
		if err != nil {
			return nil, nil, err
		}
		snapshot.Merge = &merge
	}
	observations, err := githubCIObservations(repo, pull, stream, snapshot)
	return observations, &pull, err
}

func githubCIObservations(repo string, pull githubPullRecord, stream *githubStreamState, snapshot githubCISnapshot) ([]githubObservation, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var observations []githubObservation
	if string(data) != string(stream.Items[githubCISnapshotKey].Data) {
		observations = append(observations, githubObservation{Key: githubCISnapshotKey, Data: data, UpdatedAt: now})
	}
	prURL := githubObjectURL(repo, "pull", pull.Number)
	revisions := []githubCIRevision{snapshot.Head}
	if snapshot.Merge != nil {
		revisions = append(revisions, *snapshot.Merge)
	}
	for _, revision := range revisions {
		for _, run := range revision.Actions {
			if run.Status != "completed" || run.Conclusion == "" {
				continue
			}
			key := fmt.Sprintf("run/%s/%d/%s", run.ID, run.Attempt, run.Conclusion)
			if _, seen := stream.Items[key]; seen {
				continue
			}
			// One run can be returned for both the head and test merge.
			stream.Items[key] = githubRecord{}
			item := githubObservation{Key: key, UpdatedAt: now, At: now, Data: json.RawMessage(`null`)}
			if !stream.LastSuccessAt.IsZero() {
				item.Kind = "actions_completed"
				item.URL = run.URL
				if item.URL == "" {
					item.URL = prURL + "/checks"
				}
				item.ThreadKey = prURL
				item.Title = fmt.Sprintf("GitHub Actions for %s#%d", repo, pull.Number)
				item.Body = fmt.Sprintf("%s completed with %s (attempt %d)\nCommit %s", run.Name, run.Conclusion, run.Attempt, revision.SHA)
				item.SourceEventID = "github:actions:" + key
				item.Meta = map[string]any{
					"objectType": "pull_request_ci", "objectId": strconv.FormatInt(pull.ID, 10),
					"number": pull.Number, "headSha": pull.HeadSHA, "sha": revision.SHA, "workflowRun": run,
				}
			}
			observations = append(observations, item)
		}
	}
	return observations, nil
}

type githubWorkflowRun struct {
	ID           string
	DisplayTitle string
	Event        string
	URL          string
	RunAttempt   int
	Workflow     *struct{ Name string }
}

type githubCheckSuite struct {
	ID                   string
	Status               string
	Conclusion           string
	WorkflowRun          *githubWorkflowRun
	MatchingPullRequests githubConnection[githubWorkflowPull] `graphql:"matchingPullRequests(first:20)"`
}

type githubWorkflowPull struct {
	DatabaseID githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Number     int
	HeadRefOID string `graphql:"headRefOid" json:"headRefOid"`
}

func readGitHubCIRevision(ctx context.Context, api *githubAPI, repo, sha string, pull githubPullRecord) (githubCIRevision, error) {
	revision := githubCIRevision{SHA: sha}
	if !githubCISHA.MatchString(sha) {
		return revision, errors.New("github_invalid_ci_sha")
	}
	variables := githubRepositoryVariables(repo)
	variables["oid"] = githubv4.GitObjectID(sha)
	suites, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubCheckSuite], error) {
		variables["cursor"] = cursor
		var q struct {
			Repository struct {
				Object struct {
					Commit struct {
						OID         string                             `graphql:"oid"`
						CheckSuites githubConnection[githubCheckSuite] `graphql:"checkSuites(first:20,after:$cursor)"`
					} `graphql:"... on Commit"`
				} `graphql:"object(oid:$oid)"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		if err := api.query(ctx, &q, variables); err != nil {
			return githubConnection[githubCheckSuite]{}, err
		}
		commit := q.Repository.Object.Commit
		if commit.OID != sha {
			return githubConnection[githubCheckSuite]{}, errors.New("github_ci_commit_unavailable")
		}
		return commit.CheckSuites, nil
	})
	if err != nil {
		return revision, err
	}
	revision.Actions, revision.UnassociatedActions, err = githubActionSignals(ctx, api, suites, pull)
	return revision, err
}

func githubActionSignals(ctx context.Context, api *githubAPI, suites []githubCheckSuite, pull githubPullRecord) ([]githubCISignal, int, error) {
	byID := make(map[string]githubCISignal)
	unknown := make(map[string]bool)
	for _, suite := range suites {
		run := suite.WorkflowRun
		if run == nil {
			continue
		}
		if run.ID == "" || suite.ID == "" || suite.Status == "" || run.RunAttempt <= 0 {
			return nil, 0, errors.New("github_invalid_workflow_run")
		}
		linked, missing, err := githubSuiteMatchesPull(ctx, api, suite, pull)
		if err != nil {
			return nil, 0, err
		}
		prior, seen := byID[run.ID]
		if !linked {
			if !seen && missing {
				unknown[run.ID] = true
			}
			continue
		}
		if !seen || run.RunAttempt > prior.Attempt {
			name := run.DisplayTitle
			if run.Workflow != nil && run.Workflow.Name != "" {
				name = run.Workflow.Name
			}
			byID[run.ID] = githubCISignal{
				ID: run.ID, Name: name, Status: strings.ToLower(suite.Status),
				Conclusion: strings.ToLower(suite.Conclusion), URL: run.URL, Attempt: run.RunAttempt, Event: run.Event,
			}
		}
		delete(unknown, run.ID)
	}
	out := make([]githubCISignal, 0, len(byID))
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out, len(unknown), nil
}

func githubSuiteMatchesPull(ctx context.Context, api *githubAPI, suite githubCheckSuite, pull githubPullRecord) (bool, bool, error) {
	associations := make(map[githubDatabaseID]bool)
	err := githubPages(func(cursor *githubv4.String) (githubConnection[githubWorkflowPull], error) {
		if cursor == nil {
			return suite.MatchingPullRequests, nil
		}
		var q struct {
			Node struct {
				Suite struct {
					MatchingPullRequests githubConnection[githubWorkflowPull] `graphql:"matchingPullRequests(first:20,after:$cursor)"`
				} `graphql:"... on CheckSuite"`
			} `graphql:"node(id:$id)"`
		}
		err := api.query(ctx, &q, map[string]any{"id": suite.ID, "cursor": cursor})
		return q.Node.Suite.MatchingPullRequests, err
	}, func(nodes []githubWorkflowPull) error {
		for _, association := range nodes {
			if association.DatabaseID <= 0 || association.Number <= 0 || association.HeadRefOID == "" {
				return errors.New("github_invalid_workflow_association")
			}
			if association.HeadRefOID == pull.HeadSHA {
				associations[association.DatabaseID] = association.Number == pull.Number
			}
		}
		return nil
	})
	// A shared head does not resolve attribution.
	return len(associations) == 1 && associations[githubDatabaseID(pull.ID)], len(associations) != 1, err
}
