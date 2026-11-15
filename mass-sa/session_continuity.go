package sa

import (
	"context"
	"time"
)

func matchesWorkerSpec(session Session, worker WorkerState, cwd string) bool {
	if session.Spec.RuntimeRef != worker.RuntimeRef || session.Spec.Cwd != cwd {
		return false
	}
	if session.Spec.Config == nil {
		return worker.Model == "" && worker.Effort == ""
	}
	return session.Spec.Config.Model == worker.Model && session.Spec.Config.Effort == worker.Effort
}

// MASS may confirm a live resume before observing execution state. This permit
// belongs only to its confirmed current Assignment and is consumed before the
// first new prompt. An unknown prompt outcome never restores it.
func (s *WorkerService) resumedInputReady(projectID, assignmentID string, session Session) bool {
	if session.Status.State != "unknown" {
		return false
	}
	p := s.store.Snapshot().Projects[projectID]
	a, ok := p.Assignments[assignmentID]
	if !ok || !a.AwaitingResumedInput || a.Status != "active" || a.ReconciliationRequired || a.ExecutionID != "" || a.ExecutionState != "" || p.Tickets[a.TicketID].AssignmentID != a.ID {
		return false
	}
	w, ok := p.Workers[workerKey(a.Namespace, a.WorkerName)]
	project, err := s.project(projectID)
	return ok && err == nil && w.AssignmentID == a.ID &&
		session.Status.SessionID == a.Session.SessionID && session.Metadata.Namespace == a.Session.Namespace && session.Metadata.Name == a.Session.Name &&
		matchesWorkerSpec(session, w, project.CWD)
}

// A Session's MCP command retains this private file path across resume. Rotate
// its token only after closing the old run; cached old tokens stay revoked.
func (s *WorkerService) workerCapabilityPath(projectID, assignmentID string) string {
	for _, capability := range s.store.Snapshot().Capabilities {
		if capability.ProjectID == projectID && capability.AssignmentID == assignmentID {
			return capability.File
		}
	}
	return ""
}

func (s *WorkerService) currentTicketWorker(projectID string, ticket TicketState) (WorkerState, AssignmentState, error) {
	project := s.store.Snapshot().Projects[projectID]
	assignment, ok := project.Assignments[ticket.AssignmentID]
	worker, exists := project.Workers[workerKey(assignment.Namespace, assignment.WorkerName)]
	if !ok || !exists || worker.TicketID != ticket.ID || worker.AssignmentID != assignment.ID || worker.Session.SessionID == "" {
		return WorkerState{}, AssignmentState{}, operationError("failed_precondition", "worker_identity_unconfirmed", "current Ticket executor requires reconciliation")
	}
	return worker, assignment, nil
}

func (s *WorkerService) checkedSession(ctx context.Context, client sessionClient, project ProjectConfig, worker WorkerState, assignment AssignmentState) (Session, error) {
	lifecyclePending := assignment.ReconciliationReason == "session_transition_unconfirmed" || assignment.ReconciliationReason == "session_resume_unconfirmed"
	if (assignment.ReconciliationRequired && !lifecyclePending) || (assignment.ExecutionState != "" && assignment.ExecutionState != "completed" && assignment.ExecutionState != "failed") {
		return Session{}, operationError("failed_precondition", "worker_execution_unconfirmed", "prior execution must be reconciled before retry or replacement; inputs will not be replayed")
	}
	session, err := client.GetSession(ctx, SessionRef{SessionID: worker.Session.SessionID})
	if err != nil {
		return Session{}, err
	}
	if session.Status.SessionID != worker.Session.SessionID || session.Metadata.Namespace != worker.Namespace || session.Metadata.Name != worker.Session.Name || !matchesWorkerSpec(session, worker, project.CWD) {
		return Session{}, operationError("failed_precondition", "worker_identity_mismatch", "Session identity, Runtime, or working directory does not match the Ticket executor")
	}
	if session.Status.State != "idle" && session.Status.State != "offline" && !s.resumedInputReady(project.ID, assignment.ID, session) {
		return Session{}, operationError("failed_precondition", "worker_not_quiescent", "old Session must be confirmed idle or offline before retry or replacement")
	}
	if _, err := readAllHistory(ctx, client, SessionRef{SessionID: worker.Session.SessionID}, nil); err != nil {
		return Session{}, err
	}
	return session, nil
}

// fenceWorker persists the admission fence before any close/resume side effect.
// A crash or ambiguous result leaves a durable reconciliation requirement.
func (s *WorkerService) fenceWorker(projectID string, worker WorkerState) error {
	return s.store.updateProject(projectID, func(project *ProjectState) error {
		assignment := project.Assignments[worker.AssignmentID]
		lifecyclePending := assignment.ReconciliationReason == "session_transition_unconfirmed" || assignment.ReconciliationReason == "session_resume_unconfirmed"
		if project.Tickets[worker.TicketID].AssignmentID != assignment.ID || (assignment.Status != "active" && assignment.Status != "closed" && !lifecyclePending) {
			return operationError("failed_precondition", "assignment_inactive", "current Assignment cannot enter a lifecycle transition")
		}
		assignment.Status = "closing"
		assignment.ReconciliationRequired = true
		assignment.ReconciliationReason = "session_transition_unconfirmed"
		assignment.UpdatedAt = s.now()
		project.Assignments[assignment.ID] = assignment
		return nil
	})
}

func (s *WorkerService) closeFencedWorker(ctx context.Context, client sessionClient, projectID string, worker WorkerState) error {
	if err := s.fenceWorker(projectID, worker); err != nil {
		return err
	}
	if _, err := client.CloseSession(ctx, SessionRef{SessionID: worker.Session.SessionID}); err != nil {
		return err
	}
	session, err := client.GetSession(ctx, SessionRef{SessionID: worker.Session.SessionID})
	if err != nil {
		return err
	}
	if session.Status.SessionID != worker.Session.SessionID || session.Status.State != "offline" {
		return operationError("failed_precondition", "worker_close_unconfirmed", "old Session resource release is not confirmed")
	}
	return s.store.updateState(func(state *State) error {
		project := state.Projects[projectID]
		assignment := project.Assignments[worker.AssignmentID]
		assignment.Status = "closed"
		assignment.ReconciliationRequired = false
		assignment.ReconciliationReason = ""
		assignment.UpdatedAt = s.now()
		project.Assignments[assignment.ID] = assignment
		current := project.Workers[workerKey(worker.Namespace, worker.Name)]
		current.State = "offline"
		current.UpdatedAt = s.now()
		project.Workers[workerKey(worker.Namespace, worker.Name)] = current
		for digest, cap := range state.Capabilities {
			if cap.ProjectID == projectID && cap.AssignmentID == assignment.ID {
				cap.RevokedAt = s.now()
				state.Capabilities[digest] = cap
			}
		}
		state.Projects[projectID] = project
		return nil
	})
}

func (s *WorkerService) prepareReplacement(ctx context.Context, project ProjectConfig, ticket TicketState) error {
	worker, assignment, err := s.currentTicketWorker(project.ID, ticket)
	if err != nil {
		return err
	}
	release, err := s.holdExecution(assignment.ID)
	if err != nil {
		return err
	}
	defer release()
	client, err := s.openMASS(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := s.checkedSession(ctx, client, project, worker, assignment); err != nil {
		return err
	}
	return s.closeFencedWorker(ctx, client, project.ID, worker)
}

func (s *WorkerService) continueWorker(ctx context.Context, project ProjectConfig, ticket TicketState, args createWorkerArgs, model, effort string) (WorkerView, error) {
	worker, assignment, err := s.currentTicketWorker(project.ID, ticket)
	if err != nil {
		return WorkerView{}, err
	}
	if args.Model == "" {
		model = worker.Model
	}
	if args.Effort == "" {
		effort = worker.Effort
	}
	if args.Name != worker.Name || model != worker.Model || effort != worker.Effort || project.RuntimeRef != worker.RuntimeRef {
		return WorkerView{}, operationError("failed_precondition", "explicit_replacement_required", "changing executor or execution configuration requires action=replace and a new name")
	}
	release, err := s.holdExecution(assignment.ID)
	if err != nil {
		return WorkerView{}, err
	}
	defer release()
	client, err := s.openMASS(ctx)
	if err != nil {
		return WorkerView{}, err
	}
	defer client.Close()
	session, err := s.checkedSession(ctx, client, project, worker, assignment)
	if err != nil {
		return WorkerView{}, err
	}
	if args.Action == "" && assignment.Status == "active" && (session.Status.State == "idle" || s.resumedInputReady(project.ID, assignment.ID, session)) {
		worker.State = session.Status.State
		return viewWorker(worker), nil
	}
	path := s.workerCapabilityPath(project.ID, assignment.ID)
	if path == "" {
		return WorkerView{}, capabilityInvalid()
	}
	if err := s.closeFencedWorker(ctx, client, project.ID, worker); err != nil {
		return WorkerView{}, err
	}
	id, err := s.newID("a")
	if err != nil {
		return WorkerView{}, err
	}
	grant, err := issueCapability(s.toolEndpoint(), path, CapabilityState{ProjectID: project.ID, Role: "worker", RootTicketID: worker.RootTicketID, WorkerName: worker.Name, AssignmentID: id, CreatedAt: s.now()})
	if err != nil {
		return WorkerView{}, err
	}
	next := AssignmentState{ID: id, ProjectID: project.ID, RootTicketID: worker.RootTicketID, TicketID: ticket.ID, WorkerName: worker.Name, Namespace: worker.Namespace, Session: worker.Session, RuntimeRef: worker.RuntimeRef, Model: model, Effort: effort, Status: "creating", AwaitingResumedInput: true, ReconciliationRequired: true, ReconciliationReason: "session_resume_unconfirmed", CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := s.store.updateState(func(state *State) error {
		p := state.Projects[project.ID]
		if p.Tickets[ticket.ID].AssignmentID != assignment.ID || p.Assignments[assignment.ID].Status != "closed" {
			return operationError("failed_precondition", "assignment_changed", "current Assignment changed during recovery")
		}
		worker.AssignmentID = id
		worker.State = "opening"
		worker.StoppedAt = time.Time{}
		worker.UpdatedAt = s.now()
		p.Workers[workerKey(worker.Namespace, worker.Name)] = worker
		p.Assignments[id] = next
		state.Projects[project.ID] = p
		state.Capabilities[capabilityDigest(grant.Token)] = grant.State
		return nil
	}); err != nil {
		return WorkerView{}, err
	}
	if _, err := s.tickets.RecordAssignment(ctx, RecordAssignmentRequest{ProjectID: project.ID, TicketID: ticket.ID, AssignmentID: id}); err != nil {
		return WorkerView{}, err
	}
	resumed, err := client.ResumeSession(ctx, SessionRef{SessionID: worker.Session.SessionID})
	if err != nil {
		return WorkerView{}, err
	}
	if resumed.Status.SessionID != worker.Session.SessionID || resumed.Metadata.Namespace != worker.Namespace || resumed.Metadata.Name != worker.Session.Name || !matchesWorkerSpec(resumed, worker, project.CWD) || (resumed.Status.State != "idle" && resumed.Status.State != "unknown") {
		return WorkerView{}, operationError("outcome_unknown", "worker_resume_unconfirmed", "resumed Session identity or input eligibility is unconfirmed; do not replay inputs")
	}
	if err := s.persistWorkerSession(project.ID, workerKey(worker.Namespace, worker.Name), id, resumed, "active", false, ""); err != nil {
		return WorkerView{}, err
	}
	worker, err = s.worker(project.ID, worker.Namespace, worker.Name)
	return viewWorker(worker), err
}
