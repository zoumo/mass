package triggers

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
)

var githubCISHA = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

type githubCIApp struct{ Slug string }

type githubCheckRun struct {
	DatabaseID int64 `graphql:"databaseId" json:"databaseId"`
	Name       string
	Status     string
	Conclusion string
	Permalink  string
	DetailsURL string `graphql:"detailsUrl" json:"detailsUrl"`
	CheckSuite *struct{ App *githubCIApp }
}

type githubCommitStatus struct {
	ID        string
	Context   string
	State     string
	TargetURL string `graphql:"targetUrl" json:"targetUrl"`
	UpdatedAt time.Time
}

type githubCIContext struct {
	Type   string             `graphql:"__typename" json:"__typename"`
	Check  githubCheckRun     `graphql:"... on CheckRun" json:"-"`
	Status githubCommitStatus `graphql:"... on StatusContext" json:"-"`
}

type githubWorkflowRun struct {
	DatabaseID   int64 `graphql:"databaseId" json:"databaseId"`
	DisplayTitle string
	Event        string
	URL          string
	RunAttempt   int
	Workflow     *struct{ Name string }
}

type githubCheckSuite struct {
	App                  *githubCIApp
	CheckRuns            githubConnection[githubSuiteCheckRun] `graphql:"checkRuns(first:20,filterBy:{checkType:LATEST})"`
	ID                   string
	Status               string
	Conclusion           string
	WorkflowRun          *githubWorkflowRun
	MatchingPullRequests githubConnection[githubWorkflowPull] `graphql:"matchingPullRequests(first:20)"`
}

type githubSuiteCheckRun struct {
	DatabaseID int64 `graphql:"databaseId" json:"databaseId"`
	Name       string
	Status     string
	Conclusion string
	Permalink  string
	DetailsURL string `graphql:"detailsUrl" json:"detailsUrl"`
}

type githubWorkflowPull struct {
	DatabaseID githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Number     int
	HeadRefOID string `graphql:"headRefOid" json:"headRefOid"`
}

func readGitHubCIRevision(ctx context.Context, api *githubAPI, repo, sha string, pull githubPullRecord) (githubCIRevision, error) {
	var revision githubCIRevision
	if !githubCISHA.MatchString(sha) {
		return revision, errors.New("github_invalid_ci_sha")
	}
	revision.SHA = sha
	variables := githubRepositoryVariables(repo)
	variables["oid"] = githubv4.GitObjectID(sha)
	contexts, err := readGitHubCIContexts(ctx, api, variables, sha)
	if err != nil {
		return revision, err
	}
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
	// The rollup can omit checks that exist in a suite. Keep the rollup's
	// current checks and use the suite pages to fill those gaps by run ID.
	for _, suite := range suites {
		additional, err := readGitHubSuiteChecks(ctx, api, suite)
		if err != nil {
			return revision, err
		}
		for _, run := range additional {
			contexts = append(contexts, githubCIContext{Type: "CheckRun", Check: githubCheckRun{
				DatabaseID: run.DatabaseID, Name: run.Name, Status: run.Status, Conclusion: run.Conclusion,
				Permalink: run.Permalink, DetailsURL: run.DetailsURL, CheckSuite: &struct{ App *githubCIApp }{suite.App},
			}})
		}
	}
	revision.Checks, revision.Statuses, err = githubContextSignals(contexts)
	if err != nil {
		return revision, err
	}
	revision.Actions, revision.UnassociatedActions, err = githubActionSignals(ctx, api, suites, pull)
	return revision, err
}

func readGitHubSuiteChecks(ctx context.Context, api *githubAPI, suite githubCheckSuite) ([]githubSuiteCheckRun, error) {
	if suite.ID == "" {
		return nil, errors.New("github_invalid_check_suite")
	}
	return githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubSuiteCheckRun], error) {
		if cursor == nil {
			return suite.CheckRuns, nil
		}
		var q struct {
			Node struct {
				Suite struct {
					CheckRuns githubConnection[githubSuiteCheckRun] `graphql:"checkRuns(first:100,after:$cursor,filterBy:{checkType:LATEST})"`
				} `graphql:"... on CheckSuite"`
			} `graphql:"node(id:$id)"`
		}
		err := api.query(ctx, &q, map[string]any{"id": suite.ID, "cursor": cursor})
		return q.Node.Suite.CheckRuns, err
	})
}

func readGitHubCIContexts(ctx context.Context, api *githubAPI, variables map[string]any, sha string) ([]githubCIContext, error) {
	return githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubCIContext], error) {
		variables["cursor"] = cursor
		var q struct {
			Repository struct {
				Object struct {
					Commit struct {
						OID               string `graphql:"oid"`
						StatusCheckRollup *struct {
							Contexts githubConnection[githubCIContext] `graphql:"contexts(first:100,after:$cursor)"`
						}
					} `graphql:"... on Commit"`
				} `graphql:"object(oid:$oid)"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		if err := api.query(ctx, &q, variables); err != nil {
			return githubConnection[githubCIContext]{}, err
		}
		commit := q.Repository.Object.Commit
		if commit.OID != sha {
			return githubConnection[githubCIContext]{}, errors.New("github_ci_commit_unavailable")
		}
		if commit.StatusCheckRollup == nil && cursor == nil {
			return githubConnection[githubCIContext]{Nodes: []githubCIContext{}, PageInfo: githubPageInfo{HasNextPage: new(false)}}, nil
		}
		if commit.StatusCheckRollup == nil {
			return githubConnection[githubCIContext]{}, errors.New("github_incomplete_ci_contexts")
		}
		return commit.StatusCheckRollup.Contexts, nil
	})
}

func githubContextSignals(contexts []githubCIContext) ([]githubCISignal, []githubCISignal, error) {
	checks := make(map[int64]githubCISignal)
	statuses := make(map[string]githubCommitStatus)
	for _, item := range contexts {
		switch item.Type {
		case "CheckRun":
			check := item.Check
			if check.DatabaseID <= 0 || check.Name == "" || check.Status == "" {
				return nil, nil, errors.New("github_invalid_check_run")
			}
			link := check.Permalink
			if link == "" {
				link = check.DetailsURL
			}
			signal := githubCISignal{ID: strconv.FormatInt(check.DatabaseID, 10), Name: check.Name, Status: strings.ToLower(check.Status), Conclusion: strings.ToLower(check.Conclusion), URL: link}
			if check.CheckSuite != nil && check.CheckSuite.App != nil {
				signal.Provider = check.CheckSuite.App.Slug
			}
			if _, seen := checks[check.DatabaseID]; !seen {
				checks[check.DatabaseID] = signal
			}
		case "StatusContext":
			status := item.Status
			if status.ID == "" || status.Context == "" || status.State == "" || status.UpdatedAt.IsZero() {
				return nil, nil, errors.New("github_invalid_commit_status")
			}
			prior := statuses[status.Context]
			if prior.ID == "" || status.UpdatedAt.After(prior.UpdatedAt) {
				statuses[status.Context] = status
			}
		default:
			return nil, nil, errors.New("github_invalid_ci_context")
		}
	}
	orderedStatuses := make([]githubCISignal, 0, len(statuses))
	for _, key := range slices.Sorted(maps.Keys(statuses)) {
		status := statuses[key]
		orderedStatuses = append(orderedStatuses, githubCISignal{ID: status.ID, Name: status.Context, Status: strings.ToLower(status.State), URL: status.TargetURL})
	}
	return githubOrderedSignals(checks), orderedStatuses, nil
}

func githubActionSignals(ctx context.Context, api *githubAPI, suites []githubCheckSuite, pull githubPullRecord) ([]githubCISignal, int, error) {
	byID := make(map[int64]githubCISignal)
	unknown := make(map[int64]bool)
	for _, suite := range suites {
		run := suite.WorkflowRun
		if run == nil {
			continue
		}
		if run.DatabaseID <= 0 || suite.ID == "" || suite.Status == "" || run.RunAttempt <= 0 {
			return nil, 0, errors.New("github_invalid_workflow_run")
		}
		linked, missing, err := githubSuiteMatchesPull(ctx, api, suite, pull)
		if err != nil {
			return nil, 0, err
		}
		prior, seen := byID[run.DatabaseID]
		if !linked {
			if !seen && missing {
				unknown[run.DatabaseID] = true
			}
			continue
		}
		if !seen || run.RunAttempt > prior.Attempt {
			name := run.DisplayTitle
			if run.Workflow != nil && run.Workflow.Name != "" {
				name = run.Workflow.Name
			}
			byID[run.DatabaseID] = githubCISignal{
				ID: strconv.FormatInt(run.DatabaseID, 10), Name: name, Status: strings.ToLower(suite.Status),
				Conclusion: strings.ToLower(suite.Conclusion), URL: run.URL, Attempt: run.RunAttempt, Event: run.Event,
			}
		}
		delete(unknown, run.DatabaseID)
	}
	return githubOrderedSignals(byID), len(unknown), nil
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
	// GitHub returns PRs matching the suite, which can include multiple
	// open PRs for the same head. A shared head does not resolve attribution.
	return len(associations) == 1 && associations[githubDatabaseID(pull.ID)], len(associations) != 1, err
}

func githubOrderedSignals(byID map[int64]githubCISignal) []githubCISignal {
	out := make([]githubCISignal, 0, len(byID))
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out
}
