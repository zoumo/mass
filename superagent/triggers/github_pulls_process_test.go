package triggers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubPullsDiscussionsAndRegistration(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false, githubSource)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var phase atomic.Int32
	var pageFailed atomic.Bool
	var missingOpenReads atomic.Int32
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, q githubFixtureQuery) {
		repo := q.Variables.Owner + "/" + q.Variables.Name
		if q.Variables.ID != "" {
			repo = "demo/sdk"
		}
		step := phase.Load()
		alpha := repo == "demo/sdk"
		pull := githubFixturePull(repo, old, step)
		now := time.Now().UTC().Truncate(time.Second)
		prURL := githubObjectURL(repo, "pull", pull.Number)
		created := now
		if step >= 3 {
			created = now.Add(-time.Second)
		}
		conn := q.connection()
		switch conn {
		case "issues":
			githubFixtureRepo(w, map[string]any{conn: githubFixturePage([]githubIssue{{ID: 300, Number: 25, Title: "Existing Issue", State: "OPEN", CreatedAt: old, UpdatedAt: old}}, "")})
		case "pullRequests":
			pulls := []githubPull{pull}
			if alpha && step >= 3 || !alpha && step == 4 && len(q.Variables.States) == 1 {
				pulls = nil
			}
			if !alpha && step >= 6 {
				pulls = append([]githubPull{githubFixtureNewPull(pull)}, pulls...)
			}
			githubFixtureRepo(w, map[string]any{conn: githubFixturePage(pulls, "")})
		case "":
			if q.Variables.Number == 42 && alpha && step >= 3 {
				missingOpenReads.Add(1)
			}
			if q.Variables.Number == 43 {
				pull = githubFixtureNewPull(pull)
			}
			githubFixtureObject(w, q, pull)
		case "comments":
			if q.Variables.ID != "" {
				if q.Variables.ID != "inline-thread" {
					githubFixtureEmpty(w, q)
					return
				}
				if q.Variables.Cursor == "" {
					githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: githubFixturePage([]any{}, "inline-next")}})
					return
				}
				if q.Variables.Cursor != "inline-next" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				comment := githubFixtureComment(githubComment{ID: 102, Body: "Line reply", URL: prURL + "#discussion_r102", CreatedAt: created, UpdatedAt: now})
				comment["replyTo"] = map[string]any{"fullDatabaseId": "99"}
				comment["pullRequestReview"] = map[string]any{"fullDatabaseId": "103"}
				comment["commit"] = githubCommitOID{OID: pull.HeadRefOID}
				comment["path"], comment["line"] = "main.go", 42
				githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: githubFixturePage([]any{comment}, "")}})
				return
			}
			if !alpha || q.Variables.Number == 43 {
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.Number == 25 {
				comment := githubComment{ID: 100, Body: "Issue comment", URL: githubObjectURL(repo, "issues", 25) + "#issuecomment-100", CreatedAt: old, UpdatedAt: old}
				if step >= 2 {
					comment.Body, comment.UpdatedAt = "Historical Issue comment edited", now
				}
				githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]any{githubFixtureComment(comment)}, "")})
				return
			}
			if step < 2 {
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.Cursor == "" {
				githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]any{}, "pull-comment-next")})
				return
			}
			if q.Variables.Cursor != "pull-comment-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if pageFailed.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			body := "PR comment"
			if step >= 3 {
				body = "PR comment edited"
			}
			main := githubFixtureComment(githubComment{ID: 101, Body: body, URL: prURL + "#issuecomment-101", CreatedAt: created, UpdatedAt: now})
			githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]any{main}, "")})
		case "reviewThreads":
			if !alpha || step < 2 || q.Variables.Number == 43 {
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.Cursor == "" {
				githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]githubReviewThread{{ID: "empty-thread"}}, "thread-next")})
				return
			}
			if q.Variables.Cursor != "thread-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]githubReviewThread{{ID: "inline-thread"}}, "")})
		case "reviews":
			if !alpha || step < 2 || q.Variables.Number == 43 {
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.Cursor == "" {
				githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]any{}, "review-next")})
				return
			}
			if q.Variables.Cursor != "review-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			state := "APPROVED"
			if step >= 3 {
				state = "DISMISSED"
			}
			review := map[string]any{"fullDatabaseId": "103", "state": state, "submittedAt": created, "updatedAt": now, "body": "Review body", "url": prURL + "#pullrequestreview-103", "author": githubFixtureAuthor(), "commit": githubCommitOID{OID: pull.HeadRefOID}}
			githubFixtureObject(w, q, map[string]any{conn: githubFixturePage([]any{review}, "")})
		default:
			githubFixtureEmpty(w, q)
		}
	}))
	t.Cleanup(fixture.Close)
	cfg := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: f.endpoint,
		DatabasePath: filepath.Join(f.dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", PollSeconds: 1, RequestMillis: 2500,
		Repositories: []githubRepository{{"demo/sdk", "Example SDK"}, {"demo/mass", "Example application"}},
	}
	p, statusURL := startGitHubFixture(t, f.dir, fixture.URL, cfg)
	wait(t, "silent PR and discussion baselines", func() bool {
		status := readGitHubStatus(t, statusURL)
		for _, name := range []string{"demo/sdk", "demo/mass"} {
			for _, key := range []string{"pulls", "issue-comments/issues/25", "issue-comments/pull/42", "review-comments/42", "reviews/42"} {
				if status.Repositories[name].Streams[key].State != "ready" {
					return false
				}
			}
		}
		return true
	})
	if len(readMessages(t, f.endpoint)) != 0 {
		t.Fatalf("historical PRs generated a message: %s", p.text())
	}
	phase.Store(1)
	alpha := githubWaitObservation(t, f, "demo/sdk", "edited", "pull_request", "42")
	beta := githubWaitObservation(t, f, "demo/mass", "head_updated", "pull_request", "42")
	alphaURL := githubObjectURL("demo/sdk", "pull", 42)
	betaURL := githubObjectURL("demo/mass", "pull", 42)
	if alpha["threadKey"] != alphaURL || beta["threadKey"] != betaURL {
		t.Fatalf("same-number PR Threads collapsed: alpha=%v beta=%v", alpha, beta)
	}
	for _, message := range []map[string]any{alpha, beta} {
		f.assertRouted(message, 1)
	}
	fork := beta["meta"].(map[string]any)
	if fork["headRepository"] != "contributor/mass-fork" || fork["repository"] != "demo/mass" || !strings.Contains(fmt.Sprint(fork["changes"]), "converted_to_draft") {
		t.Fatalf("fork identity or simultaneous changes were lost: %+v", fork)
	}
	alphaTopic := githubCreateTopic(t, f, "demo/sdk", alpha["id"].(string))
	betaTopic := githubCreateTopic(t, f, "demo/mass", beta["id"].(string))
	githubRegisterFixturePR(t, f, alphaTopic, alphaURL)

	baseline := readGitHubStatus(t, statusURL).Repositories["demo/sdk"].Streams["issue-comments/pull/42"]
	pageFailed.Store(true)
	phaseTwoStart := time.Now()
	phase.Store(2)
	wait(t, "failed page leaves the prior discussion cursor", func() bool {
		return readGitHubStatus(t, statusURL).Repositories["demo/sdk"].Streams["issue-comments/pull/42"].State == "stale"
	})
	failed := readGitHubStatus(t, statusURL).Repositories["demo/sdk"].Streams["issue-comments/pull/42"]
	if failed.LastSuccessAt.Before(baseline.LastSuccessAt) || failed.LastSuccessAt.After(phaseTwoStart) || failed.Items != 0 || failed.LastError != "github_http_503" {
		t.Fatalf("partial read advanced prior discussion state: %+v; before %+v", failed, baseline)
	}
	line := githubWaitObservation(t, f, "demo/sdk", "comment_created", "review_comment", "102")
	review := githubWaitObservation(t, f, "demo/sdk", "review_submitted", "review", "103")
	pageFailed.Store(false)
	main := githubWaitObservation(t, f, "demo/sdk", "comment_created", "issue_comment", "101")
	issue := githubWaitObservation(t, f, "demo/sdk", "comment_edited", "issue_comment", "100")
	if issue["threadKey"] == main["threadKey"] || issue["threadRef"].(map[string]any)["id"] != githubObjectURL("demo/sdk", "issues", 25) {
		t.Fatalf("Issue and PR main discussion share a Thread: issue=%v main=%v", issue, main)
	}
	f.assertRouted(issue, 1)
	for _, message := range []map[string]any{line, review, main} {
		if message["threadKey"] != alphaURL || message["actor"] != "reviewer" || message["human"] != true || !strings.Contains(message["text"].(string), "Example SDK") {
			t.Fatalf("invalid PR discussion Message: %+v", message)
		}
		githubAssertTopicDelivery(t, f, message, alphaTopic)
	}
	if meta := line["meta"].(map[string]any); meta["inReplyToId"] != "99" || meta["inReplyToURL"] != alphaURL+"#discussion_r99" || meta["reviewId"] != "103" || line["url"] != alphaURL+"#discussion_r102" {
		t.Fatalf("line comment lost the native reply identity: %v", line)
	}
	githubRegisterFixturePR(t, f, betaTopic, alphaURL)

	phase.Store(3)
	merged := githubWaitObservation(t, f, "demo/sdk", "merged", "pull_request", "42")
	edited := githubWaitObservation(t, f, "demo/sdk", "comment_edited", "issue_comment", "101")
	dismissed := githubWaitObservation(t, f, "demo/sdk", "review_dismissed", "review", "103")
	if missingOpenReads.Load() == 0 {
		t.Fatal("the disappeared open PR was not read back")
	}
	for _, message := range []map[string]any{merged, edited, dismissed} {
		f.assertRouted(message, 1)
		view := githubRouterMessage(t, f, message["id"].(string))
		if view["threadKey"] != alphaURL || view["text"] != message["text"] {
			t.Fatalf("Router read lost the PR conversation: %v", view)
		}
	}
	phase.Store(4)
	_ = githubWaitObservation(t, f, "demo/mass", "closed", "pull_request", "42")
	phase.Store(5)
	_ = githubWaitObservation(t, f, "demo/mass", "reopened", "pull_request", "42")
	phase.Store(6)
	_ = githubWaitObservation(t, f, "demo/mass", "opened", "pull_request", "43")
	_ = githubWaitObservation(t, f, "demo/mass", "ready_for_review", "pull_request", "42")
}

func githubFixtureNewPull(previous githubPull) githubPull {
	previous.ID, previous.Number = 453, 43
	previous.Title = "New PR"
	previous.CreatedAt = time.Now().UTC().Truncate(time.Second)
	previous.UpdatedAt = previous.CreatedAt
	return previous
}

func githubFixturePull(repo string, old time.Time, step int32) githubPull {
	pull := githubPull{
		ID: 451, Number: 42, State: "OPEN", Title: "Original PR", CreatedAt: old, UpdatedAt: old,
		BaseRefOID: strings.Repeat("d", 40), BaseRefName: "main", HeadRefOID: strings.Repeat("a", 40),
		HeadRepository: &githubRepositoryName{NameWithOwner: repo}, Mergeable: "CONFLICTING",
	}
	if repo == "demo/mass" {
		pull.ID = 452
	}
	if step > 0 {
		pull.UpdatedAt = old.Add(time.Duration(step) * time.Second)
		if repo == "demo/sdk" {
			pull.Title = "Edited PR"
			if step >= 3 {
				pull.State = "MERGED"
			}
		} else {
			pull.HeadRepository.NameWithOwner = "contributor/mass-fork"
			pull.Draft, pull.HeadRefOID = step < 6, strings.Repeat("b", 40)
			if step == 4 {
				pull.State = "CLOSED"
			}
		}
	}
	return pull
}

func githubWaitObservation(t *testing.T, f *egressFixture, repo, observation, objectType, objectID string) map[string]any {
	t.Helper()
	var found map[string]any
	wait(t, fmt.Sprintf("%s %s %s %s", repo, observation, objectType, objectID), func() bool {
		for _, msg := range readMessages(t, f.endpoint) {
			meta, _ := msg["meta"].(map[string]any)
			id := meta["objectId"]
			if objectType == "pull_request" {
				id = fmt.Sprint(meta["number"])
			}
			if meta["repository"] == repo && meta["observation"] == observation && meta["objectType"] == objectType && id == objectID {
				found = msg
				return true
			}
		}
		return false
	})
	return found
}

func githubRegisterFixturePR(t *testing.T, f *egressFixture, topicID, prURL string) {
	t.Helper()
	token := f.waitRoleToken("topic", topicID)
	type topicView struct {
		ID           string
		Revision     int
		PullRequests []struct{ CanonicalURL, ThreadKey, TopicID string }
	}
	before := callRoleTool[topicView](t, f.endpoint, token, "get_topic", struct{}{})
	body := map[string]any{
		"operationId":      "register-" + topicID,
		"expectedRevision": before.Revision,
		"pullRequests":     map[string][]string{"add": {prURL}},
	}
	topic := callRoleTool[topicView](t, f.endpoint, token, "update_topic", body)
	if topic.ID != topicID || topic.Revision != before.Revision+1 || len(topic.PullRequests) != 1 {
		t.Fatalf("invalid Topic after PR registration: %+v", topic)
	}
	link := topic.PullRequests[0]
	if link.TopicID != topicID || link.CanonicalURL != prURL || link.ThreadKey != prURL {
		t.Fatalf("invalid PR registration: %+v", link)
	}
}

func githubAssertTopicDelivery(t *testing.T, f *egressFixture, message map[string]any, topic string) {
	t.Helper()
	events := f.routingEvents(message)
	if len(events) != 1 || events[0].Kind != "routed_message" || events[0].Recipient.TopicID != topic {
		t.Fatalf("expected Message %v routed to Topic %s: %+v", message["id"], topic, events)
	}
}
