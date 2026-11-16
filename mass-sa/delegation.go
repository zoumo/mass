package sa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// WorkAuthorization is an explicit, immutable work grant. Resource identities
// delimit the task in the worker instructions; this is not an OS sandbox.
// Tools and execution choices are enforced by mass-sa on every request.
type WorkAuthorization struct {
	Resources   []string `json:"resources"`
	Tools       []string `json:"tools"`
	RuntimeRefs []string `json:"runtimeRefs"`
	Models      []string `json:"models"`
	Efforts     []string `json:"efforts"`
	MaxDepth    int      `json:"maxDepth"`
	MaxTickets  int      `json:"maxTickets"`
}

type WorkerExecution struct {
	RuntimeRef string `json:"runtimeRef"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort"`
}

// DelegationState identifies an atomically created child ticket.
type DelegationState struct {
	RequestID   string `json:"requestId"`
	ParentID    string `json:"parentTicketId"`
	TicketID    string `json:"ticketId"`
	Fingerprint string `json:"fingerprint"`
}

type createChildTicketArgs struct {
	RequestID     string            `json:"requestId"`
	Title         string            `json:"title"`
	Objective     string            `json:"objective"`
	Acceptance    string            `json:"acceptance"`
	Authorization WorkAuthorization `json:"authorization"`
	Execution     WorkerExecution   `json:"execution"`
	WorkerName    string            `json:"workerName,omitempty"`
}

type dispatchChildTicketArgs struct {
	TicketID string `json:"ticketId"`
	Name     string `json:"name"`
}

type ChildTicketResult struct {
	Ticket TicketState `json:"ticket"`
	Worker *WorkerView `json:"worker,omitempty"`
}

var workerGrantTools = []string{"list_workers", "get_worker", "send_to_worker", "get_ticket_context", "create_child_ticket", "dispatch_child_ticket"}

func validateAuthorization(grant *WorkAuthorization) error {
	if grant == nil {
		return nil
	}
	if len(grant.Resources) == 0 || len(grant.RuntimeRefs) == 0 || len(grant.Models) == 0 || len(grant.Efforts) == 0 || grant.MaxDepth < 0 || grant.MaxDepth > 8 || grant.MaxTickets < 1 || grant.MaxTickets > 64 {
		return operationError("invalid_argument", "invalid_work_authorization", "authorization requires resources and exact execution choices, maxDepth 0..8 and maxTickets 1..64")
	}
	for _, values := range [][]string{grant.Resources, grant.RuntimeRefs, grant.Models, grant.Efforts, grant.Tools} {
		if len(values) > 64 {
			return operationError("invalid_argument", "invalid_work_authorization", "authorization list exceeds 64 entries")
		}
		seen := map[string]bool{}
		for _, value := range values {
			if err := validateSingleLine("authorization value", value, false); err != nil {
				return err
			}
			if seen[value] {
				return operationError("invalid_argument", "invalid_work_authorization", "authorization lists cannot contain duplicates")
			}
			seen[value] = true
		}
	}
	for _, value := range grant.Resources {
		if value == "" {
			return operationError("invalid_argument", "invalid_work_authorization", "resource identities cannot be empty")
		}
	}
	for _, tool := range grant.Tools {
		if !slices.Contains(workerGrantTools, tool) {
			return operationError("invalid_argument", "invalid_work_authorization", "authorization contains an unsupported worker tool")
		}
	}
	return nil
}

func (s *WorkerService) validateRootAuthorization(projectID string, grant *WorkAuthorization) error {
	if err := validateAuthorization(grant); err != nil {
		return err
	}
	if grant == nil {
		return nil
	}
	project, err := s.project(projectID)
	if err != nil {
		return err
	}
	for _, runtime := range grant.RuntimeRefs {
		if runtime != project.RuntimeRef && !slices.Contains(project.Worker.RuntimeRefs, runtime) {
			return operationError("invalid_argument", "runtime_not_allowed", "authorization Runtime must be configured for this project")
		}
	}
	return nil
}

func authorizationSubset(child, parent *WorkAuthorization) error {
	if parent == nil || child == nil {
		return delegationDenied()
	}
	for _, pair := range [][2][]string{{child.Resources, parent.Resources}, {child.Tools, parent.Tools}, {child.RuntimeRefs, parent.RuntimeRefs}, {child.Models, parent.Models}, {child.Efforts, parent.Efforts}} {
		for _, value := range pair[0] {
			if !slices.Contains(pair[1], value) {
				return delegationDenied()
			}
		}
	}
	if child.MaxDepth > parent.MaxDepth || child.MaxTickets > parent.MaxTickets {
		return delegationDenied()
	}
	return nil
}

func checkExecutionAuthorization(grant *WorkAuthorization, execution WorkerExecution) error {
	if grant == nil {
		return nil
	}
	if !slices.Contains(grant.RuntimeRefs, execution.RuntimeRef) || !slices.Contains(grant.Models, execution.Model) || !slices.Contains(grant.Efforts, execution.Effort) {
		return operationError("invalid_argument", "execution_not_authorized", "Runtime, model and effort must match the Ticket's exact allowed choices")
	}
	return nil
}

func delegationDenied() error {
	return operationError("failed_precondition", "delegation_not_authorized", "delegation must remain within the current Ticket's work and tool authorization")
}

func currentWorkerTicket(project ProjectState, cap CapabilityState) (TicketState, error) {
	a, ok := project.Assignments[cap.AssignmentID]
	t, exists := project.Tickets[a.TicketID]
	w := project.Workers[workerKey(a.Namespace, cap.WorkerName)]
	if !ok || !exists || a.Status != "active" || t.AssignmentID != a.ID || w.AssignmentID != a.ID || a.ProjectID != cap.ProjectID || a.RootTicketID != cap.RootTicketID || terminalBusinessState(t.BusinessState) {
		return TicketState{}, capabilityInvalid()
	}
	return t, nil
}

func (s *WorkerService) authorizeWorkerTool(cap CapabilityState, tool string) error {
	if cap.Role != "worker" {
		return nil
	}
	ticket, err := currentWorkerTicket(s.store.Snapshot().Projects[cap.ProjectID], cap)
	if err != nil {
		return err
	}
	if ticket.Authorization == nil {
		if tool == "create_child_ticket" || tool == "dispatch_child_ticket" {
			return delegationDenied()
		}
		return nil
	}
	if !slices.Contains(ticket.Authorization.Tools, tool) {
		return delegationDenied()
	}
	return nil
}

func (s *WorkerService) createChildTicket(ctx context.Context, cap CapabilityState, args createChildTicketArgs) (ChildTicketResult, error) {
	for label, value := range map[string]string{"request ID": args.RequestID, "ticket title": args.Title} {
		if err := validateSingleLine(label, value, true); err != nil {
			return ChildTicketResult{}, err
		}
	}
	for label, value := range map[string]string{"objective": args.Objective, "acceptance": args.Acceptance} {
		if err := validateBlockText(label, value, true); err != nil {
			return ChildTicketResult{}, err
		}
	}
	if err := s.validateRootAuthorization(cap.ProjectID, &args.Authorization); err != nil {
		return ChildTicketResult{}, err
	}
	if args.WorkerName != "" {
		if err := validateWorkerName(args.WorkerName); err != nil {
			return ChildTicketResult{}, err
		}
	}
	if err := checkExecutionAuthorization(&args.Authorization, args.Execution); err != nil {
		return ChildTicketResult{}, err
	}
	projectConfig, err := s.project(cap.ProjectID)
	if err != nil {
		return ChildTicketResult{}, err
	}
	if _, _, err := workerConfig(projectConfig, args.Execution.Model, args.Execution.Effort); err != nil {
		return ChildTicketResult{}, err
	}
	// Explicit values must survive workerConfig defaults unchanged.
	if args.Execution.RuntimeRef == "" || args.Execution.Effort == "" || (args.Execution.Model == "" && projectConfig.Worker.Model != "") {
		return ChildTicketResult{}, operationError("invalid_argument", "explicit_execution_required", "child execution must explicitly select the effective Runtime, model and effort")
	}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	childID, err := s.newID("t")
	if err != nil {
		return ChildTicketResult{}, err
	}
	var intent DelegationState
	err = s.store.updateState(func(state *State) error {
		p := state.Projects[cap.ProjectID]
		parent, err := currentWorkerTicket(p, cap)
		if err != nil {
			return err
		}
		if parent.Authorization == nil || !slices.Contains(parent.Authorization.Tools, "create_child_ticket") {
			return delegationDenied()
		}
		if err := authorizationSubset(&args.Authorization, parent.Authorization); err != nil {
			return err
		}
		if args.WorkerName != "" && !slices.Contains(parent.Authorization.Tools, "dispatch_child_ticket") {
			return delegationDenied()
		}
		key := parent.ID + ":" + args.RequestID
		if previous, exists := p.Delegations[key]; exists {
			if previous.Fingerprint != fingerprint {
				return operationError("already_exists", "delegation_request_conflict", "requestId already identifies different delegation content")
			}
			intent = previous
			return nil
		}
		if parent.Depth+1 > parent.Authorization.MaxDepth {
			return operationError("failed_precondition", "delegation_depth_limit", "child would exceed the authorized absolute delegation depth")
		}
		root := p.Tickets[parent.RootTicketID]
		if root.ID == "" || root.ProjectID != cap.ProjectID || root.ParentTicketID != "" || root.Namespace != parent.Namespace || root.Authorization == nil {
			return delegationDenied()
		}
		count := 0
		for _, ticket := range p.Tickets {
			if ticket.RootTicketID == root.ID {
				count++
			}
		}
		if count >= root.Authorization.MaxTickets || count >= parent.Authorization.MaxTickets {
			return operationError("failed_precondition", "delegation_resource_limit", "root Ticket resource budget is exhausted")
		}
		if _, exists := p.Tickets[childID]; exists {
			return operationError("already_exists", "duplicate_ticket", "child Ticket identifier already exists")
		}
		intent = DelegationState{RequestID: args.RequestID, ParentID: parent.ID, TicketID: childID, Fingerprint: fingerprint}
		if p.Delegations == nil {
			p.Delegations = map[string]DelegationState{}
		}
		p.Delegations[key] = intent
		assignment := p.Assignments[cap.AssignmentID]
		ticket := TicketState{
			ID: childID, ProjectID: cap.ProjectID, RootTicketID: parent.RootTicketID,
			ParentTicketID: parent.ID, Namespace: parent.Namespace, Title: args.Title,
			Objective: args.Objective, Acceptance: args.Acceptance,
			Authorization: &args.Authorization, Execution: &args.Execution,
			CreatedByAssignment: cap.AssignmentID, CreatedBySession: assignment.Session,
			DelegationRequestID: args.RequestID, Depth: parent.Depth + 1,
			CreatedAt: s.now(), UpdatedAt: s.now(),
		}
		p.Tickets[ticket.ID] = ticket
		recordChildLifecycle(&p, ticket, "created", ticket.Objective, ticket.CreatedAt)
		state.Projects[cap.ProjectID] = p
		return nil
	})
	if err != nil {
		return ChildTicketResult{}, err
	}
	ticket := s.store.Snapshot().Projects[cap.ProjectID].Tickets[intent.TicketID]
	result := ChildTicketResult{Ticket: ticket}
	if args.WorkerName != "" {
		worker, err := s.dispatchChildTicket(ctx, cap, dispatchChildTicketArgs{TicketID: ticket.ID, Name: args.WorkerName})
		if err != nil {
			return result, err
		}
		result.Worker = &worker
		result.Ticket = s.store.Snapshot().Projects[cap.ProjectID].Tickets[ticket.ID]
	}
	return result, nil
}

func (s *WorkerService) dispatchChildTicket(ctx context.Context, cap CapabilityState, args dispatchChildTicketArgs) (WorkerView, error) {
	if err := s.authorizeWorkerTool(cap, "dispatch_child_ticket"); err != nil {
		return WorkerView{}, err
	}
	p := s.store.Snapshot().Projects[cap.ProjectID]
	parent, err := currentWorkerTicket(p, cap)
	if err != nil {
		return WorkerView{}, err
	}
	child, exists := p.Tickets[args.TicketID]
	if !exists || child.ParentTicketID != parent.ID || child.RootTicketID != parent.RootTicketID || child.Namespace != parent.Namespace || child.ProjectID != cap.ProjectID {
		return WorkerView{}, workerNotFound()
	}
	return s.createWorker(ctx, cap.ProjectID, createWorkerArgs{TicketID: child.ID, Name: args.Name})
}
