package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"
)

// TriggerRequest contains source facts only. Source identifies a connector/account;
// sourceEventId is unique within that source and Project. ThreadID is source-local.
type TriggerRequest struct {
	EchoOfOutputID string          `json:"echoOfOutputId,omitempty"`
	ReplyRequested bool            `json:"replyRequested,omitempty"`
	ProjectID      string          `json:"projectId"`
	Source         string          `json:"source"`
	SourceEventID  string          `json:"sourceEventId"`
	ThreadID       string          `json:"threadId"`
	Kind           string          `json:"kind"`
	Summary        string          `json:"summary"`
	Content        json.RawMessage `json:"content"`
}

type TriggerReceipt struct {
	TriggerID     string    `json:"triggerId"`
	ProjectID     string    `json:"projectId"`
	Source        string    `json:"source"`
	SourceEventID string    `json:"sourceEventId"`
	ThreadID      string    `json:"threadId"`
	AcceptedAt    time.Time `json:"acceptedAt"`
}

type TriggerState struct {
	TriggerReceipt
	EchoOfOutputID string          `json:"echoOfOutputId,omitempty"`
	ReplyRequested bool            `json:"replyRequested,omitempty"`
	Kind           string          `json:"kind"`
	Summary        string          `json:"summary"`
	Content        json.RawMessage `json:"content"`
	Decision       string          `json:"decision"`
	TicketIDs      []string        `json:"ticketIds,omitempty"`
	ErrorReason    string          `json:"errorReason,omitempty"`
}

// Associations survive execution changes. Multiple tickets make a Thread
// ambiguous for routing; they never cause automatic fan-out.
type ThreadState struct {
	Source    string   `json:"source"`
	ThreadID  string   `json:"threadId"`
	TicketIDs []string `json:"ticketIds"`
}

func threadKey(source, thread string) string { return source + "\x00" + thread }

// AcceptTrigger has no network dependencies. Its receipt describes persistence,
// never model execution. Replays return the original receipt even after routing.
func (s *WorkerService) AcceptTrigger(req TriggerRequest) (TriggerReceipt, error) {
	if _, err := s.project(req.ProjectID); err != nil {
		return TriggerReceipt{}, err
	}
	for name, value := range map[string]string{"source": req.Source, "sourceEventId": req.SourceEventID, "threadId": req.ThreadID, "kind": req.Kind, "summary": req.Summary} {
		if err := validateInboxField(name, value, true); err != nil {
			return TriggerReceipt{}, err
		}
		if strings.ContainsRune(value, '\x00') {
			return TriggerReceipt{}, operationError("invalid_argument", "invalid_source_identity", "source identities must not contain NUL")
		}
	}
	if len(req.Content) > maxInboxPayloadBytes || !json.Valid(req.Content) {
		return TriggerReceipt{}, operationError("invalid_argument", "invalid_trigger_content", "content must be bounded valid JSON")
	}
	id, err := s.newID("tr")
	if err != nil {
		return TriggerReceipt{}, err
	}
	var receipt TriggerReceipt
	err = s.store.updateProject(req.ProjectID, func(p *ProjectState) error {
		for _, old := range p.Triggers {
			if old.Source != req.Source || old.SourceEventID != req.SourceEventID {
				continue
			}
			if old.EchoOfOutputID != req.EchoOfOutputID || old.ReplyRequested != req.ReplyRequested || old.ThreadID != req.ThreadID || old.Kind != req.Kind || old.Summary != req.Summary || !equalJSON(old.Content, req.Content) {
				return operationError("already_exists", "trigger_identity_conflict", "source event identity already has different content")
			}
			receipt = old.TriggerReceipt
			return nil
		}
		receipt = TriggerReceipt{id, req.ProjectID, req.Source, req.SourceEventID, req.ThreadID, s.now()}
		trigger := TriggerState{TriggerReceipt: receipt, ReplyRequested: req.ReplyRequested, Kind: req.Kind, Summary: req.Summary, Content: append(json.RawMessage(nil), req.Content...), Decision: "pending"}
		if req.EchoOfOutputID != "" {
			output, exists := p.Outputs[req.EchoOfOutputID]
			if !exists || !outputMatches(output, req.Source, req.ThreadID) {
				return workerNotFound()
			}
			trigger.EchoOfOutputID = req.EchoOfOutputID
			trigger.Decision = "ignored"
			p.Triggers[id] = trigger
			return nil
		}
		thread := p.Threads[threadKey(req.Source, req.ThreadID)]
		if len(thread.TicketIDs) == 1 {
			if ticket, ok := p.Tickets[thread.TicketIDs[0]]; ok && ticket.ProjectID == req.ProjectID {
				trigger.Decision = "associated"
				trigger.TicketIDs = []string{ticket.ID}
			}
		}
		p.Triggers[id] = trigger
		return s.queueTrigger(p, trigger)
	})
	return receipt, err
}

func equalJSON(a, b []byte) bool {
	var left, right bytes.Buffer
	return json.Compact(&left, a) == nil && json.Compact(&right, b) == nil && bytes.Equal(left.Bytes(), right.Bytes())
}

func (s *WorkerService) queueTrigger(p *ProjectState, trigger TriggerState) error {
	ids := trigger.TicketIDs
	if len(ids) == 0 {
		ids = []string{""}
	}
	for _, ticketID := range ids {
		id := trigger.TriggerID + "/" + ticketID
		if _, exists := p.Inbox[id]; exists {
			continue
		}
		event := InboxEventState{ID: id, TriggerID: trigger.TriggerID, ThreadID: trigger.ThreadID, TicketID: ticketID, ProjectID: trigger.ProjectID, Source: trigger.Source, SourceEventID: trigger.SourceEventID, Kind: trigger.Kind, Summary: trigger.Summary, Payload: trigger.Content, State: "queued", AcceptedAt: trigger.AcceptedAt, UpdatedAt: s.now(), RoutingPath: "router"}
		if ticketID != "" {
			ticket, ok := p.Tickets[ticketID]
			if !ok || ticket.ProjectID != trigger.ProjectID {
				return workerNotFound()
			}
			event.RootTicketID = ticket.RootTicketID
			event.RoutingPath = "direct"
			if trigger.ReplyRequested {
				event.RoutingPath = "router"
			}
		}
		cfg, _ := projectConfigByID(s.config, trigger.ProjectID)
		if len(workerPromptFromEvents([]InboxEventState{event})) > cfg.Inbox.MaxPromptBytes {
			return operationError("invalid_argument", "event_prompt_too_large", "one Trigger exceeds the configured prompt budget")
		}
		p.NextInboxSeq++
		event.Sequence = p.NextInboxSeq
		p.Inbox[id] = event
	}
	return nil
}

type decideTriggerArgs struct {
	TriggerID  string `json:"triggerId"`
	Action     string `json:"action"`
	TicketID   string `json:"ticketId,omitempty"`
	Title      string `json:"title,omitempty"`
	Objective  string `json:"objective,omitempty"`
	Acceptance string `json:"acceptance,omitempty"`
}

// Create is fenced durably before the external write. An interrupted/failed
// creation stays unknown and is not blindly repeated after restart.
func (s *WorkerService) decideTrigger(ctx context.Context, projectID string, args decideTriggerArgs) (TriggerState, error) {
	if args.Action != "ignore" && args.Action != "associate" && args.Action != "create" {
		return TriggerState{}, operationError("invalid_argument", "invalid_trigger_action", "action must be ignore, associate, or create")
	}
	if args.Action == "create" {
		if err := validateSingleLine("title", args.Title, true); err != nil {
			return TriggerState{}, err
		}
		if err := validateBlockText("objective", args.Objective, true); err != nil {
			return TriggerState{}, err
		}
		if err := validateBlockText("acceptance", args.Acceptance, true); err != nil {
			return TriggerState{}, err
		}
	}
	var result TriggerState
	create := false
	err := s.store.updateProject(projectID, func(p *ProjectState) error {
		trigger, ok := p.Triggers[args.TriggerID]
		if !ok {
			return workerNotFound()
		}
		if trigger.Decision != "pending" {
			if args.Action == "ignore" && trigger.Decision == "ignored" || args.Action == "associate" && slices.Contains(trigger.TicketIDs, args.TicketID) || args.Action == "create" && trigger.Decision == "created" {
				result = trigger
				return nil
			}
			return operationError("failed_precondition", "trigger_already_decided", "Trigger is already decided or requires reconciliation")
		}
		switch args.Action {
		case "ignore":
			trigger.Decision = "ignored"
		case "associate":
			ticket, exists := p.Tickets[args.TicketID]
			if !exists || ticket.ProjectID != projectID {
				return workerNotFound()
			}
			trigger.Decision = "associated"
			trigger.TicketIDs = []string{ticket.ID}
			linkThread(p, trigger.Source, trigger.ThreadID, ticket.ID)
			setOutputOrigin(p, ticket.ID, trigger.TriggerID)
			if err := s.queueTrigger(p, trigger); err != nil {
				return err
			}
		case "create":
			trigger.Decision = "creating"
			create = true
		}
		// A queued Router input can be cancelled by an explicit decision. An
		// already attempted Router delivery remains immutable evidence.
		if event, ok := p.Inbox[trigger.TriggerID+"/"]; ok && event.State == "queued" {
			event.State = "handled"
			event.UpdatedAt = s.now()
			p.Inbox[event.ID] = event
		}
		p.Triggers[trigger.TriggerID] = trigger
		result = trigger
		return nil
	})
	if err != nil || !create {
		return result, err
	}
	ticket, createErr := s.createTicket(ctx, projectID, createTicketArgs{Title: args.Title, Objective: args.Objective, Acceptance: args.Acceptance})
	err = s.store.updateProject(projectID, func(p *ProjectState) error {
		trigger := p.Triggers[args.TriggerID]
		trigger.Decision = "unknown"
		if createErr != nil {
			trigger.ErrorReason = operationReason(createErr)
		} else {
			trigger.Decision = "created"
			trigger.TicketIDs = []string{ticket.ID}
			linkThread(p, trigger.Source, trigger.ThreadID, ticket.ID)
			setOutputOrigin(p, ticket.ID, trigger.TriggerID)
			if err := s.queueTrigger(p, trigger); err != nil {
				return err
			}
		}
		p.Triggers[trigger.TriggerID] = trigger
		result = trigger
		return nil
	})
	if createErr != nil {
		return result, createErr
	}
	return result, err
}

func setOutputOrigin(p *ProjectState, ticketID, triggerID string) {
	ticket := p.Tickets[ticketID]
	if ticket.OutputTriggerID == "" {
		ticket.OutputTriggerID = triggerID
		p.Tickets[ticketID] = ticket
	}
}

func linkThread(p *ProjectState, source, threadID, ticketID string) ThreadState {
	key := threadKey(source, threadID)
	thread := p.Threads[key]
	thread.Source = source
	thread.ThreadID = threadID
	if !slices.Contains(thread.TicketIDs, ticketID) {
		thread.TicketIDs = append(thread.TicketIDs, ticketID)
		sort.Strings(thread.TicketIDs)
	}
	p.Threads[key] = thread
	return thread
}

type linkThreadArgs struct {
	Source   string `json:"source"`
	ThreadID string `json:"threadId"`
	TicketID string `json:"ticketId"`
}

func (s *WorkerService) associateThread(projectID string, args linkThreadArgs) (ThreadState, error) {
	for name, value := range map[string]string{"source": args.Source, "threadId": args.ThreadID} {
		if err := validateInboxField(name, value, true); err != nil {
			return ThreadState{}, err
		}
		if strings.ContainsRune(value, '\x00') {
			return ThreadState{}, operationError("invalid_argument", "invalid_source_identity", "source identities must not contain NUL")
		}
	}
	var result ThreadState
	err := s.store.updateProject(projectID, func(p *ProjectState) error {
		if ticket, ok := p.Tickets[args.TicketID]; !ok || ticket.ProjectID != projectID {
			return workerNotFound()
		}
		result = linkThread(p, args.Source, args.ThreadID, args.TicketID)
		return nil
	})
	return result, err
}

// resolveQueuedInput changes only the dispatch candidate. prepareDelivery
// atomically checks it and fixes Assignment/Session evidence before prompting.
func resolveQueuedInput(p ProjectState, event InboxEventState) (InboxEventState, bool) {
	if event.TriggerID == "" && event.ChildLifecycle == nil {
		return event, true
	}
	if event.ChildLifecycle == nil && (event.TicketID == "" || event.RoutingPath == "router") {
		if p.Router == nil || p.Router.SessionID == "" {
			return event, false
		}
		event.TargetSession = p.Router.SessionRef
		event.TargetWorkerName = p.Router.Name
		return event, true
	}
	ticket, ok := p.Tickets[event.TicketID]
	if !ok || terminalBusinessState(ticket.BusinessState) || ticket.Transition != nil {
		return event, false
	}
	assignment, ok := p.Assignments[ticket.AssignmentID]
	if !ok || assignment.Status != "active" || assignment.ReconciliationRequired || assignment.ExecutionState == "unknown" {
		return event, false
	}
	worker, ok := p.Workers[workerKey(assignment.Namespace, assignment.WorkerName)]
	if !ok || worker.AssignmentID != assignment.ID || worker.Session.SessionID == "" {
		return event, false
	}
	event.TargetAssignmentID = assignment.ID
	event.TargetWorkerName = worker.Name
	event.TargetSession = worker.Session
	return event, true
}
