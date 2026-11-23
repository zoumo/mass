package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/zoumo/mass/superagent/triggers/internal/triggertest"
)

const githubFixtureToken = "fixture-token-github"

type githubFixtureQuery struct {
	Query     string `json:"query"`
	Variables struct {
		Owner  string
		Name   string
		Number int
		Cursor string
		OID    string
		ID     string
	} `json:"variables"`
}

func (q githubFixtureQuery) repository() string { return q.Variables.Owner + "/" + q.Variables.Name }

func (q githubFixtureQuery) connection() string {
	for _, name := range []string{"checkSuites", "matchingPullRequests", "reviewThreads", "reviews", "comments"} {
		if strings.Contains(q.Query, name+"(") {
			return name
		}
	}
	return ""
}

func githubFixtureHandler(t *testing.T, notifications http.HandlerFunc, graph func(http.ResponseWriter, githubFixtureQuery)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+githubFixtureToken {
			t.Error("GitHub fixture received an unexpected token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/notifications" && r.URL.Query().Get("all") == "true":
			notifications(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql" && graph != nil:
			var q githubFixtureQuery
			if json.NewDecoder(r.Body).Decode(&q) != nil || strings.Contains(q.Query, "issues(") || strings.Contains(q.Query, "pullRequests(") || strings.Contains(q.Query, "statusCheckRollup") {
				t.Error("GitHub fixture received an unexpected collection query")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			graph(w, q)
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func githubFixtureJSON(w http.ResponseWriter, data any) {
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, "cannot encode fixture response", http.StatusInternalServerError)
	}
}

func githubFixtureRespond(w http.ResponseWriter, data any) {
	githubFixtureJSON(w, map[string]any{"data": data})
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
	case "comments", "reviews", "reviewThreads":
		if q.Variables.ID != "" {
			githubFixtureRespond(w, map[string]any{"node": map[string]any{conn: page}})
			return
		}
		githubFixtureObject(w, q, map[string]any{conn: page})
	case "checkSuites":
		githubFixtureRepo(w, map[string]any{"object": map[string]any{"oid": q.Variables.OID, conn: page}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func githubFixtureNotice(id, number int, repo, kind, reason string, updated time.Time) githubNotification {
	n := githubNotification{ID: strconv.Itoa(id), Reason: reason, UpdatedAt: updated}
	n.Repository.FullName = repo
	n.Subject.Type = kind
	collection := map[string]string{"Issue": "issues", "PullRequest": "pulls", "CheckSuite": "check-suites"}[kind]
	n.Subject.URL = fmt.Sprintf("https://api.github.com/repos/%s/%s/%d", repo, collection, number)
	return n
}

func githubFixtureIssue(id int64, number int, old time.Time) githubIssue {
	return githubIssue{ID: githubDatabaseID(id), Number: number, Title: "Old Issue", Body: "Existing issue body", State: "OPEN", CreatedAt: old, UpdatedAt: old}
}

func githubFixturePull(repo string, number int, old time.Time) githubPull {
	return githubPull{
		ID: githubDatabaseID(700 + number), Number: number, State: "OPEN", Title: "Old PR", Body: "Existing pull body", CreatedAt: old, UpdatedAt: old,
		BaseRefOID: strings.Repeat("d", 40), BaseRefName: "main", HeadRefOID: strings.Repeat("a", 40),
		HeadRepository: &githubRepositoryName{NameWithOwner: repo}, Mergeable: "CONFLICTING",
	}
}

func githubFixtureAuthor(kind string) map[string]any {
	if kind == "Bot" {
		return map[string]any{"login": "fixture-bot", "__typename": "Bot"}
	}
	return map[string]any{"login": "reviewer", "__typename": "User", "databaseId": 444}
}

func githubFixtureComment(id int, url, body, actor string, at time.Time) map[string]any {
	return map[string]any{
		"fullDatabaseId": strconv.Itoa(id), "body": body, "url": url, "author": githubFixtureAuthor(actor), "createdAt": at, "updatedAt": at,
	}
}

func githubTestConfig(f *triggertest.CoreFixture) githubConfig {
	return githubConfig{
		AccountID: "account", InstanceID: "fixture-instance", CoreEndpoint: f.Endpoint,
		DatabasePath: filepath.Join(f.Dir, "github.sqlite"), TokenEnv: "TEST_CORE_TOKEN",
		GitHubTokenEnv: "TEST_GITHUB_TOKEN", PollSeconds: 1, RequestMillis: 2500,
	}
}

func githubTestConfigFile(t *testing.T, c githubConfig) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(c.DatabasePath), "github.yaml")
	cfg, err := yaml.Marshal(c)
	require.NoError(t, err)
	triggertest.Write(t, path, cfg)
	return path
}

func githubTestBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SUPERAGENT_CORE_CLOCK")
	require.NotEmpty(t, bin, "SUPERAGENT_CORE_CLOCK must name a same-tree superagent build with the coretest GitHub fixture endpoint")
	return bin
}

func githubFixtureEnv(endpoint string) []string {
	return []string{
		"TEST_CORE_TOKEN=" + triggertest.Token, "TEST_GITHUB_TOKEN=" + githubFixtureToken,
		"TEST_GITHUB_ENDPOINT=" + endpoint, "GORACE=atexit_sleep_ms=0",
	}
}

func startGitHubFixture(t *testing.T, upstream string, c githubConfig) (*triggertest.Process, string) {
	t.Helper()
	path := githubTestConfigFile(t, c)
	p := triggertest.Start(t, githubTestBinary(t), []string{"triggers", "github", "serve", "--config", path}, githubFixtureEnv(upstream))
	endpoint := p.Endpoint(t, "statusEndpoint")
	return p, endpoint
}

func readGitHubStatus(t *testing.T, endpoint string) githubStatus {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), githubTestBinary(t), "triggers", "github", "status", "--endpoint", endpoint)
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var status githubStatus
	require.NoError(t, json.Unmarshal(out, &status), string(out))
	return status
}

func newGitHubCoreFixture(t *testing.T) *triggertest.CoreFixture {
	t.Helper()
	return triggertest.NewCoreFixture(t, false, githubSource, "https://github.com/demo/", "github:https%3A%2F%2Fgithub.com%2Fdemo%2F",
		"github:https%3A%2F%2Fgithub.com%2Felsewhere%2Fmass%2Fissues%2F")
}

func githubMessageMeta(msg map[string]any) map[string]any {
	meta, _ := msg["_meta"].(map[string]any)
	return meta
}

func githubFixtureMessageText(t *testing.T, msg map[string]any) string {
	t.Helper()
	blocks, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	block, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", block["type"])
	text, ok := block["text"].(string)
	require.True(t, ok)
	return text
}

func githubMessage(t *testing.T, f *triggertest.CoreFixture, repo, observation, objectID string) map[string]any {
	t.Helper()
	for _, msg := range triggertest.ReadMessages(t, f.Endpoint) {
		meta := githubMessageMeta(msg)
		if meta["repository"] == repo && meta["observation"] == observation && meta["objectId"] == objectID {
			return msg
		}
	}
	return nil
}

func githubCreateTopic(t *testing.T, f *triggertest.CoreFixture, message map[string]any) string {
	t.Helper()
	id := message["id"].(string)
	ids := []string{id}
	body := map[string]any{
		"operationId": "route-" + id, "messageIds": ids, "rounds": map[string]int{id: 1}, "kind": "route", "reason": "Fixture routing",
		"targets": []map[string]any{{"create": map[string]string{"title": "Fixture Topic", "workspace": f.Dir}, "messageIds": ids}},
	}
	type decision struct{ Targets []struct{ TopicID string } }
	reply := triggertest.CallRoleTool[decision](t, f.Endpoint, f.RoleToken, "route_messages", body)
	require.Len(t, reply.Targets, 1)
	require.NotEmpty(t, reply.Targets[0].TopicID)
	return reply.Targets[0].TopicID
}

func githubRegisterFixturePR(t *testing.T, f *triggertest.CoreFixture, topicID, prURL string) {
	t.Helper()
	token := f.WaitRoleToken("topic", topicID)
	type topicView struct {
		ID           string
		Revision     int
		PullRequests []struct{ CanonicalURL, ThreadKey, TopicID string }
	}
	before := triggertest.CallRoleTool[topicView](t, f.Endpoint, token, "get_topic", struct{}{})
	body := map[string]any{
		"operationId":      "register-" + topicID + "-" + prURL,
		"expectedRevision": before.Revision,
		"pullRequests":     map[string][]string{"add": {prURL}},
	}
	topic := triggertest.CallRoleTool[topicView](t, f.Endpoint, token, "update_topic", body)
	require.Equal(t, topicID, topic.ID)
	require.Equal(t, before.Revision+1, topic.Revision)
	require.Contains(t, topic.PullRequests, struct{ CanonicalURL, ThreadKey, TopicID string }{prURL, prURL, topicID})
}

func githubAssertTopicDelivery(t *testing.T, f *triggertest.CoreFixture, message map[string]any, topic string) {
	t.Helper()
	events := f.RoutingEvents(message)
	require.Len(t, events, 1)
	require.Equal(t, "routed_message", events[0].Kind)
	require.Equal(t, topic, events[0].Recipient.TopicID)
}
