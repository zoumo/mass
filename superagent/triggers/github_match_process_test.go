package triggers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"go.yaml.in/yaml/v3"
)

func TestGitHubMatchConfiguration(t *testing.T) {
	for _, rule := range []string{
		"match: {}", "match: null", "match: {labels: []}", "match: {labels: [null]}",
		"match: {labels: ['  ']}", "match: {reviewers: [alice]}", "match: {labels: on-call}",
		"match: {authors: [123]}", "match: {labels: [one], labels: [two]}",
	} {
		t.Run(rule, func(t *testing.T) {
			var cfg githubConfig
			if err := yaml.Unmarshal([]byte(rule), &cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := readGitHubMatch(cfg.MatchYAML); err == nil {
				t.Fatal("accepted invalid match")
			}
		})
	}
	var cfg githubConfig
	err := yaml.Unmarshal([]byte("match: {labels: [Ready, ready], assignees: [Alice], authors: [Owner]}"), &cfg)
	match, matchErr := readGitHubMatch(cfg.MatchYAML)
	if err != nil || matchErr != nil || !slices.Equal(match.Labels, []string{"ready"}) ||
		!slices.Equal(match.Assignees, []string{"alice"}) || !slices.Equal(match.Authors, []string{"owner"}) {
		t.Fatalf("match normalization failed: %+v %v %v", match, err, matchErr)
	}
}

func TestGitHubMatchScopesAndTransitions(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false, githubSource)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var phase atomic.Int32
	var downstreamUnmatched, matchedCI, labelPages, assigneePages atomic.Int32
	var blocked atomic.Bool
	proxy := f.proxy(func(w http.ResponseWriter, r *http.Request) bool {
		if blocked.Load() && r.Method == http.MethodPost && r.URL.Path == "/v1/channel/messages" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}, nil)
	api := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, q githubFixtureQuery) {
		step := phase.Load()
		name := q.Variables.Owner + "/" + q.Variables.Name
		at := old
		if step > 0 {
			at = time.Now().UTC().Truncate(time.Second)
		}
		fields := func(n int) githubMatchFields {
			labels := []githubLabelName{}
			people := []githubLogin{}
			var author *githubLogin
			switch n % 10 {
			case 1:
				if step < 2 || step >= 3 {
					labels = append(labels, githubLabelName{"Ready"})
				}
			case 2:
				if step < 2 || step == 3 {
					people = append(people, githubLogin{"Alice"})
				} else if step == 2 {
					people = append(people, githubLogin{"UnmatchedUser"})
				}
			case 3:
				author = &githubLogin{"Owner"}
			case 4:
				author = &githubLogin{"Other"}
			}
			out := githubMatchFields{
				Author: author, Labels: &githubConnection[githubLabelName]{Nodes: labels},
				Assignees: &githubConnection[githubLogin]{Nodes: people},
			}
			hasMoreLabels := len(labels) > 0
			hasMorePeople := len(people) > 0 && people[0].Login == "Alice"
			out.Labels.PageInfo.HasNextPage = &hasMoreLabels
			out.Assignees.PageInfo.HasNextPage = &hasMorePeople
			if hasMoreLabels {
				out.Labels.Nodes = []githubLabelName{{"unrelated"}}
				cursor := githubv4.String("more-labels")
				out.Labels.PageInfo.EndCursor = &cursor
			}
			if hasMorePeople {
				out.Assignees.Nodes = []githubLogin{{"someone-else"}}
				cursor := githubv4.String("more-assignees")
				out.Assignees.PageInfo.EndCursor = &cursor
			}
			return out
		}
		switch q.connection() {
		case "issues":
			issues := []githubIssue{}
			for n := 1; n <= 4; n++ {
				issues = append(issues, githubIssue{
					ID: githubDatabaseID(n), Number: n, Title: "Issue", Body: fmt.Sprintf("revision %d", step),
					State: "OPEN", CreatedAt: old, UpdatedAt: at, githubMatchFields: fields(n),
				})
			}
			githubFixtureRepo(w, map[string]any{"issues": githubFixturePage(issues, "")})
		case "pullRequests":
			pulls := []githubPull{}
			for n := 11; n <= 14; n++ {
				p := githubFixturePull(name, old, 0)
				p.Number, p.ID, p.UpdatedAt, p.Body = n, githubDatabaseID(n), at, fmt.Sprintf("revision %d", step)
				p.HeadRefOID = fmt.Sprintf("%040d", n)
				p.githubMatchFields = fields(n)
				if (n == 11 || n == 13) && step >= 4 {
					p.State = "MERGED"
					if n == 11 {
						p.State = "CLOSED"
					}
					if len(q.Variables.States) == 1 {
						continue
					}
				}
				pulls = append(pulls, p)
			}
			githubFixtureRepo(w, map[string]any{"pullRequests": githubFixturePage(pulls, "")})
		case "comments", "reviews", "reviewThreads":
			n := q.Variables.Number
			if n%10 == 4 && step < 6 {
				downstreamUnmatched.Add(1)
			}
			if q.connection() == "reviews" && n == 14 {
				// This PR's reviewer matches the configured login, but its creator
				// and current assignees do not match.
				review := githubReview{
					ID: 900, URL: githubObjectURL(name, "pull", 14) + "#pullrequestreview-900",
					State: "APPROVED", Author: &githubAuthor{Login: "owner"}, SubmittedAt: old,
				}
				githubFixtureObject(w, q, map[string]any{"reviews": githubFixturePage([]githubReview{review}, "")})
				return
			}
			if q.connection() == "comments" && n == 1 {
				comment := githubComment{ID: 100, Body: fmt.Sprintf("comment revision %d", step), URL: githubObjectURL(name, "issues", 1) + "#issuecomment-100", CreatedAt: old, UpdatedAt: at}
				githubFixtureObject(w, q, map[string]any{"comments": githubFixturePage([]any{githubFixtureComment(comment)}, "")})
				return
			}
			githubFixtureEmpty(w, q)
		case "contexts", "checkSuites":
			if strings.HasSuffix(q.Variables.OID, "14") && step < 6 {
				downstreamUnmatched.Add(1)
			} else if strings.HasSuffix(q.Variables.OID, "13") {
				matchedCI.Add(1)
			}
			githubFixtureEmpty(w, q)
		default:
			object := "pullRequest"
			if q.Variables.IsIssue {
				object = "issue"
			}
			switch q.Variables.Cursor {
			case "more-labels":
				labelPages.Add(1)
				githubFixtureRepo(w, map[string]any{object: map[string]any{"labels": githubFixturePage([]githubLabelName{{"READY"}}, "")}})
			case "more-assignees":
				assigneePages.Add(1)
				githubFixtureRepo(w, map[string]any{object: map[string]any{"assignees": githubFixturePage([]githubLogin{{"ALICE"}}, "")}})
			default:
				t.Errorf("unexpected GitHub query: %s", q.Query)
				w.WriteHeader(http.StatusBadRequest)
			}
		}
	}))
	defer api.Close()
	c := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: proxy.URL,
		DatabasePath: filepath.Join(f.dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", PollSeconds: 1,
		Repositories: []githubRepository{{"demo/sdk", "sdk-prompt"}, {"demo/mass", "mass-prompt"}},
	}
	setRule := func(in string) {
		t.Helper()
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(in), &node); err != nil {
			t.Fatal(err)
		}
		c.MatchYAML = *node.Content[0]
	}
	setRule("{labels: [ready], assignees: [alice], authors: [owner]}")
	p, endpoint := startGitHubFixture(t, f.dir, api.URL, c)
	ready := func(scope string) bool {
		status := readGitHubStatus(t, endpoint)
		for _, repo := range c.Repositories {
			if status.Repositories[repo.Repository].Streams[scope].State != "ready" {
				return false
			}
		}
		return true
	}
	wait(t, "both repositories baseline issues and PRs", func() bool {
		return ready("issues") && ready(githubPullsScope) && ready(githubIssueCommentsPrefix+"1") && matchedCI.Load() > 0
	})
	for _, repo := range c.Repositories {
		streams := readGitHubStatus(t, endpoint).Repositories[repo.Repository].Streams
		for _, n := range []int{1, 2, 3, 11, 12, 13} {
			prefix := githubIssueCommentsPrefix
			if n > 10 {
				prefix = githubPullCommentsPrefix
			}
			if streams[prefix+strconv.Itoa(n)].State == "" {
				t.Fatalf("missing scope %s #%d: %v", repo.Repository, n, streams)
			}
		}
	}
	if len(readMessages(t, f.endpoint)) != 0 || labelPages.Load() < 4 || assigneePages.Load() < 4 {
		t.Fatal("expected silent history and paginated label and assignee matches")
	}

	assertMessages := func(step, n, version int, kind string) bool {
		t.Helper()
		count := 0
		for _, item := range readMessages(t, f.endpoint) {
			meta := item["meta"].(map[string]any)
			if meta["number"] == float64(n) && meta["observation"] == kind &&
				meta["observationVersion"] == float64(version) &&
				strings.Contains(item["text"].(string), fmt.Sprintf("revision %d", step)) {
				count++
			}
		}
		return count == 2
	}
	phase.Store(1)
	wait(t, "all matched parent edits and tracked comment edits arrive", func() bool {
		for _, n := range []int{1, 2, 3, 11, 12, 13} {
			if !assertMessages(1, n, 1, "edited") {
				return false
			}
		}
		return assertMessages(1, 1, 1, "comment_edited")
	})
	phase.Store(2)
	wait(t, "label removal and reassignment disable child scopes", func() bool {
		for _, repo := range readGitHubStatus(t, endpoint).Repositories {
			for _, scope := range []string{githubIssueCommentsPrefix + "1", githubIssueCommentsPrefix + "2", githubReviewsPrefix + "11", githubCIPrefix + "12"} {
				if repo.Streams[scope].State != "inactive" {
					return false
				}
			}
		}
		return true
	})
	phase.Store(3)
	wait(t, "restored label and assignee silently baseline historical changes", func() bool {
		return ready(githubIssueCommentsPrefix+"1") && ready(githubIssueCommentsPrefix+"2") && ready(githubCIPrefix+"12")
	})
	phase.Store(4)
	wait(t, "reentered object resumes versioning, and matched close and merge report", func() bool {
		return assertMessages(4, 1, 2, "edited") && assertMessages(4, 1, 2, "comment_edited") &&
			assertMessages(4, 11, 2, "closed") && assertMessages(4, 13, 4, "merged")
	})
	for _, item := range readMessages(t, f.endpoint) {
		meta := item["meta"].(map[string]any)
		n := int(meta["number"].(float64))
		if n%10 == 4 || n%10 < 3 && (strings.Contains(item["text"].(string), "revision 2") ||
			strings.Contains(item["text"].(string), "revision 3")) {
			t.Fatalf("unexpected historical or unmatched message: %v", item)
		}
	}
	blocked.Store(true)
	phase.Store(5)
	wait(t, "both repositories have queued messages before a rule change", func() bool {
		for _, repo := range readGitHubStatus(t, endpoint).Repositories {
			if repo.Backlog == 0 || repo.LastDeliveryError == "" {
				return false
			}
		}
		return true
	})
	terminate(t, p)
	if downstreamUnmatched.Load() != 0 {
		t.Fatalf("requested downstream detail for excluded objects %d times", downstreamUnmatched.Load())
	}
	phase.Store(6)
	blocked.Store(false)
	setRule("{authors: [other]}")
	c.Repositories[0].Prompt = "new-sdk-prompt"
	p, endpoint = startGitHubFixture(t, f.dir, api.URL, c)
	wait(t, "changed rule reconciles both repos and starts new scopes", func() bool {
		return ready(githubIssueCommentsPrefix+"4") && ready(githubCIPrefix+"14")
	})
	for _, r := range readGitHubStatus(t, endpoint).Repositories {
		if r.Streams[githubIssueCommentsPrefix+"1"].State != "inactive" || r.Streams[githubPullCommentsPrefix+"13"].State != "inactive" {
			t.Fatalf("obsolete rule left old scopes active: %v", r.Streams)
		}
	}
	wait(t, "queued event retains its old prompt after rule change", func() bool {
		for _, item := range readMessages(t, f.endpoint) {
			meta := item["meta"].(map[string]any)
			text := item["text"].(string)
			if meta["repository"] == "demo/sdk" && strings.Contains(text, "revision 5") &&
				strings.Contains(text, "\nsdk-prompt\n") {
				return true
			}
		}
		return false
	})
	for _, item := range readMessages(t, f.endpoint) {
		meta := item["meta"].(map[string]any)
		if int(meta["number"].(float64))%10 == 4 {
			t.Fatalf("rule change did not baseline silently: %v", item)
		}
	}
	terminate(t, p)
}
