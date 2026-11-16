package sa

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The test process is an external writer: the real observer-only subprocess
// stays alive throughout replacement, failure and recovery. No mutation API or
// observer internals are used to publish these synthetic durable generations.
func replaceObserverState(t *testing.T, path string, raw []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), "external-state-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	_, err = f.Write(raw)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Rename(f.Name(), path))
}

func liveObserverState(config Config, generation int) State {
	marker := fmt.Sprintf("generation-%d", generation)
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	state := State{Version: stateVersion, Projects: map[string]ProjectState{}}
	for _, project := range config.Projects {
		state.Projects[project.ID] = ProjectState{Router: &RouterState{
			SessionRef: SessionRef{SessionID: project.ID + "-router-" + marker, Namespace: RouterNamespace, Name: RouterName(project)},
			RuntimeRef: "runtime-" + marker, ObservedState: marker, UpdatedAt: at.Add(time.Duration(generation) * time.Second),
		}}
	}
	p := state.Projects["alpha"]
	p.Triggers = map[string]TriggerState{}
	p.Tickets = map[string]TicketState{}
	p.Assignments = map[string]AssignmentState{}
	p.Outputs = map[string]OutputRecord{}
	p.Consumers = map[string]OutputConsumer{}
	for i := range generation + 1 {
		ticketID, triggerID := fmt.Sprintf("ticket-%d", i), fmt.Sprintf("trigger-%d", i)
		assignmentID := ticketID + "-" + marker
		p.Tickets[ticketID] = TicketState{ID: ticketID, ProjectID: "alpha", RootTicketID: ticketID, Title: marker, Objective: marker, BusinessState: marker, AssignmentID: assignmentID, CreatedAt: at, UpdatedAt: at}
		p.Assignments[assignmentID] = AssignmentState{ID: assignmentID, ProjectID: "alpha", TicketID: ticketID, RootTicketID: ticketID, Session: SessionRef{SessionID: "worker-" + marker}, RuntimeRef: "runtime-" + marker, Status: "active"}
		p.Triggers[triggerID] = TriggerState{TriggerReceipt: TriggerReceipt{TriggerID: triggerID, ProjectID: "alpha", Source: "fixture/account", SourceEventID: triggerID, ThreadID: "thread", AcceptedAt: at.Add(time.Duration(i) * time.Second)}, Kind: "message", Summary: marker, Content: json.RawMessage(fmt.Sprintf(`{"generation":%q}`, marker)), Decision: "associated", TicketIDs: []string{ticketID}}
		outputID := fmt.Sprintf("output-%d", i)
		p.Outputs[outputID] = OutputRecord{ID: outputID, Sequence: uint64(i + 1), ProjectID: "alpha", TriggerID: "trigger-0", TicketID: "ticket-0", Kind: "reply.summary", Summary: marker, RecordedAt: at, Targets: []OutputTarget{{Source: "fixture/account", ThreadID: "thread"}}}
	}
	p.NextOutputSeq = uint64(generation + 1)
	p.Consumers[consumerKey("existing-adapter", "fixture/account", "thread")] = OutputConsumer{Cursor: uint64(generation), Observations: map[string]OutputObservation{
		"output-0": {ReadAt: at, Outcome: "delivered", Reference: marker, ConfirmedAt: at},
	}}
	p.Inbox = map[string]InboxEventState{"input": {ID: "input", TriggerID: "trigger-0", TicketID: "ticket-0", TargetAssignmentID: "ticket-0-" + marker, TargetSession: SessionRef{SessionID: "worker-" + marker}, RoutingPath: "direct", State: "delivered"}}
	state.Projects["alpha"] = p
	return state
}

func seedLiveObserver(t *testing.T) (Config, *fakeMASSServer, []byte, []byte) {
	t.Helper()
	config, mass := observerFixture(t)
	for generation := 1; generation <= 2; generation++ {
		marker := fmt.Sprintf("generation-%d", generation)
		for _, project := range config.Projects {
			mass.addSession(Session{Metadata: SessionMetadata{Namespace: RouterNamespace, Name: RouterName(project)}, Spec: SessionSpec{RuntimeRef: "runtime-" + marker}, Status: SessionStatus{SessionID: project.ID + "-router-" + marker, State: "idle"}})
		}
		mass.addSession(Session{Metadata: SessionMetadata{Namespace: "shared", Name: "worker-" + marker}, Spec: SessionSpec{RuntimeRef: "runtime-" + marker}, Status: SessionStatus{SessionID: "worker-" + marker, State: "idle"}})
	}
	first, err := json.Marshal(liveObserverState(config, 1))
	require.NoError(t, err)
	second, err := json.Marshal(liveObserverState(config, 2))
	require.NoError(t, err)
	replaceObserverState(t, config.StateFile, first)
	return config, mass, first, second
}

func TestObserverLiveStatePublicQueries(t *testing.T) {
	config, mass, first, second := seedLiveObserver(t)
	base, configPath := startObserverProcess(t, config)
	verify := func(generation int, raw []byte) {
		marker := fmt.Sprintf("generation-%d", generation)
		page := observerGet[ObserverPage[ObserverRouter]](t, base, "routers")
		require.Len(t, page.Items, 2)
		for _, router := range page.Items {
			require.Equal(t, router.Project.ID+"-router-"+marker, router.Persisted.SessionID)
			require.Equal(t, router.Persisted.SessionID, router.Observation.Session.SessionID)
		}
		cliRouters := runT06JSON[ObserverPage[ObserverRouter]](t, "observe", "routers", "--config", configPath)
		for i, router := range page.Items {
			require.Equal(t, router.Persisted, cliRouters.Items[i].Persisted)
			require.Equal(t, router.Observation.Session, cliRouters.Items[i].Observation.Session)
		}
		router := observerGet[ObserverRouter](t, base, "routers/alpha")
		require.Equal(t, "alpha-router-"+marker, router.Persisted.SessionID)
		triggers := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha")
		require.Len(t, triggers.Items, generation+1)
		require.Equal(t, triggers.Items, runT06JSON[ObserverPage[ObserverTrigger]](t, "observe", "triggers", "--project", "alpha", "--config", configPath).Items)
		trigger := observerGet[ObserverTriggerDetail](t, base, "triggers/trigger-0?project=alpha")
		require.Equal(t, generation+1, trigger.ReplyCount)
		require.Contains(t, trigger.Content, marker)
		require.Equal(t, "worker-"+marker, trigger.Tickets[0].CurrentSessionID)
		require.Equal(t, "worker-"+marker, trigger.Deliveries[0].SessionID)
		require.Equal(t, trigger.ObserverTrigger, runT06JSON[ObserverTriggerDetail](t, "observe", "triggers", "trigger-0", "--project", "alpha", "--config", configPath).ObserverTrigger)
		tickets := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha")
		require.Len(t, tickets.Items, generation+1)
		require.Equal(t, tickets.Items, runT06JSON[ObserverPage[ObserverTicket]](t, "observe", "tickets", "--project", "alpha", "--config", configPath).Items)
		ticket := observerGet[ObserverTicketDetail](t, base, "tickets/ticket-0?project=alpha")
		require.Equal(t, marker, ticket.Objective)
		require.Equal(t, "worker-"+marker, ticket.CurrentSessionID)
		require.Len(t, ticket.Assignments, 1)
		require.Equal(t, "ticket-0-"+marker, ticket.Assignments[0].ID)
		require.Equal(t, ticket.ObserverTicket, runT06JSON[ObserverTicketDetail](t, "observe", "tickets", "ticket-0", "--project", "alpha", "--config", configPath).ObserverTicket)
		outputs := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha")
		require.Len(t, outputs.Items, generation+1)
		require.Equal(t, outputs.Items, runT06JSON[ObserverPage[ObserverOutput]](t, "observe", "outputs", "--project", "alpha", "--config", configPath).Items)
		output := observerGet[ObserverOutput](t, base, "outputs/output-0?project=alpha")
		require.Equal(t, marker, output.Summary)
		require.Len(t, output.Audit, 1)
		require.Equal(t, marker, output.Audit[0].Reference)
		require.Equal(t, output, runT06JSON[ObserverOutput](t, "observe", "outputs", "output-0", "--project", "alpha", "--config", configPath))
		session := observerGet[ObserverResourceSession](t, base, "sessions/worker-"+marker+"?project=alpha")
		require.NotEmpty(t, session.Associations)
		for _, association := range session.Associations {
			require.True(t, association.Current || association.Role == "inbox")
			require.Contains(t, association.AssignmentID, marker)
		}
		require.Equal(t, session, runT06JSON[ObserverResourceSession](t, "observe", "session", session.SessionID, "--project", "alpha", "--config", configPath))
		sessions := observerGet[ObserverPage[ObserverResourceSession]](t, base, "sessions?project=alpha")
		for _, session := range sessions.Items {
			require.Contains(t, session.SessionID, marker)
		}
		runtimes := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?project=alpha")
		require.Len(t, runtimes.Items, 2)
		require.Equal(t, config.Projects[0].RuntimeRef, runtimes.Items[0].Name)
		require.Equal(t, "runtime-"+marker, runtimes.Items[1].Name)
		require.Equal(t, runtimes.Items, runT06JSON[ObserverPage[ObserverRuntime]](t, "observe", "runtimes", "--project", "alpha", "--config", configPath).Items)
		after, err := os.ReadFile(config.StateFile)
		require.NoError(t, err)
		require.Equal(t, raw, after, "observation must preserve externally published state and consumers")
	}
	verify(1, first)
	triggerFirst := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha&limit=1")
	ticketFirst := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha&limit=1")
	outputFirst := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&limit=1")
	routerFirst := observerGet[ObserverPage[ObserverRouter]](t, base, "routers?limit=1")
	runtimeFirst := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?project=alpha&limit=1")
	replaceObserverState(t, config.StateFile, second)
	verify(2, second)
	triggerNext := runT06JSON[ObserverPage[ObserverTrigger]](t, "observe", "triggers", "--project", "alpha", "--limit", "1", "--cursor", triggerFirst.Next, "--config", configPath)
	require.Equal(t, triggerFirst.ObservedAt, triggerNext.ObservedAt)
	require.Empty(t, triggerNext.Next)
	require.Equal(t, "generation-1", triggerNext.Items[0].Summary)
	require.Equal(t, 2, triggerNext.Items[0].ReplyCount)
	ticketNext := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha&limit=1&cursor="+url.QueryEscape(ticketFirst.Next))
	require.Equal(t, ticketFirst.ObservedAt, ticketNext.ObservedAt)
	require.Empty(t, ticketNext.Next)
	require.Equal(t, "generation-1", ticketNext.Items[0].Title)
	outputNext := observerGet[ObserverPage[ObserverOutput]](t, base, "outputs?project=alpha&limit=1&cursor="+url.QueryEscape(outputFirst.Next))
	require.Equal(t, outputFirst.ObservedAt, outputNext.ObservedAt)
	require.Empty(t, outputNext.Next)
	require.Equal(t, "generation-1", outputNext.Items[0].Summary)
	routerNext := observerGet[ObserverPage[ObserverRouter]](t, base, "routers?limit=1&cursor="+url.QueryEscape(routerFirst.Next))
	require.Equal(t, routerFirst.ObservedAt, routerNext.ObservedAt)
	require.Equal(t, "beta-router-generation-1", routerNext.Items[0].Persisted.SessionID)
	runtimeNext := observerGet[ObserverPage[ObserverRuntime]](t, base, "runtimes?project=alpha&limit=1&cursor="+url.QueryEscape(runtimeFirst.Next))
	require.Equal(t, runtimeFirst.ObservedAt, runtimeNext.ObservedAt)
	require.Equal(t, "runtime-generation-1", runtimeNext.Items[0].Name)
	response, err := http.Get(base + "/observer/api/sessions/worker-generation-1?project=alpha")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusNotFound, response.StatusCode, "removed associations must not survive a fresh query")
	for _, operation := range mass.operationOrder() {
		require.Contains(t, []string{"session.get", "session.list", "runtime.list"}, operation)
	}

}

func TestObserverLiveStateFailureRecovery(t *testing.T) {
	config, mass, _, valid := seedLiveObserver(t)
	base, configPath := startObserverProcess(t, config)
	frozen := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha&limit=1")
	for _, test := range []struct {
		name, raw, reason string
		status            int
	}{
		{"missing", "", "state_unavailable", 503},
		{"damaged", `{"version":3,"projects":`, "invalid_state", 400},
		{"version", `{"version":999,"projects":{}}`, "unsupported_state_version", 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.raw == "" {
				require.NoError(t, os.Remove(config.StateFile))
			} else {
				replaceObserverState(t, config.StateFile, []byte(test.raw))
			}
			for _, resource := range []string{"health", "projects", "routers/alpha", "triggers?project=alpha", "tickets/ticket-0?project=alpha", "outputs?project=alpha", "sessions?project=alpha", "sessions/worker-generation-1/events?project=alpha"} {
				response, err := http.Get(base + "/observer/api/" + resource)
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, test.status, response.StatusCode, resource)
				require.Contains(t, string(body), test.reason, resource)
				require.NotContains(t, string(body), config.StateFile)
			}
			command := exec.Command(massSABinary, "observe", "tickets", "--project", "alpha", "--config", configPath)
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), test.reason)
			// A cached continuation remains its explicitly dated old traversal.
			next := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha&limit=1&cursor="+url.QueryEscape(frozen.Next))
			require.Equal(t, frozen.ObservedAt, next.ObservedAt)
			require.Equal(t, "generation-1", next.Items[0].Title)
			replaceObserverState(t, config.StateFile, valid)
			recovered := runT06JSON[ObserverPage[ObserverTicket]](t, "observe", "tickets", "--project", "alpha", "--config", configPath)
			require.Len(t, recovered.Items, 3)
			require.Equal(t, "generation-2", recovered.Items[0].Title)
		})
	}
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.Equal(t, valid, after)
	require.Empty(t, mass.operationOrder(), "failed state reads must not reach MASS")

}

func TestObserverNormalServeKeepsMemoryState(t *testing.T) {
	config, mass, _, second := seedLiveObserver(t)
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	original := store.Snapshot()
	bridge, err := NewTicketBridge(config, store)
	require.NoError(t, err)
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{})
	require.NoError(t, err)
	handler, err := NewManagementHandler(service, "test-management-token")
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	replaceObserverState(t, config.StateFile, second)
	// Exercise the same public management handler used by ordinary RunServe,
	// without bootstrapping Routers or dispatching synthetic work.
	read := func() ObserverPage[ObserverTicket] {
		request, err := http.NewRequest(http.MethodGet, server.URL+managementPrefix+"/observe/tickets?project=alpha", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer test-management-token")
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, 200, response.StatusCode)
		var page ObserverPage[ObserverTicket]
		require.NoError(t, json.NewDecoder(response.Body).Decode(&page))
		return page
	}
	page := read()
	require.Len(t, page.Items, 2)
	require.Equal(t, "generation-1", page.Items[0].Title)
	require.Equal(t, original, store.Snapshot())
	require.NoError(t, os.Remove(config.StateFile))
	require.Equal(t, page.Items, read().Items, "normal serve does not read an external file even when it disappears")
	require.Empty(t, mass.operationOrder())

}

func TestObserverLiveStateConcurrentReplacement(t *testing.T) {
	config, _, first, second := seedLiveObserver(t)
	base, _ := startObserverProcess(t, config)
	// Alternate complete generations while HTTP reads project all Trigger joins.
	// Each response must agree on summary, Ticket title, Assignment and replies.
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			raw := first
			if i%2 == 1 {
				raw = second
			}
			path := config.StateFile + ".external-writer"
			if err := os.WriteFile(path, raw, 0600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(path, config.StateFile); err != nil {
				done <- err
				return
			}
		}
	}()
	var once sync.Once
	finish := func() { once.Do(func() { close(stop); require.NoError(t, <-done) }) }
	defer finish()
	for range 50 {
		page := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha")
		marker := page.Items[0].Summary
		require.Contains(t, []string{"generation-1", "generation-2"}, marker)
		for _, trigger := range page.Items {
			require.Equal(t, marker, trigger.Summary)
			require.Equal(t, marker, trigger.Tickets[0].Title)
			require.True(t, strings.HasSuffix(trigger.Tickets[0].CurrentAssignmentID, marker))
			require.Equal(t, "worker-"+marker, trigger.Tickets[0].CurrentSessionID)
			if trigger.TriggerID == "trigger-0" {
				require.Equal(t, len(page.Items), trigger.ReplyCount)
			}
		}
	}
	finish()
	replaceObserverState(t, config.StateFile, second)
	require.Len(t, observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha").Items, 3)
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.Equal(t, second, after)
}
