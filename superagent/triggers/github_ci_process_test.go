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

func TestGitHubCITracksOpenPRRevisionsAndRecovery(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false, githubSource)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	headSHA := strings.Repeat("a", 40)
	mergeSHA := strings.Repeat("b", 40)
	newHeadSHA := strings.Repeat("c", 40)
	var phase atomic.Int32
	var blockedPage atomic.Bool
	var rateHits atomic.Int32
	var checkReads atomic.Int32
	var lastPage atomic.Int32
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, q githubFixtureQuery) {
		step := phase.Load()
		first := githubCIPullFixture(old, 42, step, headSHA, mergeSHA, newHeadSHA)
		second := githubCIPullFixture(old, 43, step, headSHA, mergeSHA, newHeadSHA)
		sha := q.Variables.OID
		conn := q.connection()
		respond := func(fields map[string]any) {
			fields["oid"] = sha
			githubFixtureRepo(w, map[string]any{"object": fields})
		}
		switch conn {
		case "pullRequests":
			pulls := []githubPull{first, second}
			if step >= 8 && len(q.Variables.States) == 1 {
				pulls = pulls[1:]
			}
			githubFixtureRepo(w, map[string]any{conn: githubFixturePage(pulls, "")})
		case "":
			if q.Variables.Number == 42 {
				githubFixtureObject(w, q, first)
			} else {
				githubFixtureObject(w, q, second)
			}
		case "contexts":
			contextPage := func(items []any, cursor string) {
				respond(map[string]any{"statusCheckRollup": map[string]any{conn: githubFixturePage(items, cursor)}})
			}
			if sha == newHeadSHA {
				checkReads.Add(1)
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.Cursor != "" {
				if q.Variables.Cursor != "next-context" || sha != headSHA {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				lastPage.Add(1)
				if blockedPage.Load() {
					rateHits.Add(1)
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				contextPage([]any{map[string]any{"__typename": "CheckRun", "databaseId": 2, "name": "Complete pagination", "status": "COMPLETED", "conclusion": "SKIPPED"}}, "")
				return
			}
			if sha == mergeSHA {
				contextPage([]any{
					map[string]any{"__typename": "StatusContext", "id": "24", "context": "External CI", "state": "PENDING", "updatedAt": old.Add(time.Minute), "targetUrl": "https://example.org/ci/24"},
					map[string]any{"__typename": "StatusContext", "id": "23", "context": "External CI", "state": "SUCCESS", "updatedAt": old},
				}, "")
				return
			}
			status, conclusion := "COMPLETED", "SUCCESS"
			switch step {
			case 1:
				status, conclusion = "IN_PROGRESS", ""
			case 2:
				conclusion = "FAILURE"
			case 4, 5:
				conclusion = "CANCELLED" //nolint:misspell // GitHub GraphQL enum value.
			}
			contextPage([]any{map[string]any{"__typename": "CheckRun", "databaseId": 1, "name": "Unit test", "status": status, "conclusion": conclusion, "permalink": "https://github.com/demo/sdk/runs/123"}}, "next-context")
		case "checkSuites":
			if sha != headSHA {
				githubFixtureEmpty(w, q)
				return
			}
			if step == 5 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			suite := func(id int, status, conclusion string, attempt int, association string) map[string]any {
				return map[string]any{
					"id": fmt.Sprintf("suite-%d", id), "status": status, "conclusion": conclusion,
					"workflowRun":          map[string]any{"databaseId": id, "runAttempt": attempt, "event": "pull_request", "workflow": map[string]any{"name": "Workflow"}, "url": fmt.Sprintf("https://github.com/demo/sdk/actions/runs/%d", id)},
					"matchingPullRequests": githubFixturePage([]githubWorkflowPull{}, association),
					"checkRuns":            githubFixturePage([]any{}, ""),
				}
			}
			if q.Variables.Cursor == "" {
				status, conclusion, attempt := "COMPLETED", "SUCCESS", 1
				if step >= 4 {
					status, conclusion, attempt = "WAITING", "", 2
				}
				respond(map[string]any{conn: githubFixturePage([]any{suite(60, status, conclusion, attempt, "association-next")}, "suite-next")})
				return
			}
			if q.Variables.Cursor != "suite-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			secondSuite := suite(61, "COMPLETED", "SUCCESS", 1, "")
			secondSuite["checkRuns"] = githubFixturePage([]any{map[string]any{"databaseId": 2, "name": "Complete pagination", "status": "COMPLETED", "conclusion": "SKIPPED"}}, "checks-next")
			ambiguous := suite(62, "COMPLETED", "SUCCESS", 1, "")
			ambiguous["matchingPullRequests"] = githubFixturePage([]githubWorkflowPull{
				{DatabaseID: 742, Number: 42, HeadRefOID: headSHA},
				{DatabaseID: 743, Number: 43, HeadRefOID: headSHA},
			}, "")
			respond(map[string]any{conn: githubFixturePage([]any{secondSuite, ambiguous}, "")})
		case "checkRuns":
			if q.Variables.ID != "suite-61" || q.Variables.Cursor != "checks-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			check := map[string]any{"databaseId": 3, "name": "Check missing from rollup", "status": "COMPLETED", "conclusion": "SUCCESS"}
			githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: githubFixturePage([]any{check}, "")}})
		case "matchingPullRequests":
			if q.Variables.ID != "suite-60" || q.Variables.Cursor != "association-next" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			page := githubFixturePage([]githubWorkflowPull{{DatabaseID: 742, Number: 42, HeadRefOID: headSHA}}, "")
			githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: page}})
		default:
			githubFixtureEmpty(w, q)
		}
	}))
	t.Cleanup(fixture.Close)
	cfg := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: f.endpoint,
		DatabasePath: filepath.Join(f.dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", PollSeconds: 1, RequestMillis: 2500,
		Repositories: []githubRepository{{"demo/sdk", "Example SDK CI"}},
	}
	p, statusURL := startGitHubFixture(t, f.dir, fixture.URL, cfg)
	ciStatus := func(number int) githubStreamStatus {
		return readGitHubStatus(t, statusURL).Repositories["demo/sdk"].Streams[fmt.Sprintf("ci/%d", number)]
	}
	wait(t, "silent CI baseline for both open PRs", func() bool { return ciStatus(42).State == "ready" && ciStatus(43).State == "ready" })
	initial := ciStatus(42).CI
	other := ciStatus(43).CI
	if initial == nil || len(initial.Head.Checks) != 3 || initial.Head.Checks[2].ID != "3" || len(initial.Head.Actions) != 1 || initial.Head.UnassociatedActions != 2 || initial.Merge == nil || initial.Merge.SHA != mergeSHA || len(initial.Merge.Statuses) != 1 || initial.Merge.Statuses[0].ID != "24" || other == nil || len(other.Head.Actions) != 0 || other.Head.UnassociatedActions != 2 || lastPage.Load() == 0 {
		t.Fatalf("unexpected group or attribution in CI baseline: first=%+v second=%+v", initial, other)
	}
	if len(readMessages(t, f.endpoint)) != 0 {
		t.Fatalf("the initial CI baseline generated messages: %s", p.text())
	}
	phase.Store(1)
	first := githubWaitCI(t, f, 42, 1)
	otherFirst := githubWaitCI(t, f, 43, 1)
	for _, message := range []map[string]any{first, otherFirst} {
		if !strings.Contains(message["text"].(string), "in_progress") || !strings.Contains(message["text"].(string), "Example SDK CI") || message["actor"] != "github-trigger" || message["human"] != false {
			t.Fatalf("invalid running CI message: %v", message)
		}
		f.assertRouted(message, 1)
		view := githubRouterMessage(t, f, message["id"].(string))
		if view["text"] != message["text"] {
			t.Fatalf("Router read changed CI text: %v", view)
		}
	}
	prURL := githubObjectURL("demo/sdk", "pull", 42)
	topic := githubCreateTopic(t, f, "SDK CI", first["id"].(string))
	githubRegisterFixturePR(t, f, topic, prURL)
	phase.Store(2)
	failure := githubWaitCI(t, f, 42, 2)
	_ = githubWaitCI(t, f, 43, 2)
	if !strings.Contains(failure["text"].(string), "failure") || failure["threadKey"] != prURL {
		t.Fatalf("invalid failed CI Message: %v", failure)
	}
	githubAssertTopicDelivery(t, f, failure, topic)
	phase.Store(3)
	recovery := githubWaitCI(t, f, 42, 3)
	_ = githubWaitCI(t, f, 43, 3)
	if !strings.Contains(recovery["text"].(string), "success") || recovery["sourceMessageId"] == failure["sourceMessageId"] {
		t.Fatalf("A→B→A did not generate a distinct recovery Message: %v", recovery)
	}
	githubAssertTopicDelivery(t, f, recovery, topic)

	blockedPage.Store(true)
	phase.Store(4)
	wait(t, "rate-limited second CI page", func() bool {
		return rateHits.Load() > 0 && readGitHubStatus(t, statusURL).RateLimitedUntil.After(time.Now())
	})
	prior := ciStatus(42)
	wait(t, "failed CI page is marked stale without replacing the snapshot", func() bool {
		return ciStatus(42).State == "stale" || ciStatus(43).State == "stale"
	})
	if prior.CI == nil || prior.CI.Head.Checks[0].Conclusion != "success" || githubCIMessage(t, f, 42, 4) != nil {
		t.Fatalf("incomplete pagination published a CI change: %+v", prior)
	}
	blockedPage.Store(false)
	rerun := githubWaitCI(t, f, 42, 4)
	_ = githubWaitCI(t, f, 43, 4)
	if text := rerun["text"].(string); !strings.Contains(text, "cancelled") || !strings.Contains(text, "waiting (attempt 2)") { //nolint:misspell // Rendered GitHub GraphQL enum value.
		t.Fatalf("rerun or waiting state unavailable: %v", rerun)
	}
	phase.Store(5)
	wait(t, "Actions permission gap retains the last complete snapshot", func() bool {
		return ciStatus(42).LastError == "github_http_403"
	})
	retained := ciStatus(42)
	if retained.State != "stale" || retained.CI == nil || len(retained.CI.Head.Actions) != 1 || retained.CI.Head.Actions[0].Attempt != 2 || githubCIMessage(t, f, 42, 5) != nil {
		t.Fatalf("Actions permission failure discarded or routed past evidence: %+v", retained)
	}
	phase.Store(7)
	updated := githubWaitCI(t, f, 42, 5)
	if text := updated["text"].(string); !strings.Contains(text, newHeadSHA) || !strings.Contains(text, "No Check Runs or Commit Statuses observed") || strings.Contains(text, headSHA) || strings.Contains(text, "Workflow Run Workflow") {
		t.Fatalf("new CI version mixed prior head results: %v", updated)
	}
	if snapshot := ciStatus(42).CI; snapshot.Head.SHA != newHeadSHA || snapshot.Merge != nil {
		t.Fatalf("new CI version retained the previous test merge: %v", snapshot)
	}
	phase.Store(8)
	_ = githubWaitObservation(t, f, "demo/sdk", "closed", "pull_request", "42")
	wait(t, "closed PR exits the open CI scheduler", func() bool { return ciStatus(42).State == "inactive" })
	closedReads := checkReads.Load()
	last := ciStatus(43).LastSuccessAt
	wait(t, "other open PR is sampled independently", func() bool { return ciStatus(43).LastSuccessAt.After(last) })
	if checkReads.Load() != closedReads {
		t.Fatal("closed PR continued to collect CI")
	}
}

func githubCIPullFixture(old time.Time, number int, phase int32, head, merge, updatedHead string) githubPull {
	pull := githubFixturePull("demo/sdk", old, 0)
	pull.ID, pull.Number = githubDatabaseID(number+700), number
	pull.HeadRefOID = head
	if number == 42 {
		pull.Mergeable = "MERGEABLE"
		pull.PotentialMergeCommit = &githubCommitOID{OID: merge}
	} else {
		pull.Mergeable = "CONFLICTING"
		pull.HeadRepository.NameWithOwner = "contributor/sdk"
	}
	if number == 42 && phase >= 7 {
		pull.HeadRefOID, pull.PotentialMergeCommit, pull.Mergeable = updatedHead, nil, "CONFLICTING"
		pull.UpdatedAt = old.Add(7 * time.Second)
		if phase >= 8 {
			pull.State = "CLOSED"
			pull.UpdatedAt = old.Add(8 * time.Second)
		}
	}
	return pull
}

func githubWaitCI(t *testing.T, f *egressFixture, number, version int) map[string]any {
	t.Helper()
	var found map[string]any
	wait(t, fmt.Sprintf("CI for PR %d observation %d", number, version), func() bool {
		found = githubCIMessage(t, f, number, version)
		return found != nil
	})
	return found
}

func githubCIMessage(t *testing.T, f *egressFixture, number, version int) map[string]any {
	t.Helper()
	for _, message := range readMessages(t, f.endpoint) {
		meta, _ := message["meta"].(map[string]any)
		if meta["observation"] == "ci_changed" && meta["number"] == float64(number) && meta["observationVersion"] == float64(version) {
			return message
		}
	}
	return nil
}
