package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
)

type githubAuthor struct {
	Login string
	Type  string `graphql:"__typename" json:"__typename"`
	User  struct {
		DatabaseID int64 `graphql:"databaseId" json:"databaseId"`
	} `graphql:"... on User" json:"-"`
}

type githubComment struct {
	ID        int64
	Body      string
	URL       string
	User      *githubAuthor
	CreatedAt time.Time
	UpdatedAt time.Time
	ReplyTo   int64
	ReviewID  int64
	CommitID  string
	Path      string
	Line      int
}

type githubDiscussionComment struct {
	FullDatabaseID githubDatabaseID `graphql:"fullDatabaseId" json:"fullDatabaseId"`
	Body           string
	URL            string
	Author         *githubAuthor
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (c githubDiscussionComment) comment() githubComment {
	return githubComment{ID: int64(c.FullDatabaseID), Body: c.Body, URL: c.URL, User: c.Author, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

type githubInlineComment struct {
	githubDiscussionComment
	ReplyTo *struct {
		FullDatabaseID githubDatabaseID `graphql:"fullDatabaseId"`
	}
	PullRequestReview *struct {
		FullDatabaseID githubDatabaseID `graphql:"fullDatabaseId"`
	}
	Commit *githubCommitOID
	Path   string
	Line   int
}

type githubReviewThread struct {
	ID string
}

func readGitHubMainComments(ctx context.Context, api *githubAPI, repo string, number int, isIssue bool) ([]githubComment, error) {
	variables := githubRepositoryVariables(repo)
	variables["number"] = githubv4.Int(number)
	nodes, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubDiscussionComment], error) {
		variables["cursor"] = cursor
		type parent struct {
			Comments githubConnection[githubDiscussionComment] `graphql:"comments(first:100,after:$cursor)"`
		}
		if isIssue {
			var q struct {
				Repository struct {
					Issue parent `graphql:"issue(number:$number)"`
				} `graphql:"repository(owner:$owner,name:$name)"`
			}
			err := api.query(ctx, &q, variables)
			return q.Repository.Issue.Comments, err
		}
		var q struct {
			Repository struct {
				PullRequest parent `graphql:"pullRequest(number:$number)"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		err := api.query(ctx, &q, variables)
		return q.Repository.PullRequest.Comments, err
	})
	if err != nil {
		return nil, err
	}
	var comments []githubComment
	for _, node := range nodes {
		comments = append(comments, node.comment())
	}
	return comments, nil
}

func readGitHubInlineComments(ctx context.Context, api *githubAPI, repo string, number int) ([]githubComment, error) {
	variables := githubRepositoryVariables(repo)
	variables["number"] = githubv4.Int(number)
	threads, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubReviewThread], error) {
		variables["cursor"] = cursor
		var q struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads githubConnection[githubReviewThread] `graphql:"reviewThreads(first:100,after:$cursor)"`
				} `graphql:"pullRequest(number:$number)"`
			} `graphql:"repository(owner:$owner,name:$name)"`
		}
		err := api.query(ctx, &q, variables)
		return q.Repository.PullRequest.ReviewThreads, err
	})
	if err != nil {
		return nil, err
	}
	var comments []githubComment
	seen := make(map[string]bool)
	for _, thread := range threads {
		if thread.ID == "" {
			return nil, errors.New("github_invalid_review_thread")
		}
		if seen[thread.ID] {
			continue
		}
		seen[thread.ID] = true
		nodes, err := githubReadConnection(func(cursor *githubv4.String) (githubConnection[githubInlineComment], error) {
			var q struct {
				Node struct {
					Thread struct {
						Comments githubConnection[githubInlineComment] `graphql:"comments(first:100,after:$cursor)"`
					} `graphql:"... on PullRequestReviewThread"`
				} `graphql:"node(id:$id)"`
			}
			err := api.query(ctx, &q, map[string]any{"id": thread.ID, "cursor": cursor})
			return q.Node.Thread.Comments, err
		})
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			comment := node.comment()
			if node.ReplyTo != nil {
				comment.ReplyTo = int64(node.ReplyTo.FullDatabaseID)
			}
			if node.PullRequestReview != nil {
				comment.ReviewID = int64(node.PullRequestReview.FullDatabaseID)
			}
			if node.Commit != nil {
				comment.CommitID = node.Commit.OID
			}
			comment.Path, comment.Line = node.Path, node.Line
			comments = append(comments, comment)
		}
	}
	return comments, nil
}

func githubCommentObservations(repo string, expectedNumber int, scope *githubStreamState, objectType string, comments []githubComment) ([]githubObservation, error) {
	byID := make(map[int64]githubComment, len(comments))
	for _, comment := range comments {
		if comment.ID <= 0 || comment.CreatedAt.IsZero() || comment.UpdatedAt.Before(comment.CreatedAt) {
			return nil, errors.New("github_invalid_comment")
		}
		if old, ok := byID[comment.ID]; !ok || !comment.UpdatedAt.Before(old.UpdatedAt) {
			byID[comment.ID] = comment
		}
	}
	var observations []githubObservation
	for id, comment := range byID {
		nativeID := strconv.FormatInt(id, 10)
		key := objectType + "/" + nativeID
		previous, known := scope.Items[key]
		if known && comment.UpdatedAt.Before(previous.UpdatedAt) {
			continue
		}
		number, thread, err := githubDiscussionThread(comment.URL, repo, objectType, id)
		if err != nil {
			return nil, err
		}
		if number != expectedNumber {
			return nil, errors.New("github_invalid_discussion_number")
		}
		data := githubBodyDigest(comment.Body)
		if known && string(data) == string(previous.Data) && comment.UpdatedAt.Equal(previous.UpdatedAt) {
			continue
		}
		kind := ""
		if !known && !comment.CreatedAt.Before(scope.EnabledAt) {
			kind = "comment_created"
		} else if !known && !comment.UpdatedAt.Before(scope.EnabledAt) {
			kind = "comment_edited"
		} else if known && string(data) != string(previous.Data) {
			kind = "comment_edited"
		}
		meta := map[string]any{"objectType": objectType, "objectId": nativeID, "number": number}
		if comment.ReplyTo > 0 {
			meta["inReplyToId"] = strconv.FormatInt(comment.ReplyTo, 10)
			meta["inReplyToURL"] = thread.ID + "#discussion_r" + strconv.FormatInt(comment.ReplyTo, 10)
		}
		if comment.ReviewID > 0 {
			meta["reviewId"] = strconv.FormatInt(comment.ReviewID, 10)
		}
		if comment.CommitID != "" {
			meta["commitSha"] = comment.CommitID
		}
		if comment.Path != "" {
			meta["path"] = comment.Path
			meta["line"] = comment.Line
		}
		actor, human := githubDiscussionAuthor(comment.User, meta)
		observations = append(observations, githubObservation{
			Key: key, Data: data, Kind: kind, UpdatedAt: comment.UpdatedAt, At: comment.UpdatedAt, Actor: actor, Human: human,
			URL: comment.URL, Thread: thread, Title: fmt.Sprintf("GitHub %s %s#%d %s\n%s", objectType, repo, number, kind, comment.URL),
			Body: comment.Body, Meta: meta,
		})
	}
	githubSortObservations(observations)
	return observations, nil
}

func githubBodyDigest(body string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body))))
}

func githubDiscussionThread(raw, repo, kind string, nativeID int64) (int, coreclient.ThreadRef, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return 0, coreclient.ThreadRef{}, errors.New("github_invalid_discussion_url")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0]+"/"+parts[1], repo) {
		return 0, coreclient.ThreadRef{}, errors.New("github_invalid_discussion_repository")
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return 0, coreclient.ThreadRef{}, errors.New("github_invalid_discussion_number")
	}
	suffix := strconv.FormatInt(nativeID, 10)
	if parts[2] == "pull" && (kind == "issue_comment" && u.Fragment == "issuecomment-"+suffix || kind == "review_comment" && u.Fragment == "discussion_r"+suffix || kind == "review" && u.Fragment == "pullrequestreview-"+suffix) {
		return number, coreclient.ThreadRef{Source: githubPRThreadSource, ID: githubObjectURL(repo, "pull", number)}, nil
	}
	if parts[2] == "issues" && kind == "issue_comment" && u.Fragment == "issuecomment-"+suffix {
		return number, coreclient.ThreadRef{Source: githubSource, ID: githubObjectURL(repo, "issues", number)}, nil
	}
	return 0, coreclient.ThreadRef{}, errors.New("github_invalid_discussion_thread")
}

func githubDiscussionAuthor(author *githubAuthor, meta map[string]any) (string, bool) {
	if author == nil || author.Login == "" {
		return "github-unknown-author", false
	}
	meta["authorLogin"] = author.Login
	if author.User.DatabaseID > 0 {
		meta["authorId"] = strconv.FormatInt(author.User.DatabaseID, 10)
	}
	return author.Login, author.Type == "User"
}
