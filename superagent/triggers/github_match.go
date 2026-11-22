package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shurcooL/githubv4"
	"go.yaml.in/yaml/v3"
)

type githubMatch struct {
	Labels, Assignees, Authors []string
}

func readGitHubMatch(node yaml.Node) (githubMatch, error) {
	var match githubMatch
	if node.Kind == 0 {
		return match, nil
	}
	var fields map[string]yaml.Node
	if node.Decode(&fields) != nil || len(fields) == 0 {
		return match, errors.New("match requires labels, assignees or authors")
	}
	for key, values := range fields {
		var list *[]string
		switch key {
		case "labels":
			list = &match.Labels
		case "assignees":
			list = &match.Assignees
		case "authors":
			list = &match.Authors
		default:
			return githubMatch{}, errors.New("match only supports labels, assignees and authors")
		}
		var entries []yaml.Node
		if values.Decode(&entries) != nil || values.ShortTag() == "!!null" {
			return githubMatch{}, errors.New("match values must be lists of nonempty strings")
		}
		for _, entry := range entries {
			var value string
			if entry.ShortTag() != "!!str" || entry.Decode(&value) != nil || strings.TrimSpace(value) == "" {
				return githubMatch{}, errors.New("match values must be lists of nonempty strings")
			}
			*list = append(*list, strings.ToLower(strings.TrimSpace(value)))
		}
		slices.Sort(*list)
		*list = slices.Compact(*list)
	}
	if match.empty() {
		return match, errors.New("match requires at least one label, assignee or author")
	}
	return match, nil
}

func (m githubMatch) empty() bool {
	return len(m.Labels)+len(m.Assignees)+len(m.Authors) == 0
}

func (m githubMatch) key() string {
	if m.empty() {
		return ""
	}
	data, _ := json.Marshal(m) //nolint:errchkjson // Match contains only typed filters.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (m githubMatch) variables(vars map[string]any) {
	vars["matchLabels"] = githubv4.Boolean(len(m.Labels) != 0)
	vars["matchAssignees"] = githubv4.Boolean(len(m.Assignees) != 0)
	vars["matchAuthors"] = githubv4.Boolean(len(m.Authors) != 0)
}

type (
	githubLabelName struct{ Name string }
	githubLogin     struct{ Login string }
)

type githubMatchFields struct {
	Labels    *githubConnection[githubLabelName] `graphql:"labels(first:10) @include(if:$matchLabels)" json:"labels,omitempty"`
	Assignees *githubConnection[githubLogin]     `graphql:"assignees(first:10) @include(if:$matchAssignees)" json:"assignees,omitempty"`
	Author    *githubLogin                       `graphql:"author @include(if:$matchAuthors)" json:"author,omitempty"`
}

func (f githubMatchFields) matches(ctx context.Context, api *githubAPI, repo string, isIssue bool, number int, match githubMatch) (bool, error) {
	if match.empty() || f.Author != nil && slices.Contains(match.Authors, strings.ToLower(f.Author.Login)) {
		return true, nil
	}
	if f.Labels != nil && slices.ContainsFunc(f.Labels.Nodes, func(label githubLabelName) bool {
		return slices.Contains(match.Labels, strings.ToLower(label.Name))
	}) {
		return true, nil
	}
	if f.Assignees != nil && slices.ContainsFunc(f.Assignees.Nodes, func(user githubLogin) bool {
		return slices.Contains(match.Assignees, strings.ToLower(user.Login))
	}) {
		return true, nil
	}
	vars := githubRepositoryVariables(repo)
	vars["number"] = githubv4.Int(number)
	vars["isIssue"] = githubv4.Boolean(isIssue)
	if len(match.Labels) > 0 && f.Labels != nil {
		found, err := githubMatchPages(*f.Labels, match.Labels, func(label githubLabelName) string { return label.Name },
			func(cursor *githubv4.String) (githubConnection[githubLabelName], error) {
				type fields struct {
					Labels githubConnection[githubLabelName] `graphql:"labels(first:100,after:$cursor)"`
				}
				return readGitHubMatchPage(ctx, api, vars, cursor, isIssue, func(page fields) githubConnection[githubLabelName] { return page.Labels })
			})
		if found || err != nil {
			return found, err
		}
	}
	if len(match.Assignees) > 0 && f.Assignees != nil {
		return githubMatchPages(*f.Assignees, match.Assignees, func(user githubLogin) string { return user.Login },
			func(cursor *githubv4.String) (githubConnection[githubLogin], error) {
				type fields struct {
					Assignees githubConnection[githubLogin] `graphql:"assignees(first:100,after:$cursor)"`
				}
				return readGitHubMatchPage(ctx, api, vars, cursor, isIssue, func(page fields) githubConnection[githubLogin] { return page.Assignees })
			})
	}
	return false, nil
}

func githubMatchPages[T any](first githubConnection[T], values []string, value func(T) string,
	read func(*githubv4.String) (githubConnection[T], error),
) (bool, error) {
	found := false
	err := githubPages(func(cursor *githubv4.String) (githubConnection[T], error) {
		if cursor == nil {
			return first, nil
		}
		return read(cursor)
	}, func(nodes []T) error {
		for _, item := range nodes {
			if slices.Contains(values, strings.ToLower(value(item))) {
				found = true
				return errGitHubStopPagination
			}
		}
		return nil
	})
	return found, err
}

func readGitHubMatchPage[F, T any](ctx context.Context, api *githubAPI, vars map[string]any, cursor *githubv4.String,
	isIssue bool, connection func(F) githubConnection[T],
) (githubConnection[T], error) {
	var q struct {
		Repository struct {
			Issue F `graphql:"issue(number:$number) @include(if:$isIssue)"`
			Pull  F `graphql:"pullRequest(number:$number) @skip(if:$isIssue)"`
		} `graphql:"repository(owner:$owner,name:$name)"`
	}
	vars["cursor"] = cursor
	err := api.query(ctx, &q, vars)
	if isIssue {
		return connection(q.Repository.Issue), err
	}
	// The response field is pullRequest, which the graphql tag maps to Pull.
	return connection(q.Repository.Pull), err
}
