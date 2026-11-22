package triggers

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"modernc.org/sqlite"
)

const githubFixtureToken = "github-fixture-token"

func runGitHubFixtureProcess(endpoint string) int {
	if marker := os.Getenv("TEST_GITHUB_PAUSE_SNAPSHOT"); marker != "" {
		err := sqlite.RegisterScalarFunction("pause_github_snapshot", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			if err := os.WriteFile(marker, []byte("transaction open"), 0o600); err != nil {
				return nil, err
			}
			select {}
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	component, err := Load(GitHub, os.Getenv("TEST_GITHUB_CONFIGURATION"), nil, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := component.serve(ctx, os.Stdout, endpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type githubFixtureQuery struct {
	Query     string `json:"query"`
	Variables struct {
		Owner   string
		Name    string
		Number  int
		Since   string
		Cursor  string
		States  []string
		OID     string
		ID      string
		IsIssue bool
	} `json:"variables"`
}

func (q githubFixtureQuery) connection() string {
	for _, name := range []string{"matchingPullRequests", "checkRuns", "issues", "pullRequests", "reviewThreads", "reviews", "comments", "contexts", "checkSuites"} {
		// matchingPullRequests is also nested under checkSuites, where it does
		// not include the independently paginated $cursor.
		if strings.Contains(q.Query, name+"(") && ((name != "matchingPullRequests" && name != "checkRuns") || strings.Contains(q.Query, "node(id:")) {
			return name
		}
	}
	return ""
}

func githubFixtureHandler(t *testing.T, handle func(http.ResponseWriter, githubFixtureQuery)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q githubFixtureQuery
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+githubFixtureToken || json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("invalid GitHub fixture request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handle(w, q)
	})
}

func githubFixtureRespond(w http.ResponseWriter, data any) {
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		http.Error(w, "cannot encode fixture response", http.StatusInternalServerError)
	}
}

func githubFixturePage[T any](nodes []T, cursor string) map[string]any {
	if nodes == nil {
		nodes = []T{}
	}
	var end any
	if cursor != "" {
		end = cursor
	}
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": end != nil, "endCursor": end}}
}

func githubFixtureRepo(w http.ResponseWriter, fields map[string]any) {
	githubFixtureRespond(w, map[string]any{"repository": fields})
}

func githubFixtureObject(w http.ResponseWriter, q githubFixtureQuery, data any) {
	object := "pullRequest"
	if strings.Contains(q.Query, "issue(number:") {
		object = "issue"
	}
	githubFixtureRepo(w, map[string]any{object: data})
}

func githubFixtureEmpty(w http.ResponseWriter, q githubFixtureQuery) {
	conn := q.connection()
	page := githubFixturePage([]any{}, "")
	switch conn {
	case "issues", "pullRequests":
		githubFixtureRepo(w, map[string]any{conn: page})
	case "comments", "reviews", "reviewThreads":
		if q.Variables.ID != "" {
			githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: page}})
			return
		}
		githubFixtureObject(w, q, map[string]any{conn: page})
	case "contexts":
		githubFixtureRepo(w, map[string]any{"object": map[string]any{"oid": q.Variables.OID, "statusCheckRollup": nil}})
	case "checkSuites":
		githubFixtureRepo(w, map[string]any{"object": map[string]any{"oid": q.Variables.OID, conn: page}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func githubFixtureAuthor() map[string]any {
	return map[string]any{"login": "reviewer", "__typename": "User", "databaseId": 444}
}

func githubFixtureComment(c githubComment) map[string]any {
	return map[string]any{
		"fullDatabaseId": strconv.FormatInt(c.ID, 10), "body": c.Body, "url": c.URL,
		"author": githubFixtureAuthor(), "createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	}
}

func TestGitHubDiscussionRefreshKeepsBothPrioritiesMoving(t *testing.T) {
	const repoName = "demo/project"
	repo := githubRepository{Repository: repoName}
	c := githubConfig{DatabasePath: filepath.Join(t.TempDir(), "github.sqlite"), Repositories: []githubRepository{repo}}
	store, closeDB, err := openGitHubStore(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	now := time.Now().UTC()
	commit := func(observations ...githubObservation) {
		t.Helper()
		stream, err := store.stream(repoName, "issues")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.commitScan(c, repo, "issues", stream, observations, now, now.Add(time.Minute), time.Time{}, true); err != nil {
			t.Fatal(err)
		}
	}
	check := func(priority bool, want string, closed bool) {
		t.Helper()
		next, err := store.nextScope(repoName, "discussion", priority, now, "")
		if err != nil || next != want {
			t.Fatalf("discussion schedule priority=%v: %q, %v", priority, next, err)
		}
		if next != "" {
			stream, err := store.stream(repoName, next)
			if err != nil || stream.DiscussionClosed != closed {
				t.Fatalf("discussion closure for %q: closed=%v, %v", next, stream.DiscussionClosed, err)
			}
		}
	}
	commit(
		githubObservation{Key: "10", Data: json.RawMessage(`{"number":10,"state":"closed"}`), UpdatedAt: now.Add(-time.Hour)},
		githubObservation{Key: "20", Data: json.RawMessage(`{"number":20,"state":"open"}`), UpdatedAt: now},
	)
	check(true, githubIssueCommentsPrefix+"20", false)
	check(false, githubIssueCommentsPrefix+"10", true)
	commit(githubObservation{Key: "10", Data: json.RawMessage(`{"number":10,"state":"open"}`), UpdatedAt: now})
	check(true, githubIssueCommentsPrefix+"10", false)
	check(false, "", false)
}
