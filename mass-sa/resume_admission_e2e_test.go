package sa

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfirmedResumeBeforeStateObservationPublicLoop(t *testing.T) {
	for _, lostAcceptance := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_fresh_input", true: "unknown_acceptance"}[lostAcceptance], func(t *testing.T) {
			mass, server, router, ticket := sessionContinuityServer(t)
			worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "owner"}))
			router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Complete the original round before close"})
			waitSessionReports(t, server, ticket, 1)
			oldMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, worker.AssignmentID))
			oldMCP.call(t, "get_ticket_context", ticketContextArgs{})
			router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "resumed", TicketID: ticket.ID})
			router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name})
			mass.setResumeResponseState("unknown")
			resumed := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name}))
			require.Equal(t, worker.SessionID, resumed.SessionID)
			require.NotEqual(t, worker.AssignmentID, resumed.AssignmentID)
			require.Equal(t, "unknown", resumed.State, "identity confirmation does not invent an idle observation")
			require.Equal(t, "capability_invalid", oldMCP.callError(t, "get_ticket_context", ticketContextArgs{}).Reason)
			continued := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name}))
			require.Equal(t, resumed.AssignmentID, continued.AssignmentID, "no new renewal before the first input")
			current := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, resumed.AssignmentID))
			current.call(t, "get_ticket_context", ticketContextArgs{})
			server.stop(t)
			server = startT06Server(t, server.config)
			mass.setDropPromptReply(lostAcceptance)
			input := acceptTestTrigger(t, server, "first-after-resume", "resumed")
			state := "delivered"
			if lostAcceptance {
				state = "unknown"
			}
			waitT06(t, 15*time.Second, "fresh resumed input "+state, func() bool {
				return triggerSnapshot(t, server).Inbox[input.TriggerID+"/"+ticket.ID].State == state
			})
			mass.setDropPromptReply(false)
			if !lostAcceptance {
				waitSessionReports(t, server, ticket, 2)
				mass.setState(worker.SessionID, "unknown")
			}
			before := len(promptsForSession(observedPrompts(t, mass), worker.SessionID))
			require.Equal(t, 2, before, "old accepted work was not replayed")
			server.stop(t)
			server = startT06Server(t, server.config)
			require.Equal(t, input, acceptTestTrigger(t, server, "first-after-resume", "resumed"))
			after := acceptTestTrigger(t, server, "later-unknown-observation", "resumed")
			time.Sleep(400 * time.Millisecond)
			require.Equal(t, "queued", triggerSnapshot(t, server).Inbox[after.TriggerID+"/"+ticket.ID].State)
			require.Len(t, promptsForSession(observedPrompts(t, mass), worker.SessionID), before)
			reason := "worker_not_quiescent"
			if lostAcceptance {
				reason = "worker_execution_unconfirmed"
			}
			require.Equal(t, reason, router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}).Reason)
			server.stop(t)
		})
	}
}
