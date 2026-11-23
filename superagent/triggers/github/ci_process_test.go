package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

func TestGitHubDiscoveredPullTracksActionsAndStopsOnClosure(t *testing.T) {
	t.Parallel()
	f := newGitHubCoreFixture(t)
	const repo = "demo/sdk"
	old := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	recent := time.Now().UTC().Truncate(time.Second).Add(5 * time.Second)
	head, merge := strings.Repeat("a", 40), strings.Repeat("e", 40)
	prURL := githubObjectURL(repo, "pull", 42)
	var notices, pullState, actionState atomic.Int32
	var unavailable atomic.Bool
	var suiteReads, neverDiscovered, mergeReads, assocPages atomic.Int32
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		items := []githubNotification{githubFixtureNotice(300, 43, repo, "PullRequest", "mention", old)}
		switch notices.Load() {
		case 1:
			items = append(items,
				githubFixtureNotice(301, 42, repo, "PullRequest", "comment", recent),
				githubFixtureNotice(302, 44, repo, "PullRequest", "mention", recent))
		case 2:
			items = append(items, githubFixtureNotice(301, 42, repo, "PullRequest", "state_change", recent.Add(time.Second)))
		}
		githubFixtureJSON(w, items)
	}, func(w http.ResponseWriter, q githubFixtureQuery) {
		if q.Variables.Number != 0 && q.Variables.Number != 42 && q.Variables.Number != 44 {
			neverDiscovered.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch q.connection() {
		case "":
			pull := githubFixturePull("fork-owner/sdk", q.Variables.Number, old)
			pull.Mergeable = "MERGEABLE"
			pull.PotentialMergeCommit = &githubCommitOID{OID: merge}
			if q.Variables.Number == 44 {
				pull.State = "CLOSED"
			} else {
				if unavailable.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				switch pullState.Load() {
				case 1:
					pull.State, pull.UpdatedAt = "MERGED", recent.Add(time.Second)
				case 2:
					pull.State, pull.UpdatedAt = "OPEN", recent.Add(2*time.Second)
				case 3:
					pull.State, pull.UpdatedAt = "CLOSED", recent.Add(3*time.Second)
				}
			}
			githubFixtureObject(w, q, pull)
		case "checkSuites":
			if q.repository() != repo {
				t.Errorf("Actions were queried outside the base repository: %s", q.repository())
			}
			suiteReads.Add(1)
			if q.Variables.OID == merge {
				mergeReads.Add(1)
				githubFixtureEmpty(w, q)
				return
			}
			if q.Variables.OID != head {
				t.Errorf("queried an unrelated SHA: %s", q.Variables.OID)
			}
			status, conclusion, attempt := "IN_PROGRESS", "", 1
			switch actionState.Load() {
			case 1:
				status, conclusion = "COMPLETED", "FAILURE"
			case 2:
				status, attempt = "QUEUED", 2
			case 3:
				status, conclusion, attempt = "COMPLETED", "SUCCESS", 2
			case 4:
				status, conclusion, attempt = "COMPLETED", "NEUTRAL", 2
			case 5:
				status, conclusion, attempt = "COMPLETED", "SUCCESS", 2
			}
			existing := githubFixtureSuite(80, "COMPLETED", "SUCCESS", 1, githubFixturePage([]any{githubFixtureAssociation(42, head)}, ""))
			observed := githubFixtureSuite(90, status, conclusion, attempt, githubFixturePage([]any{}, "pull-associations"))
			unlinked := githubFixtureSuite(95, "COMPLETED", "FAILURE", 1, githubFixturePage([]any{githubFixtureAssociation(43, head)}, ""))
			githubFixtureRepo(w, map[string]any{
				"object": map[string]any{"oid": head, "checkSuites": githubFixturePage([]any{existing, observed, unlinked}, "")},
			})
		case "matchingPullRequests":
			if q.Variables.Cursor != "pull-associations" || q.Variables.ID != "suite-90" {
				t.Errorf("wrong Actions association page: %+v", q.Variables)
			}
			assocPages.Add(1)
			githubFixtureRespond(w, map[string]any{"node": map[string]any{"matchingPullRequests": githubFixturePage([]any{githubFixtureAssociation(42, head)}, "")}})
		default:
			githubFixtureEmpty(w, q)
		}
	}))
	t.Cleanup(fixture.Close)
	var corePRReads atomic.Int32
	coreProxy := f.Proxy(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "pull") {
			corePRReads.Add(1)
		}
		return false
	}, nil)
	c := githubTestConfig(f)
	c.CoreEndpoint = coreProxy.URL
	proc, endpoint := startGitHubFixture(t, fixture.URL, c)
	status := func() githubRepositoryStatus { return readGitHubStatus(t, endpoint).Repositories[repo] }
	tracked := func() githubStreamStatus { return status().Streams["tracked/42"] }
	triggertest.Wait(t, "historical PR notice baseline", func() bool {
		return readGitHubStatus(t, endpoint).Notifications.State == "ready"
	})
	require.Zero(t, suiteReads.Load())
	notices.Store(1)
	triggertest.Wait(t, "first Actions snapshots and both PR notification Messages", func() bool {
		return tracked().State == "ready" && tracked().CI != nil && status().Accepted == 2
	})
	discovered := githubMessage(t, f, repo, "notification_received", "742")
	require.NotEmpty(t, discovered)
	require.NotEmpty(t, githubMessage(t, f, repo, "notification_received", "744"))
	require.NotContains(t, status().Streams, "tracked/43")
	require.NotContains(t, status().Streams, "tracked/44")
	require.Equal(t, prURL, discovered["threadKey"])
	require.Equal(t, merge, tracked().TestMergeSHA)
	require.Positive(t, mergeReads.Load())
	require.Positive(t, assocPages.Load())
	require.Len(t, tracked().CI.Head.Actions, 2)
	topic := githubCreateTopic(t, f, discovered)
	githubRegisterFixturePR(t, f, topic, prURL)
	githubRegisterFixturePR(t, f, topic, githubObjectURL(repo, "pull", 45))
	notices.Store(0)
	actionState.Store(1)
	triggertest.Wait(t, "first attempt completion", func() bool { return status().Accepted == 3 })
	failure := githubActionMessage(t, f, "failure")
	require.Equal(t, prURL, failure["threadKey"])
	require.Contains(t, githubFixtureMessageText(t, failure), "Commit "+head)
	githubAssertMessageLinks(t, failure, prURL, "https://github.com/demo/sdk/actions/runs/90")
	githubAssertTopicDelivery(t, f, failure, topic)
	triggertest.Terminate(t, proc)

	actionState.Store(2)
	proc, endpoint = startGitHubFixture(t, fixture.URL, c)
	triggertest.Wait(t, "restarted tracker samples the same head and queued rerun", func() bool {
		ci := tracked().CI
		return ci != nil && ci.Head.SHA == head && ci.Head.Actions[1].Attempt == 2 && ci.Head.Actions[1].Status == "queued"
	})
	require.Equal(t, uint64(3), status().Accepted)
	actionState.Store(3)
	triggertest.Wait(t, "rerun success", func() bool { return status().Accepted == 4 })
	success := githubActionMessage(t, f, "success")
	require.NotEmpty(t, success)
	require.NotEqual(t, success["id"], failure["id"])
	githubAssertTopicDelivery(t, f, success, topic)
	actionState.Store(4)
	triggertest.Wait(t, "conclusion changes on the same run and attempt", func() bool { return status().Accepted == 5 })
	require.NotEmpty(t, githubActionMessage(t, f, "neutral"))
	actionState.Store(5)
	seen := tracked().LastSuccessAt
	triggertest.Wait(t, "known run, attempt, and conclusion do not replay", func() bool {
		ci := tracked()
		return ci.LastSuccessAt.After(seen) && ci.CI.Head.Actions[1].Conclusion == "success"
	})
	require.Equal(t, uint64(5), status().Accepted)

	unavailable.Store(true)
	triggertest.Wait(t, "readback failure preserves the tracked snapshot", func() bool {
		s := tracked()
		return s.LastError == "github_http_503" && s.CI != nil && s.CI.Head.Actions[1].Conclusion == "success"
	})
	pullState.Store(1)
	unavailable.Store(false)
	triggertest.Wait(t, "merged PR is removed without a merge notification", func() bool { return tracked().State == "inactive" })
	closedReads := suiteReads.Load()
	last := readGitHubStatus(t, endpoint).Notifications.LastSuccessAt
	pullState.Store(2)
	triggertest.Wait(t, "reopened PR waits for a qualifying notification", func() bool {
		return readGitHubStatus(t, endpoint).Notifications.LastSuccessAt.After(last)
	})
	require.Equal(t, closedReads, suiteReads.Load())
	notices.Store(2)
	triggertest.Wait(t, "updated notification resumes tracking the reopened PR", func() bool {
		return tracked().State == "ready" && suiteReads.Load() > closedReads && status().Accepted == 6
	})
	notices.Store(0)
	pullState.Store(3)
	triggertest.Wait(t, "closed PR leaves tracked Actions polling", func() bool { return tracked().State == "inactive" })
	closedReads = suiteReads.Load()
	last = readGitHubStatus(t, endpoint).Notifications.LastSuccessAt
	triggertest.Wait(t, "closed PR remains quiet", func() bool { return readGitHubStatus(t, endpoint).Notifications.LastSuccessAt.After(last) })
	require.Equal(t, closedReads, suiteReads.Load())
	require.Zero(t, neverDiscovered.Load())
	require.Zero(t, corePRReads.Load())
	require.Len(t, triggertest.ReadMessages(t, f.Endpoint), 6)
	triggertest.Terminate(t, proc)
}

func githubFixtureAssociation(number int, sha string) map[string]any {
	return map[string]any{"number": number, "fullDatabaseId": strconv.Itoa(700 + number), "headRefOid": sha}
}

func githubFixtureSuite(id int, status, conclusion string, attempt int, associations map[string]any) map[string]any {
	return map[string]any{
		"id": fmt.Sprintf("suite-%d", id), "status": status, "conclusion": conclusion,
		"matchingPullRequests": associations,
		"workflowRun": map[string]any{
			"id": fmt.Sprintf("workflow-run-%d", id), "displayTitle": "CI", "event": "pull_request", "runAttempt": attempt,
			"url": fmt.Sprintf("https://github.com/demo/sdk/actions/runs/%d", id),
		},
	}
}

func githubActionMessage(t *testing.T, f *triggertest.CoreFixture, conclusion string) map[string]any {
	t.Helper()
	for _, m := range triggertest.ReadMessages(t, f.Endpoint) {
		meta := githubMessageMeta(m)
		run, _ := meta["workflowRun"].(map[string]any)
		if meta["observation"] == "actions_completed" && run["id"] == "workflow-run-90" && run["conclusion"] == conclusion {
			return m
		}
	}
	return nil
}
