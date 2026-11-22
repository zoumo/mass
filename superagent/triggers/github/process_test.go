package github

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

func TestGitHubNotificationsRecoverPagesReadbacksAndCoreReceipts(t *testing.T) {
	t.Parallel()
	f := triggertest.NewCoreFixture(t, false, githubSource)
	const sdk, mass = "demo/sdk", "elsewhere/mass"
	old := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	fresh := old.Add(65 * time.Second)
	var phase atomic.Int32
	var sdkDown, loseReceipt atomic.Bool
	sdkDown.Store(true)
	loseReceipt.Store(true)
	var reads, pages, throttles atomic.Int32
	var cursorMu sync.Mutex
	var pageOneSince string
	var fixture *httptest.Server
	fixture = httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "50" || q.Get("since") == "" {
			t.Error("Notifications must use a bounded, overlapping poll")
		}
		if phase.Load() == 0 {
			w.Header().Set("X-Poll-Interval", "2")
			historic := githubFixtureNotice(100, 42, sdk, "Issue", "mention", old)
			githubFixtureJSON(w, []githubNotification{historic})
			return
		}
		if q.Get("page") == "1" {
			if throttles.Load() == 0 {
				cursorMu.Lock()
				pageOneSince = q.Get("since")
				cursorMu.Unlock()
			}
			next := fixture.URL + "/notifications?page=2"
			w.Header().Set("Link", "<"+next+`>; rel="next"`)
			n := githubFixtureNotice(100, 42, "DEMO/SDK", "Issue", "mention", fresh)
			githubFixtureJSON(w, []githubNotification{n, n})
			return
		}
		pages.Add(1)
		if phase.Load() == 1 {
			throttles.Add(1)
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		githubFixtureJSON(w, []githubNotification{githubFixtureNotice(200, 42, mass, "Issue", "subscribed", fresh)})
	}, func(w http.ResponseWriter, q githubFixtureQuery) {
		reads.Add(1)
		repo := q.repository()
		if q.Variables.Number != 42 || (repo != sdk && repo != mass) {
			t.Errorf("read unexpected GitHub object %+v", q.Variables)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch q.connection() {
		case "":
			if repo == sdk && sdkDown.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			githubFixtureObject(w, q, githubFixtureIssue(42, 42, old))
		case "comments":
			objectURL := githubObjectURL(repo, "issues", 42)
			comments := []map[string]any{
				githubFixtureComment(501, objectURL+"#issuecomment-501", "historical discussion", "User", old),
			}
			if repo == sdk {
				comments = append(comments,
					githubFixtureComment(502, objectURL+"#issuecomment-502", "new SDK mention", "User", fresh.Add(-time.Second)),
					githubFixtureComment(503, objectURL+"#issuecomment-503", "another aggregated SDK comment", "Bot", fresh))
			}
			githubFixtureObject(w, q, map[string]any{"comments": githubFixturePage(comments, "")})
		default:
			t.Errorf("unexpected readback %s", q.Query)
		}
	}))
	t.Cleanup(fixture.Close)
	var captureMu sync.Mutex
	var captures [][]byte
	proxy := f.Proxy(func(_ http.ResponseWriter, r *http.Request) bool {
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		if bytes.Contains(data, []byte("new SDK mention")) {
			captureMu.Lock()
			captures = append(captures, data)
			captureMu.Unlock()
		}
		return false
	}, func(response *http.Response) error {
		if loseReceipt.Load() && response.StatusCode == http.StatusOK && response.Request.ContentLength > 0 {
			return errors.New("fixture drops accepted receipt")
		}
		return nil
	})
	c := githubTestConfig(f)
	c.CoreEndpoint = proxy.URL
	p, statusURL := startGitHubFixture(t, fixture.URL, c)
	triggertest.Wait(t, "silent baseline and GitHub poll interval", func() bool {
		s := readGitHubStatus(t, statusURL).Notifications
		return s.State == "ready" && s.NextAttemptAt.Sub(s.LastSuccessAt) >= 2*time.Second
	})
	require.Zero(t, reads.Load())
	require.Empty(t, triggertest.ReadMessages(t, f.Endpoint))
	baseline := readGitHubStatus(t, statusURL).Notifications.LastSuccessAt
	phase.Store(1)
	triggertest.Wait(t, "second page rate limit", func() bool {
		s := readGitHubStatus(t, statusURL)
		return s.Notifications.LastError == "github_http_429" && s.RateLimitedUntil.After(time.Now())
	})
	s := readGitHubStatus(t, statusURL)
	require.Equal(t, baseline, s.Notifications.LastSuccessAt)
	require.Equal(t, 1, s.Repositories[sdk].PendingReadbacks)
	require.Zero(t, reads.Load())
	triggertest.Terminate(t, p)
	phase.Store(2)
	p, statusURL = startGitHubFixture(t, fixture.URL, c)
	triggertest.Wait(t, "durable page resumes; a failed SDK object does not block the other repository", func() bool {
		s := readGitHubStatus(t, statusURL)
		return s.Repositories[sdk].Streams["notification/100"].LastError == "github_http_503" &&
			s.Repositories[mass].LastDeliveryError != "" && len(triggertest.ReadMessages(t, f.Endpoint)) == 1
	})
	require.GreaterOrEqual(t, pages.Load(), int32(2))
	cursorMu.Lock()
	require.Equal(t, baseline.Add(-githubOverlap).Truncate(time.Second).Format(time.RFC3339), pageOneSince)
	cursorMu.Unlock()
	sdkDown.Store(false)
	triggertest.Wait(t, "Core receives the first native SDK Message before its receipt is lost", func() bool {
		s := readGitHubStatus(t, statusURL).Repositories[sdk]
		return s.Backlog == 2 && s.LastDeliveryError != "" && len(triggertest.ReadMessages(t, f.Endpoint)) == 2
	})
	triggertest.Terminate(t, p)
	loseReceipt.Store(false)
	_, statusURL = startGitHubFixture(t, fixture.URL, c)
	triggertest.Wait(t, "stored payloads resume and both Core duplicate receipts are confirmed", func() bool {
		s := readGitHubStatus(t, statusURL).Repositories
		return s[sdk].Duplicates == 1 && s[mass].Duplicates == 1 && s[sdk].Accepted == 2 &&
			s[sdk].Backlog+s[mass].Backlog == 0
	})
	delivery := readGitHubStatus(t, statusURL)
	first := githubMessage(t, f, sdk, "comment_created", "502")
	bot := githubMessage(t, f, sdk, "comment_created", "503")
	second := githubMessage(t, f, mass, "notification_received", "42")
	require.NotEmpty(t, first)
	require.NotEmpty(t, bot)
	require.NotEmpty(t, second)
	require.Equal(t, true, first["human"])
	require.Equal(t, false, bot["human"])
	require.Equal(t, false, second["human"])
	require.NotEqual(t, first["threadKey"], second["threadKey"])
	require.Equal(t, second["id"], delivery.Repositories[mass].LastReceipt.MessageID)
	require.Equal(t, bot["id"], delivery.Repositories[sdk].LastReceipt.MessageID)
	require.Contains(t, first["text"], "historical discussion")
	require.Contains(t, second["text"], "subscribed")
	require.Contains(t, second["text"], githubObjectURL(mass, "issues", 42))
	require.Nil(t, githubMessage(t, f, sdk, "comment_created", "501"))
	captureMu.Lock()
	captured := append([][]byte(nil), captures...)
	captureMu.Unlock()
	require.GreaterOrEqual(t, len(captured), 2)
	for _, replay := range captured[1:] {
		require.Equal(t, captured[0], replay)
	}
	beforeReplay := delivery.Notifications.LastSuccessAt
	triggertest.Wait(t, "unchanged Notifications replay", func() bool {
		return readGitHubStatus(t, statusURL).Notifications.LastSuccessAt.After(beforeReplay)
	})
	require.Len(t, triggertest.ReadMessages(t, f.Endpoint), 3)
}

func TestGitHubNotificationsFilterAndReadPullDiscussion(t *testing.T) {
	t.Parallel()
	f := triggertest.NewCoreFixture(t, false, githubSource)
	const repo = "demo/sdk"
	old := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	fresh := time.Now().UTC().Truncate(time.Second).Add(3 * time.Second)
	prURL := githubObjectURL(repo, "pull", 42)
	var phase, outsideReads atomic.Int32
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var items []githubNotification
		if phase.Load() > 0 {
			noticeTime := fresh.Add(time.Duration(phase.Load()) * time.Second)
			items = []githubNotification{
				githubFixtureNotice(500, 42, "Demo/SDK", "PullRequest", "review_requested", noticeTime),
				githubFixtureNotice(501, 43, "demo/outside", "PullRequest", "mention", noticeTime),
				githubFixtureNotice(502, 44, repo, "CheckSuite", "ci_activity", noticeTime),
			}
		}
		githubFixtureJSON(w, append([]githubNotification{}, items...))
	}, func(w http.ResponseWriter, q githubFixtureQuery) {
		if q.Variables.ID == "" && (q.repository() != repo || (q.Variables.Number != 0 && q.Variables.Number != 42)) {
			outsideReads.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch q.connection() {
		case "":
			githubFixtureObject(w, q, githubFixturePull(repo, 42, old))
		case "comments":
			if q.Variables.ID != "" {
				comments := []map[string]any{githubFixtureComment(603, prURL+"#discussion_r603", "inline review comment", "User", fresh)}
				githubFixtureRespond(w, map[string]any{"node": map[string]any{"comments": githubFixturePage(comments, "")}})
				return
			}
			comments := []map[string]any{githubFixtureComment(601, prURL+"#issuecomment-601", "old PR discussion", "User", old)}
			if phase.Load() > 1 {
				comments = append(comments, githubFixtureComment(602, prURL+"#issuecomment-602", "new comment", "User", fresh))
			}
			githubFixtureObject(w, q, map[string]any{"comments": githubFixturePage(comments, "")})
		case "reviews":
			var reviews []map[string]any
			if phase.Load() > 1 {
				reviews = []map[string]any{{
					"fullDatabaseId": "604", "url": prURL + "#pullrequestreview-604", "body": "review from native ID",
					"author": githubFixtureAuthor("User"), "state": "APPROVED", "submittedAt": fresh,
				}}
			}
			githubFixtureObject(w, q, map[string]any{"reviews": githubFixturePage(reviews, "")})
		case "reviewThreads":
			var threads []map[string]any
			if phase.Load() > 1 {
				threads = []map[string]any{{"id": "inline-thread"}}
			}
			githubFixtureObject(w, q, map[string]any{"reviewThreads": githubFixturePage(threads, "")})
		default:
			githubFixtureEmpty(w, q)
		}
	}))
	t.Cleanup(fixture.Close)
	c := githubTestConfig(f)
	c.Repositories = []githubRepository{{"Demo/SDK", "Review SDK compatibility"}}
	_, statusURL := startGitHubFixture(t, fixture.URL, c)
	triggertest.Wait(t, "Notifications baseline", func() bool { return readGitHubStatus(t, statusURL).Notifications.State == "ready" })
	phase.Store(1)
	triggertest.Wait(t, "review-request fallback admission", func() bool {
		return githubMessage(t, f, repo, "notification_received", "742") != nil
	})
	fallback := githubMessage(t, f, repo, "notification_received", "742")
	require.Equal(t, false, fallback["human"])
	require.Contains(t, fallback["text"], "review_requested")
	require.Contains(t, fallback["text"], "old PR discussion")
	require.Contains(t, fallback["text"], "Review SDK compatibility")
	require.Contains(t, fallback["text"], prURL)
	f.AssertRouted(fallback, 1)
	topic := githubCreateTopic(t, f, fallback)
	githubRegisterFixturePR(t, f, topic, prURL)
	phase.Store(2)
	triggertest.Wait(t, "all discussion changes in the next Notifications version reach Core", func() bool {
		return len(triggertest.ReadMessages(t, f.Endpoint)) == 4
	})
	for id, kind := range map[string]string{"602": "comment_created", "603": "comment_created", "604": "review_submitted"} {
		msg := githubMessage(t, f, repo, kind, id)
		require.NotNil(t, msg, "missing %s %s", kind, id)
		require.Equal(t, prURL, msg["threadKey"])
		require.Equal(t, true, msg["human"])
		githubAssertTopicDelivery(t, f, msg, topic)
	}
	require.Zero(t, outsideReads.Load())
	require.Nil(t, githubMessage(t, f, repo, "comment_created", "601"))
	s := readGitHubStatus(t, statusURL)
	require.NotContains(t, s.Repositories, "demo/outside")
	require.Contains(t, s.Repositories[repo].Streams, "tracked/42")
	require.NotContains(t, s.Repositories[repo].Streams, "tracked/44")
	last := s.Notifications.LastSuccessAt
	triggertest.Wait(t, "overlap replay does not re-emit discussions", func() bool {
		return readGitHubStatus(t, statusURL).Notifications.LastSuccessAt.After(last)
	})
	for _, msg := range triggertest.ReadMessages(t, f.Endpoint) {
		require.NotContains(t, msg["text"], "demo/outside")
	}
	require.Len(t, triggertest.ReadMessages(t, f.Endpoint), 4)
}
