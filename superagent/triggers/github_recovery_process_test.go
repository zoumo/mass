package triggers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubInterruptedScanRollsBackSnapshotsSchedulesAndQueue(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false, githubSource)
	const repo = "demo/restart"
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var changed atomic.Bool
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, q githubFixtureQuery) {
		if q.connection() != "issues" {
			githubFixtureEmpty(w, q)
			return
		}
		items := []githubIssue{githubIssueFixture(100, old, 0)}
		if changed.Load() {
			items[0] = githubIssueFixture(100, old, 1)
			items = append(items, githubIssueFixture(200, old, 4))
		}
		githubFixtureRepo(w, map[string]any{"issues": githubFixturePage(items, "")})
	}))
	t.Cleanup(fixture.Close)
	config := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: f.endpoint,
		DatabasePath: filepath.Join(f.dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", PollSeconds: 1, RequestMillis: 2500,
		Repositories: []githubRepository{{Repository: repo}},
	}
	process, statusURL := startGitHubFixture(t, f.dir, fixture.URL, config)
	wait(t, "first GitHub Issue baseline", func() bool {
		return readGitHubStatus(t, statusURL).Repositories[repo].Streams["issues"].Items == 1
	})
	terminate(t, process)

	baseline := func(query string) githubStreamState {
		t.Helper()
		store, closeDB, err := openGitHubStore(config)
		if err != nil {
			t.Fatal(err)
		}
		defer closeDB()
		stream, err := store.stream(repo, "issues")
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.scopeItems(repo, "issues")
		if err != nil || len(items) != 1 || items["100"].Version != 0 {
			t.Fatalf("uncommitted snapshots survived: %+v, %v", items, err)
		}
		if _, err := store.pendingHead(repo); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("uncommitted pending item survived: %v", err)
		}
		if _, err := store.stream(repo, githubIssueCommentsPrefix+"43"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("uncommitted discussion schedule survived: %v", err)
		}
		if _, err := store.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
		return stream
	}
	before := baseline(`CREATE TRIGGER github_test_pause_scan BEFORE INSERT ON github_snapshots
		WHEN NEW.scope = 'issues' AND NEW.object_key = '1200'
		BEGIN SELECT pause_github_snapshot(); END`)
	changed.Store(true)
	marker := filepath.Join(f.dir, "github-snapshot-transaction-open")
	process, _ = startGitHubFixture(t, f.dir, fixture.URL, config, "TEST_GITHUB_PAUSE_SNAPSHOT="+marker)
	wait(t, "an open scan transaction with a prior snapshot write and two queue inserts", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	killAdapter(t, process)
	after := baseline(`DROP TRIGGER github_test_pause_scan`)
	if !after.LastSuccessAt.Equal(before.LastSuccessAt) || !after.NextAttemptAt.Equal(before.NextAttemptAt) {
		t.Fatal("the interrupted scan advanced progress")
	}
	if messages := readMessages(t, f.endpoint); len(messages) != 0 {
		t.Fatalf("the interrupted scan submitted messages: %v", messages)
	}

	_, statusURL = startGitHubFixture(t, f.dir, fixture.URL, config)
	wait(t, "the interrupted scan is replayed and delivered in full", func() bool {
		status := readGitHubStatus(t, statusURL).Repositories[repo]
		return status.Backlog == 0 && status.Accepted == 2 && status.Streams["issues"].Items == 2
	})
	edited, opened := githubIssueMessages(t, f, "edited")[repo], githubIssueMessages(t, f, "opened")[repo]
	for _, item := range []map[string]any{edited, opened} {
		meta, ok := item["meta"].(map[string]any)
		if !ok || meta["observationVersion"] != float64(1) {
			t.Fatalf("the interrupted scan consumed an observation version: %v", item)
		}
	}
}

func TestGitHubMultiRepositoryRecovery(t *testing.T) {
	t.Parallel()
	f := newEgressFixture(t, false, githubSource)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var phase atomic.Int32
	var apiCalls atomic.Int64
	fixture := httptest.NewServer(githubFixtureHandler(t, func(w http.ResponseWriter, q githubFixtureQuery) {
		apiCalls.Add(1)
		step := phase.Load()
		if q.connection() != "issues" {
			githubFixtureEmpty(w, q)
			return
		}
		issuePage := func(items []githubIssue, cursor string) {
			githubFixtureRepo(w, map[string]any{"issues": githubFixturePage(items, cursor)})
		}
		if q.Variables.Name == "working" {
			issuePage([]githubIssue{githubIssueFixture(601, old, min(step, 1))}, "")
			return
		}
		if q.Variables.Cursor == "" {
			if step == 0 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			items := []githubIssue{githubIssueFixture(501, old, 0)}
			if step == 4 && q.Variables.Since == "" {
				missed := githubIssueFixture(503, old.Add(-time.Hour), 0)
				missed.Number += 2
				items = append(items, missed)
			}
			issuePage(items, "next-restricted-issue")
			return
		}
		second := githubIssueFixture(502, old, 0)
		second.Number++
		switch step {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":   map[string]any{"repository": map[string]any{"issues": githubFixturePage([]githubIssue{second}, "")}},
				"errors": []map[string]string{{"message": "partial response contains confidential text"}},
			})
		case 2:
			w.Header().Set("Retry-After", "4")
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"type": "RATE_LIMITED", "message": "secondary rate limit"}}})
		default:
			issuePage([]githubIssue{second}, "")
		}
	}))
	t.Cleanup(fixture.Close)
	var coreUnavailable atomic.Bool
	coreUnavailable.Store(true)
	proxy := f.proxy(func(w http.ResponseWriter, _ *http.Request) bool {
		if coreUnavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","retryable":true}}`))
			return true
		}
		return false
	}, nil)
	config := githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: proxy.URL,
		DatabasePath: filepath.Join(f.dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN", PollSeconds: 1, RequestMillis: 2500,
		Repositories: []githubRepository{{"demo/restricted", "Example restricted context"}, {"demo/working", "Example working context"}},
	}
	p, statusURL := startGitHubFixture(t, f.dir, fixture.URL, config)
	wait(t, "permission error isolated from the second repository", func() bool {
		s := readGitHubStatus(t, statusURL)
		return s.Repositories["demo/restricted"].Streams["issues"].LastError == "github_http_403" &&
			s.Repositories["demo/working"].Streams["issues"].Items == 1
	})
	phase.Store(1)
	wait(t, "partial read retains progress while the second repository queues an edit", func() bool {
		s := readGitHubStatus(t, statusURL)
		a := s.Repositories["demo/restricted"].Streams["issues"]
		b := s.Repositories["demo/working"]
		return a.LastError == "github_graphql_error" && a.LastSuccessAt.IsZero() && a.Items == 0 &&
			b.Backlog == 1 && b.LastDeliveryError == "unavailable"
	})
	phase.Store(2)
	wait(t, "provider retry deadline is visible in status", func() bool {
		s := readGitHubStatus(t, statusURL)
		a := s.Repositories["demo/restricted"].Streams["issues"]
		return s.RateLimitedUntil.After(time.Now()) && a.LastError == "github_rate_limited" &&
			!a.NextAttemptAt.Before(s.RateLimitedUntil) && a.Items == 0 && a.LastSuccessAt.IsZero()
	})
	terminate(t, p)
	phase.Store(3)
	callsAtRestart := apiCalls.Load()
	store, closeDB, err := openGitHubStore(config)
	if err != nil {
		t.Fatal(err)
	}
	pausedUntil, err := readGitHubRateLimit(store.db)
	closeDB()
	if err != nil {
		t.Fatal(err)
	}
	coreUnavailable.Store(false)
	p, statusURL = startGitHubFixture(t, f.dir, fixture.URL, config)
	if time.Now().Before(pausedUntil) {
		s := readGitHubStatus(t, statusURL)
		if !s.RateLimitedUntil.Equal(pausedUntil) {
			t.Fatal("restart lost the persisted GitHub retry deadline")
		}
		if time.Now().Before(pausedUntil) && apiCalls.Load() != callsAtRestart {
			t.Fatal("GitHub reads resumed before Retry-After expired")
		}
	}
	wait(t, "GitHub progress and Core submission recover", func() bool {
		s := readGitHubStatus(t, statusURL)
		a := s.Repositories["demo/restricted"].Streams["issues"]
		b := s.Repositories["demo/working"]
		return a.State == "ready" && a.Items == 2 && !a.LastFullAt.IsZero() && b.Backlog == 0 && b.Accepted == 1
	})
	got := githubIssueMessages(t, f, "edited")
	if len(got) != 1 || got["demo/working"] == nil {
		t.Fatalf("expected one recovered Message from the working repository, got %v", got)
	}
	terminate(t, p)

	store, closeDB, err = openGitHubStore(config)
	if err != nil {
		t.Fatal(err)
	}
	lastFullAt := time.Now().Add(-2 * githubFullInterval)
	_, err = store.db.ExecContext(t.Context(), `UPDATE github_streams SET next_attempt_at = 0, last_full_at = ?
		WHERE repository = 'demo/restricted' AND scope = 'issues'`, githubTimeValue(lastFullAt))
	closeDB()
	if err != nil {
		t.Fatal(err)
	}
	phase.Store(4)
	_, statusURL = startGitHubFixture(t, f.dir, fixture.URL, config)
	wait(t, "scheduled full reconciliation discovers an older Issue", func() bool {
		result := readGitHubStatus(t, statusURL).Repositories["demo/restricted"].Streams["issues"]
		return result.Items == 3 && result.LastFullAt.After(lastFullAt)
	})
	if len(readMessages(t, f.endpoint)) != 1 {
		t.Fatal("historical reconciliation generated an extra Message")
	}
}
