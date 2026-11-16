package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic test-only rows exercise every view without creating real work or
// executing an Agent. Atomic admission/decision behavior is tested separately
// through the public serve + management + MCP processes below.
func seedObserverTriggerFixture(t *testing.T, config Config) {
	t.Helper()
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	bridge, err := NewTicketBridge(config, store)
	require.NoError(t, err)
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{CapabilityDir: t.TempDir(), MCPCommand: "/fixture/mass-sa"})
	require.NoError(t, err)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	sequence := 0
	service.now = func() time.Time { sequence++; return at.Add(time.Duration(sequence) * time.Second) }
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		p.Tickets["ticket-a"] = TicketState{ID: "ticket-a", ProjectID: "alpha", Title: "Fixture work A", RootTicketID: "ticket-a", AssignmentID: "assignment-current"}
		p.Tickets["ticket-b"] = TicketState{ID: "ticket-b", ProjectID: "alpha", Title: "Fixture work B", RootTicketID: "ticket-b"}
		p.Assignments["assignment-current"] = AssignmentState{ID: "assignment-current", TicketID: "ticket-a", Session: SessionRef{SessionID: "session-current"}}
		linkThread(p, "fixture/account", "direct", "ticket-a")
		linkThread(p, "fixture/account", "ambiguous", "ticket-a")
		linkThread(p, "fixture/account", "ambiguous", "ticket-b")
		p.Outputs["fixture-output"] = OutputRecord{ID: "fixture-output", Targets: []OutputTarget{{Source: "fixture/account", ThreadID: "echo"}}}
		return nil
	}))
	accept := func(event, thread, summary string, reply bool, echo string) TriggerReceipt {
		receipt, err := service.AcceptTrigger(TriggerRequest{ProjectID: "alpha", Source: "fixture/account", SourceEventID: event, ThreadID: thread, Kind: "message", Summary: summary, Content: json.RawMessage(`{"text":"<img src=https://invalid.example/remote onerror=alert(1)> token=fixture-source-secret"}`), ReplyRequested: reply, EchoOfOutputID: echo})
		require.NoError(t, err)
		return receipt
	}
	both := accept("two-stage", "new-thread", "Router 后交 Worker", false, "")
	peer := accept("batch-peer", "direct", "同批另一输入", false, "")
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		event := p.Inbox[both.TriggerID+"/"]
		event.State, event.TargetSession, event.ExecutionID = "delivered", SessionRef{SessionID: "fixture-router-session"}, "execution-router"
		p.Inbox[event.ID] = event
		p.Deliveries["batch-router"] = DeliveryBatchState{ID: "batch-router", State: "delivered", RoutingPath: "router", TargetSession: event.TargetSession, ExecutionID: event.ExecutionID, EventIDs: []string{event.ID}, CreatedAt: service.now(), UpdatedAt: service.now()}
		return nil
	}))
	_, err = service.decideTrigger(t.Context(), "alpha", decideTriggerArgs{TriggerID: both.TriggerID, Action: "associate", TicketID: "ticket-a"}, "fixture-capability-secret")
	require.NoError(t, err)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		ids := []string{both.TriggerID + "/ticket-a", peer.TriggerID + "/ticket-a"}
		for _, id := range ids {
			event := p.Inbox[id]
			event.State, event.TargetAssignmentID, event.TargetSession, event.ExecutionID = "unknown", "assignment-old", SessionRef{SessionID: "session-old"}, "execution-worker"
			event.ErrorReason = "prompt_outcome_unknown"
			p.Inbox[id] = event
		}
		p.Inbox["internal-child"] = InboxEventState{ID: "internal-child", ChildLifecycle: &ChildLifecycleFact{}, TicketID: "ticket-a"}
		p.Deliveries["batch-worker"] = DeliveryBatchState{ID: "batch-worker", State: "unknown", RoutingPath: "direct", TargetAssignmentID: "assignment-old", TargetSession: SessionRef{SessionID: "session-old"}, ExecutionID: "execution-worker", EventIDs: append(ids, "internal-child"), ErrorReason: "prompt_outcome_unknown", CreatedAt: service.now(), UpdatedAt: service.now()}
		return nil
	}))
	accept("pending", "direct", "排队等待执行者", false, "")
	blocked := accept("blocked", "direct", "投递前阻塞", false, "")
	blockedEvent := store.Snapshot().Projects["alpha"].Inbox[blocked.TriggerID+"/ticket-a"]
	require.NoError(t, service.createBlockedBatch("alpha", queuedDeliveryGroup{rootTicketID: "ticket-a", routingPath: "direct", targetAssignmentID: "assignment-current", targetSession: SessionRef{SessionID: "session-current"}}, []InboxEventState{blockedEvent}, "worker_session_missing"))
	accept("ambiguous", "ambiguous", "歧义关联待判断", false, "")
	accept("reply", "direct", "显式请求 Router 回复", true, "")
	accept("echo", "echo", "已证实回流忽略", false, "fixture-output")
	unowned := accept("unowned", "unowned", "无关联待判断", false, "")
	_, err = service.decideTrigger(t.Context(), "alpha", decideTriggerArgs{TriggerID: unowned.TriggerID, Action: "ignore"}, "")
	require.NoError(t, err)
	for _, decision := range []string{"creating", "unknown", "pending"} {
		require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
			id := "legacy-" + decision
			p.Triggers[id] = TriggerState{TriggerReceipt: TriggerReceipt{TriggerID: id, ProjectID: "alpha", Source: "legacy/account", SourceEventID: id, ThreadID: "direct", AcceptedAt: service.now()}, Kind: "message", Summary: "旧记录 " + decision, Decision: decision, Content: json.RawMessage(`{"text":"legacy source"}`)}
			return nil
		}))
	}
}

func TestObserverTriggerPublicProjectionAndTraversal(t *testing.T) {
	config, mass := observerFixture(t)
	seedObserverTriggerFixture(t, config)
	base, configPath := startObserverProcess(t, config)
	before, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	all := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers")
	require.Len(t, all.Items, 11)
	var two, peer, pending ObserverTrigger
	for _, item := range all.Items {
		switch item.SourceEventID {
		case "two-stage":
			two = item
		case "batch-peer":
			peer = item
		case "pending":
			pending = item
		}
	}
	require.Equal(t, []string{"router", "direct"}, two.Routes)
	require.Equal(t, "assignment-current", two.Tickets[0].CurrentAssignmentID)
	detailPath := "triggers/" + two.TriggerID + "?project=alpha"
	detail := observerGet[ObserverTriggerDetail](t, base, detailPath)
	require.Len(t, detail.Deliveries, 2)
	require.Equal(t, "no_association", detail.Admission.Reason)
	require.Empty(t, detail.Admission.MatchedTicketIDs)
	require.Equal(t, "router_session", detail.Decisions[0].Actor.Kind)
	require.Equal(t, "fixture-router-session", detail.Decisions[0].Actor.SessionID)
	require.Equal(t, "fixture-router-session", detail.Deliveries[0].SessionID)
	require.Empty(t, detail.Deliveries[0].AssignmentID)
	require.Equal(t, "assignment-old", detail.Deliveries[1].AssignmentID)
	require.Equal(t, "session-old", detail.Deliveries[1].SessionID)
	require.Equal(t, "unknown", detail.Deliveries[1].State)
	inputs := detail.Deliveries[1].Batches[0].Inputs
	require.Len(t, inputs, 3)
	require.Equal(t, peer.TriggerID, inputs[1].TriggerID)
	require.Equal(t, "child_lifecycle", inputs[2].Kind)
	require.Empty(t, inputs[2].TriggerID)
	raw, err := json.Marshal(detail)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fixture-source-secret")
	require.NotContains(t, string(raw), "fixture-capability-secret")
	cli := runT06JSON[ObserverTriggerDetail](t, "observe", "triggers", two.TriggerID, "--project", "alpha", "--config", configPath)
	cli.ObservedAt = detail.ObservedAt
	require.Equal(t, detail, cli)
	cliList := runT06JSON[ObserverPage[ObserverTrigger]](t, "observe", "triggers", "--project", "alpha", "--config", configPath)
	require.Equal(t, all.Items, cliList.Items)
	queued := observerGet[ObserverTriggerDetail](t, base, "triggers/"+pending.TriggerID+"?project=alpha")
	require.Equal(t, "unique_association", queued.Admission.Reason)
	require.False(t, queued.Deliveries[0].Attempted)
	require.Equal(t, "pending_executor", queued.Deliveries[0].TargetResolution)
	require.Empty(t, queued.Deliveries[0].AssignmentID)
	require.Empty(t, queued.Deliveries[0].SessionID)
	legacy := observerGet[ObserverTriggerDetail](t, base, "triggers/legacy-pending?project=alpha")
	require.False(t, legacy.HistoryRecorded)
	require.Nil(t, legacy.Admission)
	require.Equal(t, "history_not_recorded", legacy.EntryReason)
	for _, scenario := range []struct{ event, reason string }{{"ambiguous", "ambiguous_association"}, {"reply", "reply_requested"}, {"echo", "proven_output_echo"}} {
		list := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?search="+scenario.event)
		require.NotEmpty(t, list.Items)
		found := false
		for _, row := range list.Items {
			if row.SourceEventID == scenario.event {
				found = true
				require.Equal(t, scenario.reason, row.EntryReason)
			}
		}
		require.True(t, found)
	}
	ignored := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?decision=ignored")
	require.Len(t, ignored.Items, 2)
	filtered := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?project=alpha&source=fixture%2Faccount&thread=direct&route=direct")
	require.Len(t, filtered.Items, 3)
	timed := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?"+url.Values{"from": {all.Items[len(all.Items)-1].AcceptedAt.Format(time.RFC3339Nano)}, "to": {all.Items[len(all.Items)-1].AcceptedAt.Format(time.RFC3339Nano)}}.Encode())
	require.Len(t, timed.Items, 1)
	for _, path := range []string{"triggers?project=missing", "triggers/" + two.TriggerID + "?project=beta", "triggers/missing?project=alpha"} {
		response, err := http.Get(base + "/observer/api/" + path)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, 404, response.StatusCode)
	}
	for _, path := range []string{"triggers/" + two.TriggerID, "triggers?source=a&source=b", "triggers?from=yesterday", "triggers?from=2026-11-17T00:00:00Z&to=2026-11-16T00:00:00Z", "triggers?rpc=session.prompt", "triggers?limit=201", "triggers?limit=0", "triggers?cursor=bad", "triggers/" + two.TriggerID + "?project=alpha&search=forbidden", "routers?source=forbidden"} {
		response, err := http.Get(base + "/observer/api/" + path)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, 400, response.StatusCode, path)
	}
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "observation must not mutate state, consumers or checkpoints")
	mass.mu.Lock()
	operations := append([]string{}, mass.operations...)
	mass.mu.Unlock()
	require.Empty(t, operations, "local Trigger reads need no massd call")

	// Start traversal, append through the real authenticated management endpoint,
	// then finish the original frozen traversal without duplication or new rows.
	first := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?limit=2")
	client, err := NewManagementClient(config)
	require.NoError(t, err)
	var accepted TriggerReceipt
	require.NoError(t, client.Request(t.Context(), "POST", "/triggers", TriggerRequest{ProjectID: "alpha", Source: "new/account", SourceEventID: "new", ThreadID: "new", Kind: "message", Summary: "arrived during pagination", Content: json.RawMessage(`{}`)}, &accepted))
	ids := []string{}
	page := first
	for {
		for _, row := range page.Items {
			ids = append(ids, row.TriggerID)
		}
		if page.Next == "" {
			break
		}
		page = observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?limit=2&cursor="+url.QueryEscape(page.Next))
	}
	expected := []string{}
	for _, row := range all.Items {
		expected = append(expected, row.TriggerID)
	}
	require.Equal(t, expected, ids)
	fresh := observerGet[ObserverPage[ObserverTrigger]](t, base, "triggers?limit=2")
	require.Equal(t, accepted.TriggerID, fresh.Items[0].TriggerID)
	response, err := http.Get(base + "/observer/api/triggers?limit=2&project=alpha&cursor=" + url.QueryEscape(first.Next))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 400, response.StatusCode)
}

func TestObserverTriggerPublicAtomicHistoryAndReplacement(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	base := "http://" + server.config.Management.Listen
	routerID := triggerSnapshot(t, server).Router.SessionID
	mass.setState(routerID, "idle")
	trigger := acceptTestTrigger(t, server, "two-stage", "discussion")
	waitT06(t, 15*time.Second, "Router input accepted", func() bool { return triggerSnapshot(t, server).Inbox[trigger.TriggerID+"/"].State == "delivered" })
	initial := observerGet[ObserverTriggerDetail](t, base, "triggers/"+trigger.TriggerID+"?project=mass")
	require.Equal(t, "no_association", initial.Admission.Reason)
	require.Equal(t, "service", initial.Admission.Actor.Kind)
	require.Empty(t, initial.Tickets)
	beforeRejected := triggerSnapshot(t, server).Triggers[trigger.TriggerID]
	router.callError(t, "decide_trigger", decideTriggerArgs{TriggerID: trigger.TriggerID, Action: "associate", TicketID: "missing"})
	require.Equal(t, beforeRejected, triggerSnapshot(t, server).Triggers[trigger.TriggerID], "rejected change cannot leave a decision record")
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: trigger.TriggerID, Action: "associate", TicketID: ticket.ID})
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "trace-owner"}))
	waitSessionReports(t, server, ticket, 1)
	detail := observerGet[ObserverTriggerDetail](t, base, "triggers/"+trigger.TriggerID+"?project=mass")
	require.Equal(t, initial.Admission, detail.Admission)
	require.Len(t, detail.Decisions, 1)
	require.Equal(t, "associated", detail.Decisions[0].Decision)
	require.Equal(t, []string{ticket.ID}, detail.Decisions[0].MatchedTicketIDs)
	require.Equal(t, TriggerActor{Kind: "router_session", SessionID: routerID}, detail.Decisions[0].Actor)
	require.Len(t, detail.Deliveries, 2)
	require.Equal(t, routerID, detail.Deliveries[0].SessionID)
	require.Equal(t, worker.AssignmentID, detail.Deliveries[1].AssignmentID)
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "trace-replacement", Action: "replace"}))
	later := observerGet[ObserverTriggerDetail](t, base, "triggers/"+trigger.TriggerID+"?project=mass")
	require.Equal(t, replacement.AssignmentID, later.Tickets[0].CurrentAssignmentID)
	require.Equal(t, detail.Deliveries, later.Deliveries)
	require.Equal(t, detail.Decisions, later.Decisions)
	// The admission match remains empty even after Thread association changes.
	require.Empty(t, later.Admission.MatchedTicketIDs)
	queued := acceptTestTrigger(t, server, "direct", "discussion")
	direct := observerGet[ObserverTriggerDetail](t, base, "triggers/"+queued.TriggerID+"?project=mass")
	require.Equal(t, "unique_association", direct.Admission.Reason)
	require.Equal(t, []string{ticket.ID}, direct.Admission.MatchedTicketIDs)
	require.Equal(t, queued, acceptTestTrigger(t, server, "direct", "discussion"))
	require.Equal(t, direct.Admission, observerGet[ObserverTriggerDetail](t, base, "triggers/"+queued.TriggerID+"?project=mass").Admission)
	waitT06(t, 15*time.Second, "direct input settled", func() bool {
		return triggerSnapshot(t, server).Inbox[queued.TriggerID+"/"+ticket.ID].State == "delivered"
	})
	mass.setDropPromptReply(true)
	unknownInput := acceptTestTrigger(t, server, "unknown-delivery", "discussion")
	waitT06(t, 15*time.Second, "unknown delivery fixed evidence", func() bool {
		return triggerSnapshot(t, server).Inbox[unknownInput.TriggerID+"/"+ticket.ID].State == "unknown"
	})
	unknownDelivery := observerGet[ObserverTriggerDetail](t, base, "triggers/"+unknownInput.TriggerID+"?project=mass")
	require.True(t, unknownDelivery.Deliveries[0].Attempted)
	require.Equal(t, replacement.AssignmentID, unknownDelivery.Deliveries[0].AssignmentID)
	require.Equal(t, replacement.SessionID, unknownDelivery.Deliveries[0].SessionID)
	require.Equal(t, "unknown", unknownDelivery.Deliveries[0].Batches[0].State)
	require.NotEmpty(t, unknownDelivery.Deliveries[0].ErrorReason)
	mass.setDropPromptReply(false)
	server.stop(t)
	server = startT06Server(t, server.config)
	persisted := observerGet[ObserverTriggerDetail](t, base, "triggers/"+trigger.TriggerID+"?project=mass")
	require.Equal(t, later.Admission, persisted.Admission)
	require.Equal(t, later.Decisions, persisted.Decisions)
	require.Equal(t, later.Deliveries, persisted.Deliveries)
	createdInput := acceptTestTrigger(t, server, "create-traced", "new-work")
	args := decideTriggerArgs{TriggerID: createdInput.TriggerID, Action: "create", Title: "Observed creation", Objective: "Create scoped work", Acceptance: "Explicit acceptance"}
	created := decodeT06[TriggerState](t, router.call(t, "decide_trigger", args))
	require.Equal(t, "created", created.Decision)
	require.Len(t, created.Decisions, 2)
	require.Equal(t, "creating", created.Decisions[0].Decision)
	require.Equal(t, "created", created.Decisions[1].Decision)
	require.Equal(t, created.TicketIDs, created.Decisions[1].MatchedTicketIDs)
	require.Equal(t, created.Decisions[0].Actor, created.Decisions[1].Actor)
	require.Equal(t, created, decodeT06[TriggerState](t, router.call(t, "decide_trigger", args)))
	server.stop(t)
}

func TestObserverTriggerCancelledCreationReadback(t *testing.T) {
	config, _ := observerFixture(t)
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	bridge, err := NewTicketBridge(config, store)
	require.NoError(t, err)
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{CapabilityDir: t.TempDir(), MCPCommand: "/opt/mass-sa"})
	require.NoError(t, err)
	receipt, err := service.AcceptTrigger(TriggerRequest{ProjectID: "alpha", Source: "fixture", SourceEventID: "cancelled-create", ThreadID: "uncertain", Kind: "message", Summary: "Cancelled local creation", Content: json.RawMessage(`{}`)})
	require.NoError(t, err)
	args := decideTriggerArgs{TriggerID: receipt.TriggerID, Action: "create", Title: "Cancelled creation", Objective: "Test durable unknown evidence", Acceptance: "No fabricated Ticket"}
	// Cancellation fences the decision, then prevents the local Ticket write.
	// Public HTTP/CLI readback below verifies the actual persisted outcome.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = service.decideTrigger(ctx, "alpha", args, "")
	require.ErrorIs(t, err, context.Canceled)
	base, path := startObserverProcess(t, config)
	uncertainInput := receipt
	uncertain := observerGet[ObserverTriggerDetail](t, base, "triggers/"+uncertainInput.TriggerID+"?project=alpha")
	require.Equal(t, "unknown", uncertain.Decision)
	require.Len(t, uncertain.Decisions, 2)
	require.Equal(t, "creating", uncertain.Decisions[0].Decision)
	require.Equal(t, "unknown", uncertain.Decisions[1].Decision)
	require.Empty(t, uncertain.Tickets)
	_, err = service.decideTrigger(t.Context(), "alpha", args, "")
	require.Error(t, err)
	cli := runT06JSON[ObserverTriggerDetail](t, "observe", "triggers", receipt.TriggerID, "--project", "alpha", "--config", path)
	require.Equal(t, uncertain.Decisions, cli.Decisions)
	reopened, _ := startObserverProcess(t, configWithNewAddress(t, config))
	require.Equal(t, uncertain.Decisions, observerGet[ObserverTriggerDetail](t, reopened, "triggers/"+receipt.TriggerID+"?project=alpha").Decisions)
}

func TestObserverTriggerSnapshotIsolationAndDecisionPagination(t *testing.T) {
	h := newWorkerHarness(t)
	service := h.service
	accept := func(id string) TriggerReceipt {
		receipt, err := service.AcceptTrigger(TriggerRequest{ProjectID: "mass", Source: "fixture", SourceEventID: id, ThreadID: "thread", Kind: "message", Summary: id, Content: json.RawMessage(`{}`)})
		require.NoError(t, err)
		return receipt
	}
	a, b := accept("first"), accept("second")
	page, err := service.observer.triggerPage(h.store.Snapshot(), url.Values{"decision": {"pending"}, "limit": {"1"}})
	require.NoError(t, err)
	require.NotEmpty(t, page.Next)
	// Direct in-process callers have no confirmed Router Session credential.
	decided, err := service.decideTrigger(t.Context(), "mass", decideTriggerArgs{TriggerID: a.TriggerID, Action: "ignore"}, "")
	require.NoError(t, err)
	require.Equal(t, "operator", decided.Decisions[0].Actor.Kind)
	snapshot := h.store.Snapshot()
	changed := snapshot.Projects["mass"].Triggers[a.TriggerID]
	changed.Admission.MatchedTicketIDs = append(changed.Admission.MatchedTicketIDs, "forged")
	changed.Decisions[0].Actor.Kind = "forged"
	require.Equal(t, decided, h.store.Snapshot().Projects["mass"].Triggers[a.TriggerID], "snapshots must detach new history slices and pointers")
	next, err := service.observer.triggerPage(h.store.Snapshot(), url.Values{"decision": {"pending"}, "limit": {"1"}, "cursor": {page.Next}})
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.Equal(t, a.TriggerID, next.Items[0].TriggerID)
	require.Equal(t, "pending", next.Items[0].Decision)
	require.Equal(t, b.TriggerID, page.Items[0].TriggerID)
	_, err = service.observer.triggerPage(h.store.Snapshot(), url.Values{"decision": {"pending"}, "limit": {"1"}, "cursor": {page.Next + "broken"}})
	require.Error(t, err)
	service.observer.mu.Lock()
	for key, traversal := range service.observer.triggerPages {
		traversal.At = time.Now().Add(-16 * time.Minute)
		service.observer.triggerPages[key] = traversal
	}
	service.observer.mu.Unlock()
	_, err = service.observer.triggerPage(h.store.Snapshot(), url.Values{"decision": {"pending"}, "limit": {"1"}, "cursor": {page.Next}})
	require.Error(t, err)
	// Failed persistence must publish neither the Trigger nor its history.
	stateBefore := h.store.Snapshot()
	service.store.path = filepath.Join(t.TempDir(), "directory")
	require.NoError(t, os.Mkdir(service.store.path, 0700))
	_, err = service.AcceptTrigger(TriggerRequest{ProjectID: "mass", Source: "fixture", SourceEventID: "cannot-persist", ThreadID: "thread", Kind: "message", Summary: "failed", Content: json.RawMessage(`{}`)})
	require.Error(t, err)
	require.Equal(t, stateBefore, h.store.Snapshot())
	_, err = service.decideTrigger(t.Context(), "mass", decideTriggerArgs{TriggerID: b.TriggerID, Action: "ignore"}, "")
	require.Error(t, err)
	require.Equal(t, stateBefore, h.store.Snapshot())
}

func TestObserverTriggerPublicBlockedBeforePrompt(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "blocked", TicketID: ticket.ID})
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "blocked-owner"}))
	_, _, before := mass.operationCounts()
	mass.setGetError(&OperationError{Code: "not_found", Reason: "injected_session_absent", Message: "fixture absent"})
	trigger := acceptTestTrigger(t, server, "blocked", "blocked")
	waitT06(t, 15*time.Second, "preflight blocked without prompt", func() bool {
		return triggerSnapshot(t, server).Inbox[trigger.TriggerID+"/"+ticket.ID].State == "blocked"
	})
	base := "http://" + server.config.Management.Listen
	detail := observerGet[ObserverTriggerDetail](t, base, "triggers/"+trigger.TriggerID+"?project=mass")
	require.Len(t, detail.Deliveries, 1)
	delivery := detail.Deliveries[0]
	require.False(t, delivery.Attempted)
	require.Equal(t, "blocked_before_attempt", delivery.TargetResolution)
	require.Empty(t, delivery.AssignmentID)
	require.Empty(t, delivery.SessionID)
	require.Empty(t, delivery.ExecutionID)
	require.Len(t, delivery.Batches, 1)
	batch := delivery.Batches[0]
	require.False(t, batch.Attempted)
	require.Equal(t, "pre_dispatch_candidate", batch.TargetResolution)
	require.Equal(t, worker.AssignmentID, batch.AssignmentID)
	require.Equal(t, worker.SessionID, batch.SessionID)
	require.Empty(t, batch.ExecutionID)
	require.Equal(t, "worker_session_missing", batch.ErrorReason)
	_, _, after := mass.operationCounts()
	require.Equal(t, before, after, "preflight block must not call prompt")
	beforeRead, err := os.ReadFile(server.config.StateFile)
	require.NoError(t, err)
	cli := runT06JSON[ObserverTriggerDetail](t, "observe", "triggers", trigger.TriggerID, "--project", "mass", "--config", outputTestConfig(t, server))
	cli.ObservedAt = detail.ObservedAt
	require.Equal(t, detail, cli)
	afterRead, err := os.ReadFile(server.config.StateFile)
	require.NoError(t, err)
	require.Equal(t, beforeRead, afterRead)
	server.stop(t)
}
