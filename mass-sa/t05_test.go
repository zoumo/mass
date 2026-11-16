package sa

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type t05Clock struct {
	mu      sync.Mutex
	current time.Time
}

func newT05Clock() *t05Clock {
	return &t05Clock{current: time.Unix(1_800_000_000, 0).UTC()}
}

func (c *t05Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *t05Clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(duration)
}

type observedPrompt struct {
	Session SessionRef
	Text    string
}

func setT05Inbox(t *testing.T, harness *workerHarness, projectID string, windowMS, maxBatch, maxPromptBytes int) {
	t.Helper()
	inbox := InboxConfig{MergeWindowMS: windowMS, MaxBatchSize: maxBatch, MaxPromptBytes: maxPromptBytes}
	found := false
	for index := range harness.config.Projects {
		if harness.config.Projects[index].ID == projectID {
			harness.config.Projects[index].Inbox = inbox
			found = true
		}
	}
	for index := range harness.service.config.Projects {
		if harness.service.config.Projects[index].ID == projectID {
			harness.service.config.Projects[index].Inbox = inbox
		}
	}
	if !found {
		t.Fatalf("project %s is not configured", projectID)
	}
}

func observedPrompts(t *testing.T, mass *fakeMASSServer) []observedPrompt {
	t.Helper()
	mass.mu.Lock()
	rawPayloads := append([]json.RawMessage(nil), mass.promptPayloads...)
	mass.mu.Unlock()
	result := make([]observedPrompt, 0, len(rawPayloads))
	for _, raw := range rawPayloads {
		var params struct {
			Session SessionRef `json:"session"`
			Input   struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatal(err)
		}
		text := ""
		if len(params.Input.Content) != 0 {
			text = params.Input.Content[0].Text
		}
		result = append(result, observedPrompt{Session: params.Session, Text: text})
	}
	return result
}

func promptsForSession(prompts []observedPrompt, sessionID string) []observedPrompt {
	var result []observedPrompt
	for _, prompt := range prompts {
		if prompt.Session.SessionID == sessionID {
			result = append(result, prompt)
		}
	}
	return result
}

func acceptT05Event(t *testing.T, harness *workerHarness, rootTicketID, routeKey, summary string, payload any) AcceptedEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.service.AcceptEvent(AcceptEventRequest{
		ProjectID:    "mass",
		RootTicketID: rootTicketID,
		RouteKey:     routeKey,
		Source:       "source-a",
		Kind:         "change",
		Summary:      summary,
		Payload:      raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestT05FirstEventUsesRouterThenBindingDeliversDirect(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "direct")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-direct", "", "")

	first := acceptT05Event(t, harness, root.ID, "repo/main/push", "first event", map[string]string{"value": "one"})
	if first.RoutingPath != "router" || first.TargetAssignmentID != "" || first.TargetSession.SessionID == "" {
		t.Fatalf("first accepted event = %+v", first)
	}
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	project := harness.store.Snapshot().Projects["mass"]
	if project.Inbox[first.EventID].State != "delivered" {
		t.Fatalf("first event = %+v", project.Inbox[first.EventID])
	}
	routerPrompts := promptsForSession(observedPrompts(t, harness.mass), project.Router.SessionID)
	if len(routerPrompts) != 1 || !strings.Contains(routerPrompts[0].Text, first.EventID) {
		t.Fatalf("router prompts = %+v", routerPrompts)
	}
	if reports := project.Tickets[root.ID].Reports; len(reports) != 0 {
		t.Fatalf("ordinary Router final created worker reports: %+v", reports)
	}
	routerBatch := firstDelivery(project.Deliveries)
	if routerBatch.ResultState != "confirmed" || routerBatch.FinalEventID == "" {
		t.Fatalf("Router final audit = %+v", routerBatch)
	}

	binding, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "repo/main/push",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.AssignmentID != worker.AssignmentID {
		t.Fatalf("binding = %+v", binding)
	}
	second := acceptT05Event(t, harness, root.ID, "repo/main/push", "second event", map[string]string{"value": "two"})
	if second.RoutingPath != "direct" || second.TargetAssignmentID != worker.AssignmentID ||
		second.TargetSession.SessionID != worker.SessionID {
		t.Fatalf("second accepted event = %+v", second)
	}
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	prompts := observedPrompts(t, harness.mass)
	if got := len(promptsForSession(prompts, harness.store.Snapshot().Projects["mass"].Router.SessionID)); got != 1 {
		t.Fatalf("router prompt count = %d, want 1", got)
	}
	workerPrompts := promptsForSession(prompts, worker.SessionID)
	if len(workerPrompts) != 1 || !strings.Contains(workerPrompts[0].Text, second.EventID) {
		t.Fatalf("worker prompts = %+v", workerPrompts)
	}

	inspection, err := harness.service.InspectTicket("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Counts.Direct != 1 || inspection.Counts.Delivered != 2 {
		t.Fatalf("inspection counts = %+v", inspection.Counts)
	}
	if len(inspection.Batches) < 2 {
		t.Fatalf("inspection batches = %+v", inspection.Batches)
	}
	states := map[string]string{}
	for _, event := range inspection.Events {
		states[event.ID] = event.State
	}
	if states[first.EventID] != "delivered" || states[second.EventID] != "delivered" {
		t.Fatalf("event states = %+v", states)
	}
}

func TestT05RouterFinalAfterRootResolutionIsRetained(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "resolved-router-final")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-resolved-final", "", "")

	harness.mass.setPromptTurn(fakePromptTurn{
		FinalText:        `{"status":"completed","summary":"worker completed the ticket"}`,
		Phase:            "final_answer",
		EmitFinal:        true,
		EmitHistoryFinal: true,
	})
	routerSend(t, harness, root.ID, worker.Name, "Implement and report the ticket before acceptance.")
	workerRun, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
	if err != nil {
		t.Fatal(err)
	}
	project := harness.store.Snapshot().Projects["mass"]
	workerReport := t05TicketReport(t, project.Tickets[ticket.ID], workerRun.SourceEventID)
	if workerReport.Status != "confirmed" || workerReport.ID == "" {
		t.Fatalf("worker report = %+v", workerReport)
	}

	const routerFinal = "router final retained after root resolution"
	routerGate := harness.mass.blockPromptFinal(project.Router.SessionID)
	released := false
	defer func() {
		if !released {
			close(routerGate)
		}
	}()
	harness.mass.setPromptTurn(fakePromptTurn{
		FinalText:        `{"status":"completed","summary":"` + routerFinal + `"}`,
		Phase:            "final_answer",
		EmitFinal:        true,
		EmitHistoryFinal: true,
	})

	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Millisecond)
	dispatched := make(chan error, 1)
	go func() {
		_, dispatchErr := dispatcher.DispatchOnce(t.Context())
		dispatched <- dispatchErr
	}()
	waitT06(t, 5*time.Second, "router prompt accepted before final", func() bool {
		current := harness.store.Snapshot().Projects["mass"]
		return current.Inbox[workerRun.RouterInboxEventID].State == "delivered"
	})

	if status, body := harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
		TicketID:   ticket.ID,
		Resolution: "completed",
		Summary:    "Accept the confirmed report while the router final is still pending.",
	}); status != http.StatusOK {
		t.Fatalf("finish_ticket status %d: %s", status, body)
	}
	revisionBefore := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].Revision
	close(routerGate)
	released = true
	select {
	case dispatchErr := <-dispatched:
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("router final did not finish after release")
	}

	project = harness.store.Snapshot().Projects["mass"]
	var routerDelivery DeliveryBatchState
	for _, batch := range project.Deliveries {
		if t05ContainsEvent(batch.EventIDs, workerRun.RouterInboxEventID) {
			routerDelivery = batch
			break
		}
	}
	if routerDelivery.ID == "" || routerDelivery.State != "delivered" ||
		routerDelivery.ResultState != "confirmed" || routerDelivery.FinalEventID == "" {
		t.Fatalf("router delivery = %+v", routerDelivery)
	}
	if reports := project.Tickets[root.ID].Reports; len(reports) != 0 {
		t.Fatalf("ordinary Router final leaked as worker report: %+v", reports)
	}
	if revision := project.Tickets[ticket.ID].Revision; revision != revisionBefore {
		t.Fatalf("router final changed business revision: before=%d after=%d", revisionBefore, revision)
	}
	if project.Tickets[ticket.ID].BusinessState != "completed" {
		t.Fatalf("router final changed ticket resolution: %+v", project.Tickets[ticket.ID])
	}
}

func t05TicketReport(t *testing.T, ticket TicketState, sourceEventID string) ReportState {
	t.Helper()
	for _, report := range ticket.Reports {
		if report.SourceEventID == sourceEventID {
			return report
		}
	}
	t.Fatalf("ticket %s has no report for source event %s", ticket.ID, sourceEventID)
	return ReportState{}
}

func t05ContainsEvent(events []string, eventID string) bool {
	for _, candidate := range events {
		if candidate == eventID {
			return true
		}
	}
	return false
}

func TestT05RouteBindingsReplaceRevokeAndInvalidBindingsFallBack(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticketOne := harness.rootAndChild(t, "mass", "route-lifecycle")
	ticketTwo := harness.createTicket(t, "mass", root.ID, "route-lifecycle-two")
	workerOne := harness.createWorker(t, "mass", root.ID, ticketOne.ID, "worker-one", "", "")
	workerTwo := harness.createWorker(t, "mass", root.ID, ticketTwo.ID, "worker-two", "", "")

	first, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "source-route",
		WorkerName:   workerOne.Name,
		AssignmentID: workerOne.AssignmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.AssignmentID != workerOne.AssignmentID {
		t.Fatalf("first binding = %+v", first)
	}
	replaced, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "source-route",
		WorkerName:   workerTwo.Name,
		AssignmentID: workerTwo.AssignmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.AssignmentID != workerTwo.AssignmentID {
		t.Fatalf("replacement = %+v", replaced)
	}
	routes, err := harness.service.ListEventRoutes("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].AssignmentID != workerTwo.AssignmentID {
		t.Fatalf("routes after replacement = %+v", routes)
	}
	direct := acceptT05Event(t, harness, root.ID, "source-route", "replacement target", map[string]string{"x": "y"})
	if direct.RoutingPath != "direct" || direct.TargetAssignmentID != workerTwo.AssignmentID {
		t.Fatalf("direct event = %+v", direct)
	}

	revoked, err := harness.service.RevokeEventRoute("mass", root.ID, "source-route")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Revoked || revoked.AssignmentID != workerTwo.AssignmentID {
		t.Fatalf("revoked binding = %+v", revoked)
	}
	routes, err = harness.service.ListEventRoutes("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes after revoke = %+v", routes)
	}
	afterRevoke := acceptT05Event(t, harness, root.ID, "source-route", "after revoke", map[string]string{"x": "z"})
	if afterRevoke.RoutingPath != "router" {
		t.Fatalf("after revoke event = %+v", afterRevoke)
	}

	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "stopped-source",
		WorkerName:   workerOne.Name,
		AssignmentID: workerOne.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := harness.store.updateProject("mass", func(project *ProjectState) error {
		assignment := project.Assignments[workerOne.AssignmentID]
		assignment.Status = "stopped"
		project.Assignments[workerOne.AssignmentID] = assignment
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := acceptT05Event(t, harness, root.ID, "stopped-source", "stale binding", map[string]string{"x": "s"})
	if stale.RoutingPath != "router" {
		t.Fatalf("stale binding event = %+v", stale)
	}
	routes, err = harness.service.ListEventRoutes("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("stale binding remains: %+v", routes)
	}
}

func TestT05DirectEventsMergeInAcceptanceOrder(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "merge")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-merge", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "merge-key",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	var eventIDs []string
	for index := 1; index <= 3; index++ {
		accepted := acceptT05Event(t, harness, root.ID, "merge-key", "event", map[string]int{"sequence": index})
		eventIDs = append(eventIDs, accepted.EventID)
	}
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	workerPrompts := promptsForSession(observedPrompts(t, harness.mass), worker.SessionID)
	if len(workerPrompts) != 1 {
		t.Fatalf("worker prompts = %d, want 1", len(workerPrompts))
	}
	lastIndex := -1
	for _, eventID := range eventIDs {
		index := strings.Index(workerPrompts[0].Text, eventID)
		if index < 0 || index <= lastIndex {
			t.Fatalf("events are not in acceptance order: %v\n%s", eventIDs, workerPrompts[0].Text)
		}
		lastIndex = index
	}
	inspection, err := harness.service.InspectTicket("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	var directBatches []DeliveryBatchView
	for _, batch := range inspection.Batches {
		if batch.RoutingPath == "direct" {
			directBatches = append(directBatches, batch)
		}
	}
	if len(directBatches) != 1 || !equalStrings(directBatches[0].EventIDs, eventIDs) ||
		directBatches[0].State != "delivered" {
		t.Fatalf("direct batches = %+v", directBatches)
	}
	if inspection.Counts.Direct != 3 || inspection.Counts.Delivered != 3 {
		t.Fatalf("counts = %+v", inspection.Counts)
	}
}

func TestT05PromptBudgetSplitsOnlyAtEventBoundaries(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 10, 64<<10)
	root, ticket := harness.rootAndChild(t, "mass", "split")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-split", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "split-key",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	var eventIDs []string
	for index := 0; index < 3; index++ {
		accepted := acceptT05Event(t, harness, root.ID, "split-key", "event", map[string]string{
			"body": strings.Repeat("x", 400),
		})
		eventIDs = append(eventIDs, accepted.EventID)
		clock.Advance(time.Millisecond)
	}
	projectState := harness.store.Snapshot().Projects["mass"]
	single := 0
	for _, eventID := range eventIDs {
		size := len(workerPromptFromEvents([]InboxEventState{projectState.Inbox[eventID]}))
		if size > single {
			single = size
		}
	}
	setT05Inbox(t, harness, "mass", 1, 10, single+8)
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	for range eventIDs {
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	workerPrompts := promptsForSession(observedPrompts(t, harness.mass), worker.SessionID)
	if len(workerPrompts) != len(eventIDs) {
		t.Fatalf("worker prompts = %d, want %d", len(workerPrompts), len(eventIDs))
	}
	for _, prompt := range workerPrompts {
		if len(prompt.Text) > single+8 {
			t.Fatalf("prompt bytes = %d, budget = %d", len(prompt.Text), single+8)
		}
	}
	inspection, err := harness.service.InspectTicket("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	var directBatches []DeliveryBatchView
	for _, batch := range inspection.Batches {
		if batch.RoutingPath == "direct" {
			directBatches = append(directBatches, batch)
			if len(batch.EventIDs) != 1 || batch.PromptBytes > single+8 {
				t.Fatalf("oversized or truncated batch = %+v", batch)
			}
		}
	}
	if len(directBatches) != len(eventIDs) {
		t.Fatalf("direct batches = %+v", directBatches)
	}
}

func TestT05DifferentRootsSharingRouterDoNotMerge(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	rootOne, _ := harness.rootAndChild(t, "mass", "router-one")
	rootTwo, _ := harness.rootAndChild(t, "mass", "router-two")
	eventOne := acceptT05Event(t, harness, rootOne.ID, "one", "root one", map[string]string{"root": "one"})
	clock.Advance(time.Millisecond)
	eventTwo := acceptT05Event(t, harness, rootTwo.ID, "two", "root two", map[string]string{"root": "two"})
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	routerSessionID := harness.store.Snapshot().Projects["mass"].Router.SessionID
	routerPrompts := promptsForSession(observedPrompts(t, harness.mass), routerSessionID)
	if len(routerPrompts) != 2 {
		t.Fatalf("router prompts = %+v", routerPrompts)
	}
	for _, prompt := range routerPrompts {
		containsOne := strings.Contains(prompt.Text, eventOne.EventID)
		containsTwo := strings.Contains(prompt.Text, eventTwo.EventID)
		if containsOne == containsTwo {
			t.Fatalf("roots merged or event missing: %s", prompt.Text)
		}
	}
}

func TestT05MergeWindowAndMaxBatchSizeAreConfigurable(t *testing.T) {
	t.Run("merge window", func(t *testing.T) {
		harness := newWorkerHarness(t)
		clock := newT05Clock()
		harness.service.now = clock.Now
		setT05Inbox(t, harness, "mass", 5, 8, 32<<10)
		root, _ := harness.rootAndChild(t, "mass", "window")
		event := acceptT05Event(t, harness, root.ID, "window-key", "wait for window", map[string]string{"window": "yes"})
		dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "queued" {
			t.Fatalf("event before window = %q, want queued", got)
		}
		clock.Advance(4 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "queued" {
			t.Fatalf("event inside window = %q, want queued", got)
		}
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "delivered" {
			t.Fatalf("event after window = %q, want delivered", got)
		}
	})

	t.Run("max batch size", func(t *testing.T) {
		harness := newWorkerHarness(t)
		clock := newT05Clock()
		harness.service.now = clock.Now
		setT05Inbox(t, harness, "mass", 1, 2, 32<<10)
		root, _ := harness.rootAndChild(t, "mass", "max-batch")
		var eventIDs []string
		for index := 0; index < 3; index++ {
			event := acceptT05Event(t, harness, root.ID, "batch-key", "batch event", map[string]int{"index": index})
			eventIDs = append(eventIDs, event.EventID)
		}
		dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		inspection, err := harness.service.InspectTicket("mass", root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(inspection.Batches) != 2 {
			t.Fatalf("batches = %+v", inspection.Batches)
		}
		if !equalStrings(inspection.Batches[0].EventIDs, eventIDs[:2]) ||
			!equalStrings(inspection.Batches[1].EventIDs, eventIDs[2:]) {
			t.Fatalf("batch event boundaries = %+v", inspection.Batches)
		}
	})
}

func TestT05RouteKeysTargetIndependentAssignments(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticketOne := harness.rootAndChild(t, "mass", "route-keys")
	ticketTwo := harness.createTicket(t, "mass", root.ID, "route-keys-two")
	workerOne := harness.createWorker(t, "mass", root.ID, ticketOne.ID, "worker-key-one", "", "")
	workerTwo := harness.createWorker(t, "mass", root.ID, ticketTwo.ID, "worker-key-two", "", "")
	for routeKey, worker := range map[string]WorkerView{
		"route-one": workerOne,
		"route-two": workerTwo,
	} {
		if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
			ProjectID:    "mass",
			RootTicketID: root.ID,
			RouteKey:     routeKey,
			WorkerName:   worker.Name,
			AssignmentID: worker.AssignmentID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	eventOne := acceptT05Event(t, harness, root.ID, "route-one", "route one", map[string]string{"route": "one"})
	eventTwo := acceptT05Event(t, harness, root.ID, "route-two", "route two", map[string]string{"route": "two"})
	if eventOne.TargetAssignmentID != workerOne.AssignmentID ||
		eventTwo.TargetAssignmentID != workerTwo.AssignmentID ||
		eventOne.TargetSession.SessionID == eventTwo.TargetSession.SessionID {
		t.Fatalf("route targets = %+v / %+v", eventOne, eventTwo)
	}
	routes, err := harness.service.ListEventRoutes("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].RouteKey != "route-one" || routes[1].RouteKey != "route-two" {
		t.Fatalf("routes = %+v", routes)
	}
}

func TestT05NonIdleSessionsKeepEventsQueued(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "non-idle")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-idle-gate", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "idle-gate",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	event := acceptT05Event(t, harness, root.ID, "idle-gate", "wait for idle", map[string]bool{"queued": true})
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"running", "requires_action", "opening", "unknown"} {
		harness.mass.setState(worker.SessionID, state)
		clock.Advance(2 * time.Millisecond)
		if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "queued" {
			t.Fatalf("state %s event = %q, want queued", state, got)
		}
		if prompts := observedPrompts(t, harness.mass); len(prompts) != 0 {
			t.Fatalf("state %s prompted %d times", state, len(prompts))
		}
	}
	harness.mass.setState(worker.SessionID, "idle")
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "delivered" {
		t.Fatalf("idle event = %q, want delivered", got)
	}
}

func TestT05UnknownPromptIsNeverResent(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, _ := harness.rootAndChild(t, "mass", "unknown-router")
	event := acceptT05Event(t, harness, root.ID, "unknown-key", "unknown result", map[string]string{"unknown": "reply"})
	harness.mass.setDropPromptReply(true)
	dispatcher, err := NewEventDispatcher(harness.service, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err == nil || !IsErrorCode(err, "outcome_unknown") {
		t.Fatalf("dispatch error = %v, want outcome_unknown", err)
	}
	project := harness.store.Snapshot().Projects["mass"]
	if project.Inbox[event.EventID].State != "unknown" {
		t.Fatalf("unknown event = %+v", project.Inbox[event.EventID])
	}
	if len(project.Deliveries) != 1 || firstDelivery(project.Deliveries).State != "unknown" {
		t.Fatalf("deliveries = %+v", project.Deliveries)
	}
	before := len(observedPrompts(t, harness.mass))
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := len(observedPrompts(t, harness.mass)); after != before {
		t.Fatalf("unknown prompt was resent: before=%d after=%d", before, after)
	}
}

func TestT05DispatcherRestartRecoversQueuedEvents(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "restart")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-restart", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "restart-key",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	event := acceptT05Event(t, harness, root.ID, "restart-key", "survive restart", map[string]string{"restart": "yes"})

	restartedStore, err := OpenStateStore(harness.config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	restartedBridge, err := NewTicketBridge(harness.config, restartedStore)
	if err != nil {
		t.Fatal(err)
	}
	restartedService, err := NewWorkerService(harness.config, restartedStore, restartedBridge, WorkerServiceOptions{
		CapabilityDir: t.TempDir(),
		MCPCommand:    "/opt/mass-sa",
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedService.now = clock.Now
	dispatcher, err := NewEventDispatcher(restartedService, WithDispatcherClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Millisecond)
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := restartedStore.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "delivered" {
		t.Fatalf("restarted event state = %q, want delivered", got)
	}
	workerPrompts := promptsForSession(observedPrompts(t, harness.mass), worker.SessionID)
	if len(workerPrompts) != 1 || !strings.Contains(workerPrompts[0].Text, event.EventID) {
		t.Fatalf("worker prompts after restart = %+v", workerPrompts)
	}
}

func TestT05DispatcherRunUsesInjectedWait(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, _ := harness.rootAndChild(t, "mass", "wait")
	event := acceptT05Event(t, harness, root.ID, "wait-key", "run once", map[string]string{"run": "once"})
	clock.Advance(2 * time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	var waitOnce sync.Once
	waitCalls := 0
	dispatcher, err := NewEventDispatcher(harness.service,
		WithDispatcherClock(clock.Now),
		WithDispatcherWait(func(waitCtx context.Context, duration time.Duration) error {
			waitCalls++
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State == "delivered" {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			waitOnce.Do(cancel)
			return waitCtx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1", waitCalls)
	}
	if got := harness.store.Snapshot().Projects["mass"].Inbox[event.EventID].State; got != "delivered" {
		t.Fatalf("event state after Run = %q", got)
	}
}

func TestT05DispatcherKeepsOtherTargetsLiveWhileWorkerTurnIsPending(t *testing.T) {
	harness := newWorkerHarness(t)
	clock := newT05Clock()
	harness.service.now = clock.Now
	setT05Inbox(t, harness, "mass", 1, 8, 32<<10)
	root, ticket := harness.rootAndChild(t, "mass", "concurrent")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-slow", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "slow-worker",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	workerEvent := acceptT05Event(t, harness, root.ID, "slow-worker", "slow worker", map[string]string{"target": "worker"})
	clock.Advance(time.Millisecond)
	routerEvent := acceptT05Event(t, harness, root.ID, "router-while-slow", "router probe", map[string]string{"target": "router"})
	gate := harness.mass.blockPromptFinal(worker.SessionID)
	gateClosed := false
	defer func() {
		if !gateClosed {
			close(gate)
		}
	}()

	dispatcher, err := NewEventDispatcher(harness.service,
		WithDispatcherClock(clock.Now),
		WithDispatcherPollInterval(5*time.Millisecond),
		WithDispatcherMaxConcurrency(2),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	clock.Advance(2 * time.Millisecond)
	go func() { runDone <- dispatcher.Run(ctx) }()

	waitT05(t, 2*time.Second, "router delivery while worker final is pending", func() bool {
		project := harness.store.Snapshot().Projects["mass"]
		return project.Inbox[routerEvent.EventID].State == "delivered" &&
			project.Inbox[workerEvent.EventID].State == "delivered"
	})
	routerPrompts := promptsForSession(observedPrompts(t, harness.mass), harness.store.Snapshot().Projects["mass"].Router.SessionID)
	if len(routerPrompts) != 1 || !strings.Contains(routerPrompts[0].Text, routerEvent.EventID) {
		t.Fatalf("router prompts = %+v", routerPrompts)
	}
	if workerPrompts := promptsForSession(observedPrompts(t, harness.mass), worker.SessionID); len(workerPrompts) != 1 {
		t.Fatalf("worker prompts while final pending = %+v", workerPrompts)
	}

	clock.Advance(2 * time.Millisecond)
	secondWorkerEvent := acceptT05Event(t, harness, root.ID, "slow-worker", "same target queued", map[string]string{"target": "worker", "second": "yes"})
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if workerPrompts := promptsForSession(observedPrompts(t, harness.mass), worker.SessionID); len(workerPrompts) != 1 {
		t.Fatalf("same target received a duplicate in-flight prompt: %+v", workerPrompts)
	}
	if got := harness.store.Snapshot().Projects["mass"].Inbox[secondWorkerEvent.EventID].State; got != "queued" {
		t.Fatalf("second same-target event state = %q, want queued", got)
	}

	clock.Advance(2 * time.Millisecond)
	laterRouterEvent := acceptT05Event(t, harness, root.ID, "router-after-slow", "later router event", map[string]string{"target": "router", "later": "yes"})
	clock.Advance(2 * time.Millisecond)
	waitT05(t, 2*time.Second, "later router delivery while worker final is pending", func() bool {
		project := harness.store.Snapshot().Projects["mass"]
		return project.Inbox[laterRouterEvent.EventID].State == "delivered" &&
			project.Assignments[worker.AssignmentID].ExecutionState == "prompted"
	})

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("dispatcher Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop after cancellation")
	}
	close(gate)
	gateClosed = true
	project := harness.store.Snapshot().Projects["mass"]
	if project.Assignments[worker.AssignmentID].ExecutionState != "unknown" {
		t.Fatalf("canceled worker execution = %+v", project.Assignments[worker.AssignmentID])
	}
	if project.Inbox[workerEvent.EventID].State != "delivered" {
		t.Fatalf("accepted worker prompt was not preserved as delivered: %+v", project.Inbox[workerEvent.EventID])
	}
}

func waitT05(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func firstDelivery(deliveries map[string]DeliveryBatchState) DeliveryBatchState {
	for _, batch := range deliveries {
		return batch
	}
	return DeliveryBatchState{}
}
