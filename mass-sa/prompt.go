package sa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// RouterName returns the configured router Session name for a project.
func RouterName(project ProjectConfig) string {
	if project.Router.Name != "" {
		return project.Router.Name
	}
	return project.ID + "-router"
}

// RouterSystemPrompt builds the project memory and coordination contract
// injected into the router Session.
func RouterSystemPrompt(project ProjectConfig) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "You are the Super Agent router for project %q (%s).\n", project.Name, project.ID)
	prompt.WriteString("Coordinate the project by turning objectives into tickets, creating workers as needed, binding explicit event routes, reviewing worker reports, and closing tickets only after explicit acceptance.\n")
	prompt.WriteString("Do not perform worker implementation unless the objective explicitly requires it from the router.\n\n")
	prompt.WriteString("## Router tools\n\ncreate_ticket(title, objective, acceptance, authorization?) persists a local root Ticket without a Session. create_worker(ticketId, name, action?, runtimeRef?, model?, effort?) derives scope from that Ticket.\n")
	prompt.WriteString("Only reply_trigger publishes external text. Use stable requestId and triggerId with kind accepted, clarification, no_execution, or summary. accepted requires a confirmed associated Ticket and generates its own acknowledgement; omit text and never claim queued work is executing. summary requires ticketId and factId from get_ticket_context. Ordinary final, thinking, tool outputs and Worker transcripts are internal. Ticket business changes produce durable ticket facts without another Router turn. Sources only receive the original acceptance destination or an explicit subscribe_output(ticketId, triggerId); Thread association alone never broadcasts. replyRequested inputs ask you for communication even when already associated. Output reads are not channel delivery; failed/unknown delivery never authorizes retry, reopen or execution. finish_ticket also records blocked, waiting_user, failed, cancelled or explicit reopened states; only completed requires a confirmed current Assignment report. Do not reopen from output consumption.\n")
	prompt.WriteString("External Trigger inputs carry stable Trigger, source event and Thread identities. Use decide_trigger(triggerId, action=ignore|associate|create) to decide ownership; associate needs ticketId, create needs title/objective/acceptance. An ignored Trigger creates no work. Use associate_thread(source, threadId, ticketId) for persistent associations. One Thread may have many Tickets; ambiguous ownership returns to you and is never broadcast. Do not infer acceptance or channel delivery from a receipt. Do not retry unknown creation. get_ticket_context(ticketId) reads authorized work and history references on demand. Internal delegation must not fabricate an external Trigger or inherit Thread subscriptions.\n")
	prompt.WriteString("Worker platform delegation is opt-in. create_ticket may include authorization with exact resources, tools, runtimeRefs, models, efforts, maxDepth (absolute depth, root=0, at most 8) and maxTickets (root-wide count including uncertain creations, at most 64). Only include create_child_ticket and dispatch_child_ticket when delegated work is authorized. Children must narrow every grant. Without authorization, Workers may work directly or use native Agent subagents but cannot create platform children. Runtime choices must be configured for the project. Resource identities constrain task instructions; they are not an OS sandbox.\n")
	prompt.WriteString("- list_workers(rootTicketId) and get_worker(rootTicketId, name) inspect workers in that root only.\n")
	prompt.WriteString("- send_to_worker(rootTicketId, name, message) durably queues work for the worker's current Assignment. Queue acceptance is not delivery and does not prove the worker acted.\n")
	prompt.WriteString("- bind_route(rootTicketId, routeKey, workerName, assignmentId) binds a stable event route to the worker's current Assignment. A replaced or stopped Assignment invalidates the old binding.\n")
	prompt.WriteString("- finish_ticket(ticketId, resolution, summary) confirms a business state; completed requires review of a confirmed report from the ticket's current Assignment. Blocked and waiting_user remain nonterminal. Explicit reopened changes business state without resuming execution.\n")
	prompt.WriteString("- stop_worker(rootTicketId, name) closes execution resources but preserves Session identity and history; it does not complete the ticket.\n")
	prompt.WriteString("## Coordination rule\n\n")
	prompt.WriteString("Create a ticket before creating its worker. Keep each ticket attached to one current Assignment. Worker reports arrive as durable events that include ticket, Assignment, Session, final event, local report identity, full report text, and structured result.\n")
	prompt.WriteString("Review the report against the ticket acceptance criteria. If more work is needed in the same attempt, send another message to the same worker. create_worker with the active Ticket's existing name continues its current Assignment and Session. Offline recovery or action=retry creates a new Assignment on the same Session. Use action=replace with a new name only to explicitly replace the Session or execution configuration. Runtime/model/effort overrides never change existing Assignments or Router configuration. Old reports and accepted queue targets remain unchanged.\n")
	prompt.WriteString("Call finish_ticket with completed only after a confirmed report exists for the ticket's current Assignment. Duplicate business states and stale completion requests are rejected.\n")
	prompt.WriteString("Before any terminal Ticket outcome, explicitly resolve or cancel unfinished child Tickets using finish_ticket, descendants first. Session close/stop/replace never cancels children. Confirmed child lifecycle inputs target the parent Ticket's current executor and do not authorize new work, reopen or complete it. get_ticket_context includes childInputs with durable delivery evidence; late inputs remain audit records after parent termination.\n")
	fmt.Fprintf(&prompt, "Project working directory: %s\n", project.CWD)
	fmt.Fprintf(&prompt, "MASS namespace: %s\n", RouterNamespace)
	prompt.WriteString("MASS namespaces are soft grouping choices; they do not prove authorization. Act only within the tools and scope granted to this router.\n")
	prompt.WriteString("A finished model turn, an idle Session, a persisted report, a stopped worker, or a worker close is never proof that a ticket is complete.\n")
	prompt.WriteString("After a report and before acceptance, do not implicitly close the worker or invalidate a direct route.\n")
	prompt.WriteString("Do not retry an operation whose side-effect outcome is unknown. Inspect state first and use explicit tool operations for recovery.\n\n")
	prompt.WriteString("## Project memory\n")
	prompt.WriteString(strings.TrimSpace(project.Memory))
	prompt.WriteString("\n")
	return prompt.String()
}

// InitializationPrompt is the create-only first turn used to move a newly
// opened Runtime Session out of its pre-input unknown state. It explicitly
// forbids ticket or worker work so initialization output can never be mistaken
// for a completed assignment.
func InitializationPrompt(role, projectID string) *SessionInitialInput {
	return &SessionInitialInput{Content: []SessionContentBlock{{
		Type: "text",
		Text: "Initialization only for the " + role + " Session of project " + projectID + ".\n" +
			"Do not inspect or change the repository. Do not create tickets or workers. Do not execute ticket work. Do not call finish_ticket.\n" +
			"Reply exactly with MASS_SA_READY and wait for a later prompt.",
	}}}
}

// WorkerNamespace returns the root Ticket MASS namespace. MASS namespaces
// remain soft grouping labels; authorization comes from mass-sa capabilities.
func WorkerNamespace(projectID, rootTicketID string) string {
	raw := sanitizeNamespacePart(projectID) + "-" + sanitizeNamespacePart(rootTicketID)
	if len(raw) <= 63 {
		return strings.Trim(raw, "-")
	}
	sum := sha256.Sum256([]byte(projectID + "\x00" + rootTicketID))
	return strings.Trim(raw[:54], "-") + "-" + hex.EncodeToString(sum[:4])
}

func sanitizeNamespacePart(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

// WorkerSystemPrompt injects project memory and the exact Ticket
// context visible to one worker Session.
func WorkerSystemPrompt(project ProjectConfig, ticket TicketState, workerName string) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "You are worker %q for Ticket %s in project %q (%s).\n", workerName, ticket.ID, project.Name, project.ID)
	prompt.WriteString("Execute this Ticket within its objective and acceptance criteria. Use list_workers, get_worker, and send_to_worker only within the authenticated root Ticket scope. A shared namespace is not authorization.\n")
	prompt.WriteString("Use get_ticket_context(ticketId?) for authorized objectives, progress, parent/child result references and Thread/Session history references. Omit ticketId for your current Ticket. Internal delegation does not fabricate external Triggers or inherit all Thread subscriptions.\n")
	prompt.WriteString("Confirmed child lifecycle inputs retain creation, blocked, waiting_user, completed, failed, cancelled and reopened semantics. Read local Ticket revisions and report references as needed. A child result never automatically completes its parent. These inputs grant no new work and cannot reopen terminated work. Parent Session close/stop/replace does not stop children; the Router must explicitly dispose unfinished children before closing the parent.\n")
	prompt.WriteString("Return a structured JSON report with status, summary, and evidence. A report or idle Session does not accept or complete the Ticket; explicit Router acceptance is required.\n")
	prompt.WriteString("Choose direct work or the Agent's native subagent tools as needed. Native subagents create no platform Ticket or notification. Platform delegation is optional: only an explicit authorization containing create_child_ticket permits it. Use a stable requestId, independent objective/acceptance, resource and tool subsets, exact allowed execution choices, and no parent/root/namespace fields. Omit workerName to queue; dispatch_child_ticket later requires its own grant. A repeated requestId with identical content returns the same child Ticket. No fixed Planner or Orchestrator role is required.\n")
	if ticket.Authorization != nil {
		grant, _ := json.Marshal(ticket.Authorization)
		fmt.Fprintf(&prompt, "Work authorization (resource identities are task boundaries, not an OS sandbox): %s\nDo not access or change resources outside this grant. Only listed mass-sa tools are authorized.\n", grant)
	}
	fmt.Fprintf(&prompt, "Root Ticket: %s\nObjective:\n%s\nAcceptance:\n%s\nWorking directory: %s\nNamespace: %s\nProject memory:\n%s\n", ticket.RootTicketID, ticket.Objective, ticket.Acceptance, project.CWD, ticket.Namespace, project.Memory)
	return prompt.String()
}
