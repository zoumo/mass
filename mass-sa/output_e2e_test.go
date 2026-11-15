package sa

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func outputTestConfig(t *testing.T, server *t06Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw, err := yaml.Marshal(server.config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	return path
}

func readTestOutputs(t *testing.T, server *t06Server, thread string, after *uint64) OutputPage {
	t.Helper()
	query := url.Values{"projectId": {"mass"}, "consumer": {"adapter"}, "source": {"test/account"}, "threadId": {thread}}
	if after != nil {
		query.Set("after", strconv.FormatUint(*after, 10))
	}
	var result OutputPage
	require.NoError(t, server.client.Request(t.Context(), "GET", "/outputs?"+query.Encode(), nil, &result))
	return result
}

func TestExternalOutputPublicLoop(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	routerID := triggerSnapshot(t, server).Router.SessionID
	mass.setState(routerID, "running")
	start := time.Now()
	trigger := acceptTestTrigger(t, server, "request", "origin")
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, trigger, acceptTestTrigger(t, server, "request", "origin"))
	reply := replyTriggerArgs{TriggerID: trigger.TriggerID, RequestID: "accept", Kind: "accepted", TicketID: ticket.ID}
	require.NotEmpty(t, router.callError(t, "reply_trigger", reply).Reason)
	clarification := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: trigger.TriggerID, RequestID: "clarify", Kind: "clarification", Text: "Which result should be delivered?"}))
	router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: trigger.TriggerID, RequestID: "answer", Kind: "no_execution", Text: "This question needs no execution."})
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: trigger.TriggerID, Action: "associate", TicketID: ticket.ID})
	accepted := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", reply))
	require.Contains(t, accepted.Summary, "confirmed Ticket "+ticket.ID)
	require.Contains(t, accepted.Summary, "Execution and completion are not yet confirmed")
	require.Equal(t, accepted, decodeT06[OutputRecord](t, router.call(t, "reply_trigger", reply)))
	beforeEcho := triggerSnapshot(t, server)
	echoReq := TriggerRequest{ProjectID: "mass", Source: "test/account", SourceEventID: "echoed-output", ThreadID: "origin", Kind: "message", Summary: "Channel echoed our acknowledgement", Content: json.RawMessage(`{}`), EchoOfOutputID: accepted.ID}
	var echo TriggerReceipt
	require.NoError(t, server.client.Request(t.Context(), "POST", "/triggers", echoReq, &echo))
	require.NoError(t, server.client.Request(t.Context(), "POST", "/triggers", echoReq, &echo))
	afterEcho := triggerSnapshot(t, server)
	require.Equal(t, "ignored", afterEcho.Triggers[echo.TriggerID].Decision)
	require.Len(t, afterEcho.Inbox, len(beforeEcho.Inbox))
	require.Len(t, afterEcho.Tickets, len(beforeEcho.Tickets))
	echoReq.SourceEventID, echoReq.ThreadID = "forged-echo", "unrelated"
	require.Error(t, server.client.Request(t.Context(), "POST", "/triggers", echoReq, &echo))
	changed := reply
	changed.Kind, changed.Text = "clarification", "changed"
	require.Equal(t, "reply_identity_conflict", router.callError(t, "reply_trigger", changed).Reason)
	changed = reply
	changed.Text = "Already executing"
	require.Equal(t, "acceptance_text_forbidden", router.callError(t, "reply_trigger", changed).Reason)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "output-owner"}))
	waitSessionReports(t, server, ticket, 1)
	workerMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, worker.AssignmentID))
	require.NotEmpty(t, workerMCP.callError(t, "reply_trigger", reply).Reason)
	require.Len(t, triggerSnapshot(t, server).Outputs, 3, "Worker final and tool results must stay internal")
	// A known Ticket's explicit communication request goes to the same Router.
	var communication TriggerReceipt
	require.NoError(t, server.client.Request(t.Context(), "POST", "/triggers", TriggerRequest{ProjectID: "mass", Source: "test/account", SourceEventID: "communication", ThreadID: "origin", Kind: "message", Summary: "Please explain the progress", Content: json.RawMessage(`{}`), ReplyRequested: true}, &communication))
	mass.setState(routerID, "idle")
	waitT06(t, 15*time.Second, "explicit communication Router final", func() bool {
		p := triggerSnapshot(t, server)
		e := p.Inbox[communication.TriggerID+"/"+ticket.ID]
		if e.State != "delivered" || e.TargetSession.SessionID != routerID {
			return false
		}
		for _, batch := range p.Deliveries {
			if batch.ExecutionID == e.ExecutionID && batch.ResultState != "" {
				return true
			}
		}
		return false
	})
	mass.setState(routerID, "running")
	require.Len(t, triggerSnapshot(t, server).Outputs, 3, "Router ordinary final must not become an output")
	// Many-to-many associations provide context without granting broadcast.
	router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "other", TicketID: ticket.ID})
	other := acceptTestTrigger(t, server, "other", "other")
	for _, state := range []string{"blocked", "waiting_user", "completed"} {
		result := decodeT06[TicketState](t, router.call(t, "finish_ticket", finishTicketArgs{TicketID: ticket.ID, Resolution: state, Summary: "Confirmed " + state}))
		require.Equal(t, state, result.BusinessState)
	}
	page := readTestOutputs(t, server, "origin", nil)
	require.Len(t, page.Outputs, 6)
	fact := page.Outputs[5]
	require.Equal(t, "ticket.completed", fact.Kind)
	require.NotEmpty(t, fact.Evidence)
	require.Empty(t, fact.Observation.Outcome, "reading is not channel delivery")
	var failedDelivery OutputConsumer
	require.NoError(t, server.client.Request(t.Context(), "POST", "/outputs/ack", OutputAckRequest{ProjectID: "mass", Consumer: "adapter", Source: "test/account", ThreadID: "origin", OutputID: fact.ID, Outcome: "failed", Reference: "channel rejected the completed notification"}, &failedDelivery))
	require.Equal(t, "completed", triggerSnapshot(t, server).Tickets[ticket.ID].BusinessState, "channel failure must not roll back accepted completion")
	require.Empty(t, readTestOutputs(t, server, "other", nil).Outputs)
	context := decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: ticket.ID}))
	require.Len(t, context.Facts, 3)
	summary := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: trigger.TriggerID, RequestID: "summary", Kind: "summary", TicketID: ticket.ID, FactID: fact.ID, Text: "The reviewed work is complete."}))
	require.Equal(t, fact.ID, summary.FactID)
	router.call(t, "subscribe_output", subscribeOutputArgs{TicketID: ticket.ID, TriggerID: other.TriggerID})
	for _, state := range []string{"reopened", "failed", "reopened", "cancelled"} {
		router.call(t, "finish_ticket", finishTicketArgs{TicketID: ticket.ID, Resolution: state, Summary: "Explicit " + state})
	}
	require.Len(t, readTestOutputs(t, server, "other", nil).Outputs, 4)
	// A source-free Ticket records business facts without a channel destination.
	internal := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Internal work", Objective: "No external source", Acceptance: "Review"}))
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: internal.ID, Resolution: "blocked", Summary: "Needs a dependency"})
	internalContext := decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: internal.ID}))
	require.Len(t, internalContext.Facts, 1)
	require.Empty(t, internalContext.Facts[0].Targets)
	configPath := outputTestConfig(t, server)
	cliArgs := []string{"--config", configPath, "--project", "mass", "--consumer", "adapter", "--source", "test/account", "--thread", "origin"}
	all := runT06JSON[OutputPage](t, append([]string{"output", "read"}, cliArgs...)...)
	require.Len(t, all.Outputs, 11)
	// Out-of-order ack cannot skip unread/unconfirmed earlier records.
	last := all.Outputs[len(all.Outputs)-1]
	runT06CLI(t, append([]string{"output", "ack", last.ID, "--outcome", "unknown", "--reference", "channel timeout; reconcile before retry"}, cliArgs...)...)
	require.Zero(t, readTestOutputs(t, server, "origin", nil).Checkpoint)
	_, _, promptsBefore := mass.operationCounts()
	for i, record := range all.Outputs[:len(all.Outputs)-1] {
		outcome := "delivered"
		if i == 0 {
			outcome = "failed"
		}
		runT06CLI(t, append([]string{"output", "ack", record.ID, "--outcome", outcome, "--reference", "adapter evidence"}, cliArgs...)...)
	}
	require.Empty(t, readTestOutputs(t, server, "origin", nil).Outputs)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Empty(t, readTestOutputs(t, server, "origin", nil).Outputs)
	var noReadAck any
	require.Error(t, server.client.Request(t.Context(), "POST", "/outputs/ack", OutputAckRequest{ProjectID: "mass", Consumer: "never-read", Source: "test/account", ThreadID: "origin", OutputID: clarification.ID, Outcome: "delivered", Reference: "forged"}, &noReadAck))
	require.Error(t, server.client.Request(t.Context(), "GET", "/outputs?projectId=other&consumer=adapter&source=test%2Faccount&threadId=origin", nil, &noReadAck))
	require.Len(t, readTestOutputs(t, server, "other", nil).Outputs, 4, "unconsumed outputs survive restart")
	require.Equal(t, accepted, decodeT06[OutputRecord](t, router.call(t, "reply_trigger", reply)))
	zero := uint64(0)
	audit := readTestOutputs(t, server, "origin", &zero)
	require.Len(t, audit.Outputs, 11)
	require.Equal(t, clarification.ID, audit.Outputs[0].ID)
	require.Equal(t, "failed", audit.Outputs[0].Observation.Outcome)
	require.Equal(t, "unknown", audit.Outputs[10].Observation.Outcome)
	var reconciled OutputConsumer
	req := OutputAckRequest{ProjectID: "mass", Consumer: "adapter", Source: "test/account", ThreadID: "origin", OutputID: last.ID, Outcome: "delivered", Reference: "channel lookup confirmed receipt"}
	require.NoError(t, server.client.Request(t.Context(), "POST", "/outputs/ack", req, &reconciled))
	require.NoError(t, server.client.Request(t.Context(), "POST", "/outputs/ack", req, &reconciled))
	req.Outcome = "unknown"
	require.Error(t, server.client.Request(t.Context(), "POST", "/outputs/ack", req, &reconciled))
	final := decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: ticket.ID}))
	require.Equal(t, "cancelled", final.Ticket.BusinessState)
	_, _, promptsAfter := mass.operationCounts()
	require.Equal(t, promptsBefore, promptsAfter)
	// A new consumer can recover the entire retained stream after restart.
	readback := runT06JSON[OutputPage](t, "output", "read", "--config", configPath, "--project", "mass", "--consumer", "replacement-adapter", "--source", "test/account", "--thread", "origin", "--limit", "1")
	require.Len(t, readback.Outputs, 1)
	require.Equal(t, clarification.ID, readback.Outputs[0].ID)
	server.stop(t)
}

func TestOutputBusinessTransitionRecoveryPublicLoop(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_status_write", true: "lost_status_confirmation"}[afterWrite], func(t *testing.T) {
			mass := newFakeMASSServer(t, "")
			fake := newFakeMultica(t)
			project := newTestProject("mass")
			project.CWD = t.TempDir()
			project.Multica = ProjectMultica{WorkspaceID: fake.workspace, ProjectID: fake.project}
			config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
			config.Multica.Executable = fakeMulticaBinary
			config.Management.Listen = freeLoopbackAddress(t)
			config.Management.TokenFile = filepath.Join(t.TempDir(), "token")
			require.NoError(t, os.WriteFile(config.Management.TokenFile, []byte("test-management"), 0600))
			server := startT06Server(t, config)
			waitT06(t, 15*time.Second, "Router ready", func() bool { return triggerSnapshot(t, server).Router != nil })
			router := startT06MCP(t, "mass", "router", triggerSnapshot(t, server).Router.CapabilityFile)
			mass.setState(triggerSnapshot(t, server).Router.SessionID, "running")
			trigger := acceptTestTrigger(t, server, "finish", "origin")
			owned := decodeT06[TriggerState](t, router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: trigger.TriggerID, Action: "create", Title: "Recover output", Objective: "Persist completion", Acceptance: "Explicit review"}))
			ticket := triggerSnapshot(t, server).Tickets[owned.TicketIDs[0]]
			worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "worker"}))
			workerMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, worker.AssignmentID))
			waitSessionReports(t, server, ticket, 1)
			server.stop(t)
			t.Setenv("FAKE_MULTICA_FAIL", "status")
			if afterWrite {
				t.Setenv("FAKE_MULTICA_FAIL_AFTER_WRITE", "1")
			}
			server = startT06Server(t, config)
			args := finishTicketArgs{TicketID: ticket.ID, Resolution: "completed", Summary: "Reviewed and accepted work"}
			require.NotEmpty(t, router.callError(t, "finish_ticket", args).Reason)
			pending := triggerSnapshot(t, server).Tickets[ticket.ID]
			require.NotNil(t, pending.Transition)
			require.True(t, pending.Transition.StatusPending)
			require.False(t, terminalBusinessState(pending.BusinessState))
			require.Empty(t, readTestOutputs(t, server, "origin", nil).Outputs)
			statusCalls := len(fake.invocationsOf("issue", "status"))
			server.stop(t)
			t.Setenv("FAKE_MULTICA_FAIL", "")
			t.Setenv("FAKE_MULTICA_FAIL_AFTER_WRITE", "")
			server = startT06Server(t, config)
			// Management health precedes startup reconciliation; await its read.
			waitT06(t, 15*time.Second, "startup status readback", func() bool {
				return strings.Contains(server.logs(), "business transition still needs reconciliation") || strings.Contains(server.logs(), "router ready")
			})
			result := readTestOutputs(t, server, "origin", nil)
			if afterWrite {
				require.Len(t, result.Outputs, 1)
				require.Equal(t, "ticket.completed", result.Outputs[0].Kind)
				require.Equal(t, "completed", triggerSnapshot(t, server).Tickets[ticket.ID].BusinessState)
				require.Equal(t, "capability_invalid", workerMCP.callError(t, "list_workers", emptyArgs{}).Reason)
				runT06CLI(t, "ticket", "reconcile", ticket.ID, "--config", outputTestConfig(t, server), "--project", "mass")
				require.Len(t, readTestOutputs(t, server, "origin", nil).Outputs, 1)
			} else {
				require.Empty(t, result.Outputs)
				var reconcile any
				require.Error(t, server.client.Request(t.Context(), "POST", "/tickets/"+ticket.ID+"/reconcile", map[string]string{"projectId": "mass"}, &reconcile))
				require.Equal(t, "ticket_transition_unconfirmed", router.callError(t, "finish_ticket", args).Reason)
			}
			require.Len(t, fake.invocationsOf("issue", "status"), statusCalls, "recovery must not repeat external status writes")
			server.stop(t)
		})
	}
}
