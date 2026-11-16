package sa

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This joins the independently seeded page fixtures by durable identities.
// Actual ingress, MCP lifecycle and output production are covered by the public
// story; these rows let a browser traverse the same complete identity chain.
func seedObserverIntegratedFixture(t *testing.T, config Config, mass *fakeMASSServer) {
	t.Helper()
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		p.Assignments["assignment-old"] = AssignmentState{ID: "assignment-old", ProjectID: "alpha", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-old"}, RuntimeRef: "fixture-runtime", Status: "stopped"}
		current := p.Assignments["assignment-current"]
		current.ProjectID, current.RuntimeRef, current.Status = "alpha", "fixture-runtime", "active"
		p.Assignments[current.ID] = current
		p.Assignments["assignment-resumed"] = AssignmentState{ID: "assignment-resumed", ProjectID: "alpha", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-old"}, RuntimeRef: "fixture-runtime", Status: "stopped"}
		for _, ticket := range []TicketState{
			{ID: "t06-child", ParentTicketID: "ticket-a", RootTicketID: "ticket-a", Title: "联合故事子工作", Depth: 1},
			{ID: "t06-grandchild", ParentTicketID: "t06-child", RootTicketID: "ticket-a", Title: "联合故事孙工作", Depth: 2},
		} {
			ticket.ProjectID = "alpha"
			p.Tickets[ticket.ID] = ticket
		}
		return nil
	}))
	for _, id := range []string{"session-old", "session-current"} {
		mass.addSession(Session{Metadata: SessionMetadata{Name: id}, Spec: SessionSpec{RuntimeRef: "fixture-runtime"}, Status: SessionStatus{SessionID: id, State: "offline"}})
		mass.appendHistory(id, mass.nextSessionEvent(id, "agent.message", "integrated-"+id, json.RawMessage(`{"content":[{"type":"text","text":"Joint story durable history"}]}`)), false)
	}
}

// Called after the real public fake-peer story has stopped dispatch. The complete
// state and external mutation counts must stay unchanged during observation.
func verifyIntegratedObserverStory(t *testing.T, config Config, mass *fakeMASSServer, triggerID, ticketID, sessionID string) {
	t.Helper()
	before, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	massBefore := len(mass.operationOrder())

	base, path := startObserverProcess(t, config)
	routers := observerGet[ObserverPage[ObserverRouter]](t, base, "routers?project=mass")
	require.Len(t, routers.Items, 1)
	trigger := observerGet[ObserverTriggerDetail](t, base, "triggers/"+triggerID+"?project=mass")
	require.Len(t, trigger.Tickets, 1)
	require.Equal(t, ticketID, trigger.Tickets[0].ID)
	ticket := observerGet[ObserverTicketDetail](t, base, "tickets/"+ticketID+"?project=mass")
	require.Equal(t, "completed", ticket.BusinessState)
	require.Len(t, ticket.Descendants, 2)
	require.GreaterOrEqual(t, len(ticket.Assignments), 3)
	session := observerGet[ObserverResourceSession](t, base, "sessions/"+sessionID+"?project=mass")
	for _, assignment := range ticket.Assignments {
		if assignment.SessionID == sessionID {
			found := false
			for _, association := range session.Associations {
				found = found || association.AssignmentID == assignment.ID && association.TicketID == ticketID
			}
			require.True(t, found, "each historical Assignment must retain its own reverse association")
		}
	}
	history := observerGet[ObserverHistory](t, base, "sessions/"+sessionID+"/history?project=mass")
	require.NotEmpty(t, history.Events)
	replies := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&trigger="+triggerID+"&category=reply")
	require.Len(t, replies.Items, 2)
	facts := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=mass&relatedTrigger="+triggerID+"&category=fact")
	require.Len(t, facts.Items, 1)
	require.Equal(t, facts.Items[0].ID, replies.Items[1].FactID)
	require.Equal(t, ticketID, facts.Items[0].TicketID)
	require.NotEmpty(t, facts.Items[0].Audit)
	require.Equal(t, "failed", facts.Items[0].Audit[0].Outcome)
	cli := runT06JSON[ObserverTicketDetail](t, "observe", "tickets", ticketID, "--project", "mass", "--config", path)
	require.Equal(t, ticket.CurrentAssignmentID, cli.CurrentAssignmentID)
	for _, resource := range []string{"projects", "routers", "triggers", "tickets", "outputs", "sessions", "runtimes"} {
		response, e := http.Get(base + "/observer/api/" + resource + "?project=missing")
		require.NoError(t, e)
		require.Equal(t, 404, response.StatusCode, resource)
		_ = response.Body.Close()
	}
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.Equal(t, before, after, "all coordination, consumer and checkpoint bytes must be identical")
	var beforeState, afterState State
	require.NoError(t, json.Unmarshal(before, &beforeState))
	require.NoError(t, json.Unmarshal(after, &afterState))
	require.Equal(t, beforeState, afterState, "semantic state must be identical")

	for _, operation := range mass.operationOrder()[massBefore:] {
		require.Contains(t, []string{"session.get", "session.list", "session.history", "runtime.list", "runtime.get"}, operation)
	}
	// Observing did not consume the recovering adapter's unconfirmed outputs.
	page := runT06JSON[OutputPage](t, "output", "read", "--config", path, "--project", "mass", "--consumer", "recovering-adapter", "--source", "test/account", "--thread", "origin")
	require.Len(t, page.Outputs, 3)
}

func TestObserverUnknownProject(t *testing.T) {
	config, _ := observerFixture(t)
	base, _ := startObserverProcess(t, config)
	for _, resource := range []string{"projects", "routers"} {
		response, err := http.Get(base + "/observer/api/" + resource + "?project=missing")
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, response.StatusCode)
		_ = response.Body.Close()
	}
}

func TestObserverEveryProductionAsset(t *testing.T) {
	assets, err := observerAssets()
	if err != nil {
		t.Skip("ordinary build has no UI; its explicit 503 boundary is checked separately")
	}
	config, _ := observerFixture(t)
	base, _ := startObserverProcess(t, config)
	count := 0
	require.NoError(t, fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() {
			return nil
		}
		want, readErr := fs.ReadFile(assets, name)
		require.NoError(t, readErr)
		response, getErr := http.Get(base + "/observer/" + name)
		require.NoError(t, getErr)
		defer response.Body.Close()
		require.Equal(t, 200, response.StatusCode, name)
		got, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		if name == "index.html" {
			nonceTag := regexp.MustCompile(`<meta name="observer-style-nonce" content="([A-Za-z0-9+/]{32})">`)
			match := nonceTag.FindSubmatch(got)
			require.Len(t, match, 2, "HTML must carry its response-specific style nonce")
			require.Contains(t, response.Header.Get("Content-Security-Policy"), "'nonce-"+string(match[1])+"'")
			require.NotContains(t, response.Header.Get("Content-Security-Policy"), "unsafe-inline")
			got = []byte(strings.Replace(string(got), string(match[0]), "", 1))
		}
		require.Equal(t, want, got, name)
		count++
		return nil
	}))
	require.Greater(t, count, 5)
	for _, page := range []string{"triggers", "routers", "tickets", "resources"} {
		response, getErr := http.Get(base + "/observer/" + page)
		require.NoError(t, getErr)
		require.Equal(t, 200, response.StatusCode)
		_ = response.Body.Close()
	}
}
