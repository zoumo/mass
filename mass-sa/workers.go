package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxToolArgumentBytes  = 1 << 20
	maxWorkerMessageBytes = 64 << 10
)

type sessionClient interface {
	GetSession(context.Context, SessionRef) (Session, error)
	ListSessions(context.Context, SessionQuery) (SessionPage, error)
	CreateSession(context.Context, CreateSessionInput) (Session, error)
	ResumeSession(context.Context, SessionRef) (Session, error)
	CloseSession(context.Context, SessionRef) (Session, error)
	PromptSession(context.Context, SessionRef, string) (json.RawMessage, error)
	History(context.Context, SessionRef, HistoryQuery) (HistoryPage, error)
	Subscribe(context.Context, SessionRef) (sessionEvent, error)
	Close() error
}

// WorkerServiceOptions selects process-local resources and allows tests to
// replace only the MASS transport seam.
type WorkerServiceOptions struct {
	ToolEndpoint  string
	CapabilityDir string
	MCPCommand    string
	OpenMASS      func(context.Context) (sessionClient, error)
}

// WorkerService owns worker Session provisioning, capability scope, and the
// narrow worker collaboration surface. It deliberately has no ticket logic of
// its own; assignment mapping delegates to TicketBridge.
type WorkerService struct {
	config     Config
	store      *StateStore
	tickets    *TicketBridge
	openMASS   func(context.Context) (sessionClient, error)
	capDir     string
	mcpCommand string

	endpointMu sync.RWMutex
	endpoint   string

	now   func() time.Time
	newID func(string) (string, error)

	executionMu sync.Mutex
	executions  map[string]struct{}
}

// WorkerView is the stable identity and current observed state of one worker.
type WorkerView struct {
	ProjectID           string          `json:"projectId"`
	RootTicketID        string          `json:"rootTicketId"`
	TicketID            string          `json:"ticketId"`
	Name                string          `json:"name"`
	Namespace           string          `json:"namespace"`
	SessionID           string          `json:"sessionId,omitempty"`
	AssignmentID        string          `json:"assignmentId"`
	RuntimeRef          string          `json:"runtimeRef"`
	Model               string          `json:"model,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	State               string          `json:"state"`
	InitialInputReceipt json.RawMessage `json:"initialInputReceipt,omitempty"`
}

// ListWorkersResult contains only workers in the authenticated root scope.
type ListWorkersResult struct {
	Workers []WorkerView `json:"workers"`
}

// SendToWorkerResult records durable queue acceptance, not delivery.
type SendToWorkerResult struct {
	EventID   string `json:"eventId"`
	State     string `json:"state"`
	Delivered bool   `json:"delivered"`
}

// BindRouteResult reports one explicit route binding.
type BindRouteResult struct {
	RootTicketID string    `json:"rootTicketId"`
	RouteKey     string    `json:"routeKey"`
	WorkerName   string    `json:"workerName"`
	AssignmentID string    `json:"assignmentId"`
	CreatedAt    time.Time `json:"createdAt"`
}

type createWorkerArgs struct {
	TicketID   string `json:"ticketId"`
	Name       string `json:"name"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Action     string `json:"action,omitempty"`
	RuntimeRef string `json:"runtimeRef,omitempty"`
}

type createTicketArgs struct {
	Authorization *WorkAuthorization `json:"authorization,omitempty"`
	Title         string             `json:"title"`
	Objective     string             `json:"objective"`
	Acceptance    string             `json:"acceptance"`
}

type finishTicketArgs struct {
	TicketID   string `json:"ticketId"`
	Resolution string `json:"resolution"`
	Summary    string `json:"summary"`
}

type stopWorkerArgs struct {
	RootTicketID string `json:"rootTicketId"`
	Name         string `json:"name"`
}

type routerListWorkersArgs struct {
	RootTicketID string `json:"rootTicketId"`
}

type routerGetWorkerArgs struct {
	RootTicketID string `json:"rootTicketId"`
	Name         string `json:"name"`
}

type workerGetArgs struct {
	Name string `json:"name"`
}

type routerSendToWorkerArgs struct {
	RootTicketID string `json:"rootTicketId"`
	Name         string `json:"name"`
	Message      string `json:"message"`
}

type workerSendToWorkerArgs struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type bindRouteArgs struct {
	RootTicketID string `json:"rootTicketId"`
	RouteKey     string `json:"routeKey"`
	WorkerName   string `json:"workerName"`
	AssignmentID string `json:"assignmentId"`
}

type emptyArgs struct{}

// NewWorkerService validates durable and process configuration before any
// Session or capability file is created.
func NewWorkerService(config Config, store *StateStore, tickets *TicketBridge, options WorkerServiceOptions) (*WorkerService, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if store == nil || tickets == nil {
		return nil, operationError("invalid_argument", "invalid_worker_service", "state store and ticket bridge are required")
	}
	capDir := options.CapabilityDir
	if capDir == "" {
		capDir = filepath.Join(filepath.Dir(config.StateFile), "capabilities")
	}
	if !filepath.IsAbs(capDir) {
		return nil, operationError("invalid_argument", "invalid_capability_dir", "capability directory must be absolute")
	}
	mcpCommand := options.MCPCommand
	if mcpCommand == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, operationError("unavailable", "mcp_command_unavailable", "mass-sa executable path is unavailable")
		}
		mcpCommand = executable
	}
	if !filepath.IsAbs(mcpCommand) {
		return nil, operationError("invalid_argument", "invalid_mcp_command", "MCP command must be absolute")
	}
	openMASS := options.OpenMASS
	if openMASS == nil {
		openMASS = func(ctx context.Context) (sessionClient, error) {
			return OpenMASS(ctx, config.MASS)
		}
	}
	service := &WorkerService{
		config:     config,
		store:      store,
		tickets:    tickets,
		openMASS:   openMASS,
		capDir:     filepath.Clean(capDir),
		mcpCommand: filepath.Clean(mcpCommand),
		endpoint:   options.ToolEndpoint,
		now:        func() time.Time { return time.Now().UTC() },
		newID:      newRecordID,
		executions: map[string]struct{}{},
	}
	if options.ToolEndpoint != "" {
		if err := validateToolEndpoint(options.ToolEndpoint); err != nil {
			return nil, err
		}
	}
	return service, nil
}

// SetToolEndpoint connects capability files to the already-listening
// authenticated tool HTTP server. It does not change existing capabilities.
func (s *WorkerService) SetToolEndpoint(endpoint string) error {
	if err := validateToolEndpoint(endpoint); err != nil {
		return err
	}
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()
	s.endpoint = endpoint
	return nil
}

func (s *WorkerService) toolEndpoint() string {
	s.endpointMu.RLock()
	defer s.endpointMu.RUnlock()
	return s.endpoint
}

// EnsureRouterCapability returns the project's stable router credential,
// creating it only when the existing file or digest is missing or invalid.
func (s *WorkerService) EnsureRouterCapability(projectID string) (CapabilityGrant, error) {
	if _, err := s.project(projectID); err != nil {
		return CapabilityGrant{}, err
	}
	endpoint := s.toolEndpoint()
	if endpoint == "" {
		return CapabilityGrant{}, operationError("failed_precondition", "tool_endpoint_missing", "tool HTTP endpoint must be configured before issuing capabilities")
	}
	path := filepath.Join(s.capDir, "router-"+projectID+".json")
	snapshot := s.store.Snapshot()
	for digest, capability := range snapshot.Capabilities {
		if capability.ProjectID != projectID || capability.Role != "router" ||
			!capability.RevokedAt.IsZero() || capability.File != path {
			continue
		}
		content, err := readCapabilityFile(path)
		if err == nil && content.ProjectID == projectID && content.Role == "router" &&
			content.Endpoint == endpoint && capabilityDigest(content.Token) == digest {
			return CapabilityGrant{Token: content.Token, State: capability}, nil
		}
	}
	grant, err := issueCapability(endpoint, path, CapabilityState{
		ProjectID: projectID,
		Role:      "router",
		CreatedAt: s.now(),
	})
	if err != nil {
		return CapabilityGrant{}, err
	}
	digest := capabilityDigest(grant.Token)
	var staleFiles []string
	if err := s.store.updateState(func(state *State) error {
		if _, exists := state.Capabilities[digest]; exists {
			return operationError("already_exists", "capability_exists", "capability digest already exists")
		}
		now := s.now()
		for oldDigest, capability := range state.Capabilities {
			if capability.ProjectID != projectID || capability.Role != "router" ||
				!capability.RevokedAt.IsZero() || oldDigest == digest {
				continue
			}
			capability.RevokedAt = now
			state.Capabilities[oldDigest] = capability
			if capability.File != path {
				staleFiles = append(staleFiles, capability.File)
			}
		}
		state.Capabilities[digest] = grant.State
		return nil
	}); err != nil {
		_ = removeCapabilityFile(path)
		return CapabilityGrant{}, err
	}
	for _, staleFile := range staleFiles {
		_ = removeCapabilityFile(staleFile)
	}
	return grant, nil
}

// CallTool authenticates once and dispatches strictly by the server-side role
// in the capability. Caller parameters never supply project or namespace.
func (s *WorkerService) CallTool(ctx context.Context, token, tool string, arguments json.RawMessage) (any, error) {
	capability, err := s.authenticate(token)
	if err != nil {
		return nil, err
	}
	if len(arguments) > maxToolArgumentBytes {
		return nil, operationError("invalid_argument", "tool_arguments_too_large", "tool arguments exceed the size limit")
	}
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if trimmed := bytes.TrimSpace(arguments); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, operationError("invalid_argument", "invalid_tool_arguments", "tool arguments must be one JSON object")
	}
	if tool == "get_ticket_context" {
		if err := s.authorizeWorkerTool(capability, tool); err != nil {
			return nil, err
		}
		var args ticketContextArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.ticketContext(capability, args.TicketID)
	}

	if capability.Role == "worker" {
		if err := s.authorizeWorkerTool(capability, tool); err != nil {
			return nil, err
		}
		switch tool {
		case "create_child_ticket":
			var args createChildTicketArgs
			if err := decodeToolArgs(arguments, &args); err != nil {
				return nil, err
			}
			return s.createChildTicket(ctx, capability, args)
		case "dispatch_child_ticket":
			var args dispatchChildTicketArgs
			if err := decodeToolArgs(arguments, &args); err != nil {
				return nil, err
			}
			return s.dispatchChildTicket(ctx, capability, args)
		case "list_workers":
			var args emptyArgs
			if err := decodeToolArgs(arguments, &args); err != nil {
				return nil, err
			}
			return s.listWorkers(capability.ProjectID, capability.RootTicketID), nil
		case "get_worker":
			var args workerGetArgs
			if err := decodeToolArgs(arguments, &args); err != nil {
				return nil, err
			}
			return s.getWorker(ctx, capability.ProjectID, capability.RootTicketID, args.Name)
		case "send_to_worker":
			var args workerSendToWorkerArgs
			if err := decodeToolArgs(arguments, &args); err != nil {
				return nil, err
			}
			return s.sendToWorker(capability, capability.RootTicketID, args.Name, args.Message)
		default:
			return nil, toolNotFound()
		}
	}

	switch tool {
	case "reply_trigger":
		var args replyTriggerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.replyTrigger(capability.ProjectID, args)
	case "subscribe_output":
		var args subscribeOutputArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.subscribeOutput(capability.ProjectID, args)
	case "decide_trigger":
		var args decideTriggerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.decideTrigger(ctx, capability.ProjectID, args)
	case "associate_thread":
		var args linkThreadArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.associateThread(capability.ProjectID, args)
	case "create_ticket":
		var args createTicketArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.createTicket(ctx, capability.ProjectID, args)
	case "create_worker":
		var args createWorkerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.createWorker(ctx, capability.ProjectID, args)
	case "stop_worker":
		var args stopWorkerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.stopWorker(ctx, capability.ProjectID, args)
	case "list_workers":
		var args routerListWorkersArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		if _, err := s.rootTicket(capability.ProjectID, args.RootTicketID); err != nil {
			return nil, err
		}
		return s.listWorkers(capability.ProjectID, args.RootTicketID), nil
	case "get_worker":
		var args routerGetWorkerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		if _, err := s.rootTicket(capability.ProjectID, args.RootTicketID); err != nil {
			return nil, err
		}
		return s.getWorker(ctx, capability.ProjectID, args.RootTicketID, args.Name)
	case "send_to_worker":
		var args routerSendToWorkerArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		if _, err := s.rootTicket(capability.ProjectID, args.RootTicketID); err != nil {
			return nil, err
		}
		return s.sendToWorker(capability, args.RootTicketID, args.Name, args.Message)
	case "bind_route":
		var args bindRouteArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.bindRoute(capability.ProjectID, args)
	case "finish_ticket":
		var args finishTicketArgs
		if err := decodeToolArgs(arguments, &args); err != nil {
			return nil, err
		}
		return s.finishTicket(ctx, capability.ProjectID, args)
	default:
		return nil, toolNotFound()
	}
}

func (s *WorkerService) createWorker(ctx context.Context, projectID string, args createWorkerArgs) (WorkerView, error) {
	release, err := s.holdExecution("ticket:" + projectID + ":" + args.TicketID)
	if err != nil {
		return WorkerView{}, err
	}
	defer release()
	if args.Action != "" && args.Action != "retry" && args.Action != "replace" {
		return WorkerView{}, operationError("invalid_argument", "invalid_worker_action", "action must be retry, replace, or omitted")
	}
	if s.toolEndpoint() == "" {
		return WorkerView{}, operationError("failed_precondition", "tool_endpoint_missing", "tool HTTP endpoint must be configured before creating workers")
	}
	project, err := s.project(projectID)
	if err != nil {
		return WorkerView{}, err
	}
	ticket, err := s.ticket(projectID, args.TicketID)
	if err != nil {
		return WorkerView{}, err
	}
	if terminalBusinessState(ticket.BusinessState) {
		return WorkerView{}, operationError("failed_precondition", "ticket_resolved", "resolved tickets cannot receive a worker")
	}
	if ticket.Execution != nil && ticket.AssignmentID == "" {
		if args.RuntimeRef == "" {
			args.RuntimeRef = ticket.Execution.RuntimeRef
		}
		if args.Model == "" {
			args.Model = ticket.Execution.Model
		}
		if args.Effort == "" {
			args.Effort = ticket.Execution.Effort
		}
	}
	if ticket.Authorization != nil && ticket.AssignmentID != "" && args.Action != "replace" {
		current := s.store.Snapshot().Projects[projectID].Assignments[ticket.AssignmentID]
		if args.Model == "" {
			args.Model = current.Model
		}
		if args.Effort == "" {
			args.Effort = current.Effort
		}
	}
	if err := validateWorkerName(args.Name); err != nil {
		return WorkerView{}, err
	}
	model, effort, err := workerConfig(project, args.Model, args.Effort)
	if err != nil {
		return WorkerView{}, err
	}
	runtimeRef := args.RuntimeRef
	if runtimeRef == "" && ticket.AssignmentID != "" && args.Action != "replace" {
		runtimeRef = s.store.Snapshot().Projects[projectID].Assignments[ticket.AssignmentID].RuntimeRef
	}
	if runtimeRef == "" {
		runtimeRef = project.RuntimeRef
	}
	allowed := runtimeRef == project.RuntimeRef
	for _, configured := range project.Worker.RuntimeRefs {
		allowed = allowed || configured == runtimeRef
	}
	if !allowed {
		return WorkerView{}, operationError("invalid_argument", "runtime_not_allowed", "worker Runtime must be configured for this project")
	}
	if err := checkExecutionAuthorization(ticket.Authorization, WorkerExecution{RuntimeRef: runtimeRef, Model: model, Effort: effort}); err != nil {
		return WorkerView{}, err
	}
	project.RuntimeRef = runtimeRef
	for _, pending := range s.store.Snapshot().Projects[projectID].Assignments {
		if pending.TicketID == ticket.ID && pending.ID != ticket.AssignmentID && (pending.Status == "creating" || pending.Status == "unknown") {
			return WorkerView{}, operationError("failed_precondition", "worker_creation_unconfirmed", "prior worker provisioning requires reconciliation before another lifecycle transition")
		}
	}
	if ticket.AssignmentID != "" {
		if args.Action != "replace" {
			return s.continueWorker(ctx, project, ticket, args, model, effort)
		}
		if _, exists := s.store.Snapshot().Projects[projectID].Workers[workerKey(WorkerNamespace(projectID, ticket.RootTicketID), args.Name)]; exists {
			return WorkerView{}, operationError("already_exists", "worker_already_exists", "replacement requires a new worker name")
		}
		if err := s.prepareReplacement(ctx, project, ticket); err != nil {
			return WorkerView{}, err
		}
	}
	namespace := WorkerNamespace(projectID, ticket.RootTicketID)
	key := workerKey(namespace, args.Name)
	if _, exists := s.store.Snapshot().Projects[projectID].Workers[key]; exists {
		return WorkerView{}, operationError("already_exists", "worker_already_exists", "worker name already exists in this root Ticket")
	}
	assignmentID, err := s.newID("a")
	if err != nil {
		return WorkerView{}, err
	}
	capabilityPath := filepath.Join(s.capDir, assignmentID+".json")
	grant, err := issueCapability(s.toolEndpoint(), capabilityPath, CapabilityState{
		ProjectID:    projectID,
		Role:         "worker",
		RootTicketID: ticket.RootTicketID,
		WorkerName:   args.Name,
		AssignmentID: assignmentID,
		CreatedAt:    s.now(),
	})
	if err != nil {
		return WorkerView{}, err
	}
	digest := capabilityDigest(grant.Token)
	now := s.now()
	worker := WorkerState{
		Name:         args.Name,
		Namespace:    namespace,
		ProjectID:    projectID,
		RootTicketID: ticket.RootTicketID,
		TicketID:     ticket.ID,
		AssignmentID: assignmentID,
		Session:      SessionRef{Namespace: namespace, Name: args.Name},
		RuntimeRef:   project.RuntimeRef,
		Model:        model,
		Effort:       effort,
		State:        "creating",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	assignment := AssignmentState{
		ID:           assignmentID,
		ProjectID:    projectID,
		RootTicketID: ticket.RootTicketID,
		TicketID:     ticket.ID,
		WorkerName:   args.Name,
		Namespace:    namespace,
		Session:      worker.Session,
		RuntimeRef:   worker.RuntimeRef,
		Model:        model,
		Effort:       effort,
		Status:       "creating",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.updateState(func(state *State) error {
		projectState := state.Projects[projectID]
		normalizeProject(&projectState)
		if _, exists := projectState.Workers[key]; exists {
			return operationError("already_exists", "worker_already_exists", "worker name already exists in this root Ticket")
		}
		if projectState.Tickets[ticket.ID].AssignmentID != ticket.AssignmentID {
			return operationError("failed_precondition", "assignment_changed", "Ticket already has an Assignment")
		}
		if _, exists := state.Capabilities[digest]; exists {
			return operationError("already_exists", "capability_exists", "capability digest already exists")
		}
		projectState.Workers[key] = worker
		projectState.Assignments[assignmentID] = assignment
		state.Projects[projectID] = projectState
		state.Capabilities[digest] = grant.State
		return nil
	}); err != nil {
		_ = removeCapabilityFile(capabilityPath)
		return WorkerView{}, err
	}

	sessionInput := CreateSessionInput{
		Metadata: SessionMetadata{Namespace: namespace, Name: args.Name},
		Spec: SessionSpec{
			RuntimeRef:   project.RuntimeRef,
			Cwd:          project.CWD,
			SystemPrompt: WorkerSystemPrompt(project, ticket, args.Name),
			Config: &InitialSessionConfig{
				Model:  model,
				Effort: effort,
			},
			MCPServers: []MCPServer{{
				Name:    "mass-sa",
				Type:    "stdio",
				Command: s.mcpCommand,
				Args:    massSAArgs(projectID, "worker", capabilityPath),
			}},
		},
		InitialInput: InitializationPrompt("worker", projectID),
	}
	client, err := s.openMASS(ctx)
	if err != nil {
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "failed", "worker_create_unavailable")
		_ = removeCapabilityFile(capabilityPath)
		return WorkerView{}, err
	}
	session, err := client.CreateSession(ctx, sessionInput)
	if err != nil {
		_ = client.Close()
		status := "failed"
		reason := "worker_create_failed"
		if IsErrorCode(err, "outcome_unknown") {
			status = "unknown"
			reason = "worker_create_unconfirmed"
		} else {
			_ = removeCapabilityFile(capabilityPath)
		}
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, status, reason)
		return WorkerView{}, err
	}
	if session.Status.SessionID == "" || session.Metadata.Namespace != namespace || session.Metadata.Name != args.Name || !matchesWorkerSpec(session, worker, project.CWD) {
		_ = client.Close()
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "unknown", "worker_identity_unconfirmed")
		return WorkerView{}, operationError("outcome_unknown", "worker_identity_unconfirmed", "created Session identity or execution configuration is unconfirmed; inspect MASS before creating another executor")
	}

	if err := s.persistWorkerSession(projectID, key, assignmentID, session, "creating", false, ""); err != nil {
		_ = client.Close()
		return WorkerView{}, err
	}
	if _, err := s.tickets.RecordAssignment(ctx, RecordAssignmentRequest{
		ProjectID:    projectID,
		TicketID:     ticket.ID,
		AssignmentID: assignmentID,
	}); err != nil {
		_ = client.Close()
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "unknown", "worker_ticket_assignment_unconfirmed")
		return WorkerView{}, operationError(
			"outcome_unknown",
			"worker_ticket_assignment_unconfirmed",
			"worker Session "+session.Status.SessionID+" was created but its ticket assignment was not confirmed; do not recreate it",
		)
	}

	settled, settleErr := awaitCreatedSession(ctx, client, session)
	_ = client.Close()
	if settleErr != nil {
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "unknown", "worker_initialization_unconfirmed")
		return WorkerView{}, operationError(
			"outcome_unknown",
			"worker_initialization_unconfirmed",
			"worker Session "+session.Status.SessionID+" was created but its initialization state was not confirmed; do not recreate it",
		)
	}
	if settled.Status.SessionID != session.Status.SessionID ||
		settled.Metadata.Namespace != namespace || settled.Metadata.Name != args.Name || !matchesWorkerSpec(settled, worker, project.CWD) {
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "unknown", "worker_identity_unconfirmed")
		return WorkerView{}, operationError(
			"outcome_unknown",
			"worker_identity_unconfirmed",
			"worker Session identity changed while initialization was settling; do not recreate it",
		)
	}
	if err := s.persistWorkerSession(projectID, key, assignmentID, settled, "active", false, ""); err != nil {
		_ = s.markWorkerCreateFailed(projectID, key, assignmentID, "unknown", "worker_state_unconfirmed")
		return WorkerView{}, operationError(
			"outcome_unknown",
			"worker_state_unconfirmed",
			"worker Session "+session.Status.SessionID+" was created but its state was not persisted; do not recreate it",
		)
	}
	worker, err = s.worker(projectID, namespace, args.Name)
	if err != nil {
		return WorkerView{}, err
	}
	return viewWorker(worker), nil
}

func (s *WorkerService) persistWorkerSession(projectID, key, assignmentID string, session Session, assignmentStatus string, reconciliationRequired bool, reconciliationReason string) error {
	return s.store.updateProject(projectID, func(projectState *ProjectState) error {
		current, exists := projectState.Workers[key]
		if !exists || current.AssignmentID != assignmentID {
			return operationError("failed_precondition", "worker_creating_mismatch", "worker creation record changed before Session confirmation")
		}
		now := s.now()
		current.Session = SessionRef{
			SessionID: session.Status.SessionID,
			Namespace: session.Metadata.Namespace,
			Name:      session.Metadata.Name,
		}
		current.State = session.Status.State
		current.InitialInputReceipt = append(json.RawMessage(nil), session.InitialInputReceipt...)
		current.UpdatedAt = now
		projectState.Workers[key] = current
		currentAssignment := projectState.Assignments[assignmentID]
		currentAssignment.Session = current.Session
		currentAssignment.Status = assignmentStatus
		currentAssignment.ReconciliationRequired = reconciliationRequired
		currentAssignment.ReconciliationReason = reconciliationReason
		currentAssignment.UpdatedAt = now
		projectState.Assignments[assignmentID] = currentAssignment
		return nil
	})
}

func (s *WorkerService) createTicket(ctx context.Context, projectID string, args createTicketArgs) (TicketState, error) {
	if err := s.validateRootAuthorization(projectID, args.Authorization); err != nil {
		return TicketState{}, err
	}
	return s.tickets.CreateTicket(ctx, CreateTicketRequest{
		Authorization: args.Authorization,
		ProjectID:     projectID,
		Title:         args.Title,
		Objective:     args.Objective,
		Acceptance:    args.Acceptance,
	})
}

func (s *WorkerService) finishTicket(ctx context.Context, projectID string, args finishTicketArgs) (TicketState, error) {
	resolution := TicketResolution(args.Resolution)
	if err := resolution.validate(); err != nil {
		return TicketState{}, err
	}
	ticket, err := s.ticket(projectID, args.TicketID)
	if err != nil {
		return TicketState{}, err
	}
	if terminalBusinessState(ticket.BusinessState) && resolution != ResolutionReopened {
		return TicketState{}, operationError("failed_precondition", "ticket_already_resolved", "ticket already has a resolution: "+ticket.BusinessState)
	}
	if resolution != ResolutionCompleted {
		return s.tickets.FinishTicket(ctx, FinishTicketRequest{ProjectID: projectID, TicketID: ticket.ID, Resolution: resolution, Summary: args.Summary})
	}
	if ticket.AssignmentID == "" {
		return TicketState{}, operationError("failed_precondition", "ticket_report_missing", "ticket has no current Assignment report")
	}
	projectState := s.store.Snapshot().Projects[projectID]
	assignment, exists := projectState.Assignments[ticket.AssignmentID]
	if !exists || assignment.TicketID != ticket.ID || assignment.ProjectID != projectID {
		return TicketState{}, operationError("failed_precondition", "assignment_stale", "ticket references an expired Assignment")
	}
	validReport := false
	for _, report := range ticket.Reports {
		if report.AssignmentID != ticket.AssignmentID {
			continue
		}
		if report.Status != "confirmed" {
			continue
		}
		if report.ID != "" && report.Body != "" {
			validReport = true
		}
	}
	if !validReport {
		return TicketState{}, operationError("failed_precondition", "ticket_report_missing", "current Assignment has no confirmed worker report")
	}
	finished, err := s.tickets.FinishTicket(ctx, FinishTicketRequest{
		ProjectID:  projectID,
		TicketID:   ticket.ID,
		Resolution: resolution,
		Summary:    args.Summary,
	})
	if err != nil {
		return TicketState{}, err
	}
	return finished, nil
}

func (s *WorkerService) markWorkerCreateFailed(projectID, key, assignmentID, status, reconciliationReason string) error {
	return s.store.updateState(func(state *State) error {
		project := state.Projects[projectID]
		normalizeProject(&project)
		worker := project.Workers[key]
		if worker.AssignmentID != assignmentID {
			return nil
		}
		worker.State = status
		worker.UpdatedAt = s.now()
		project.Workers[key] = worker
		assignment := project.Assignments[assignmentID]
		assignment.Status = status
		assignment.ReconciliationRequired = status == "unknown"
		assignment.ReconciliationReason = reconciliationReason
		assignment.UpdatedAt = worker.UpdatedAt
		project.Assignments[assignmentID] = assignment
		if status != "unknown" {
			for digest, capability := range state.Capabilities {
				if capability.AssignmentID == assignmentID {
					capability.RevokedAt = worker.UpdatedAt
					state.Capabilities[digest] = capability
				}
			}
		}
		state.Projects[projectID] = project
		return nil
	})
}

func (s *WorkerService) stopWorker(ctx context.Context, projectID string, args stopWorkerArgs) (WorkerView, error) {
	root, err := s.rootTicket(projectID, args.RootTicketID)
	if err != nil {
		return WorkerView{}, err
	}
	namespace := WorkerNamespace(projectID, root.ID)
	worker, err := s.worker(projectID, namespace, args.Name)
	if err != nil {
		return WorkerView{}, err
	}
	if worker.Session.SessionID == "" {
		return WorkerView{}, operationError("failed_precondition", "worker_session_missing", "worker has no confirmed MASS Session")
	}
	return s.closeWorkerResources(ctx, projectID, worker)
}

func (s *WorkerService) closeWorkerResources(ctx context.Context, projectID string, worker WorkerState) (WorkerView, error) {
	release, err := s.holdExecution(worker.AssignmentID)
	if err != nil {
		return WorkerView{}, err
	}
	defer release()
	client, err := s.openMASS(ctx)
	if err != nil {
		return WorkerView{}, err
	}
	defer client.Close()
	assignment := s.store.Snapshot().Projects[projectID].Assignments[worker.AssignmentID]
	project, _ := s.project(projectID)
	if _, err := s.checkedSession(ctx, client, project, worker, assignment); err != nil {
		return WorkerView{}, err
	}
	if err := s.closeFencedWorker(ctx, client, projectID, worker); err != nil {
		return WorkerView{}, err
	}
	worker.State = "offline"
	return viewWorker(worker), nil
}

func (s *WorkerService) listWorkers(projectID, rootTicketID string) ListWorkersResult {
	project := s.store.Snapshot().Projects[projectID]
	namespace := WorkerNamespace(projectID, rootTicketID)
	result := ListWorkersResult{Workers: []WorkerView{}}
	for _, worker := range project.Workers {
		if worker.ProjectID == projectID && worker.RootTicketID == rootTicketID && worker.Namespace == namespace {
			result.Workers = append(result.Workers, viewWorker(worker))
		}
	}
	sort.Slice(result.Workers, func(i, j int) bool { return result.Workers[i].Name < result.Workers[j].Name })
	return result
}

func (s *WorkerService) getWorker(ctx context.Context, projectID, rootTicketID, name string) (WorkerView, error) {
	if _, err := s.rootTicket(projectID, rootTicketID); err != nil {
		return WorkerView{}, err
	}
	namespace := WorkerNamespace(projectID, rootTicketID)
	worker, err := s.worker(projectID, namespace, name)
	if err != nil {
		return WorkerView{}, err
	}
	if worker.Session.SessionID == "" || worker.State == "stopped" || worker.State == "offline" {
		return viewWorker(worker), nil
	}
	client, err := s.openMASS(ctx)
	if err != nil {
		return WorkerView{}, err
	}
	session, getErr := client.GetSession(ctx, SessionRef{SessionID: worker.Session.SessionID})
	_ = client.Close()
	if getErr != nil {
		return WorkerView{}, getErr
	}
	worker.State = session.Status.State
	worker.UpdatedAt = s.now()
	_ = s.store.updateProject(projectID, func(project *ProjectState) error {
		current, exists := project.Workers[workerKey(namespace, name)]
		if !exists || current.AssignmentID != worker.AssignmentID {
			return operationError("failed_precondition", "worker_assignment_changed", "worker Assignment changed while refreshing state")
		}
		current.State = worker.State
		current.UpdatedAt = worker.UpdatedAt
		project.Workers[workerKey(namespace, name)] = current
		return nil
	})
	return viewWorker(worker), nil
}

// ListWorkers exposes the root Ticket read model to authenticated
// management callers. The project and root pair is validated before any
// worker state is returned.
func (s *WorkerService) ListWorkers(projectID, rootTicketID string) (ListWorkersResult, error) {
	if _, err := s.project(projectID); err != nil {
		return ListWorkersResult{}, err
	}
	if _, err := s.rootTicket(projectID, rootTicketID); err != nil {
		return ListWorkersResult{}, err
	}
	return s.listWorkers(projectID, rootTicketID), nil
}

func (s *WorkerService) sendToWorker(source CapabilityState, rootTicketID, targetName, message string) (SendToWorkerResult, error) {
	if err := validateWorkerMessage(message); err != nil {
		return SendToWorkerResult{}, err
	}
	namespace := WorkerNamespace(source.ProjectID, rootTicketID)
	target, err := s.worker(source.ProjectID, namespace, targetName)
	if err != nil {
		return SendToWorkerResult{}, err
	}
	switch target.State {
	case "idle", "running", "requires_action":
	default:
		return SendToWorkerResult{}, operationError("failed_precondition", "worker_unavailable", "worker is not available for collaboration")
	}
	project := s.store.Snapshot().Projects[source.ProjectID]
	assignment, exists := project.Assignments[target.AssignmentID]
	if !exists || assignment.Status != "active" {
		return SendToWorkerResult{}, operationError("failed_precondition", "worker_unavailable", "worker Assignment is not active")
	}
	ticket, exists := project.Tickets[assignment.TicketID]
	if !exists || ticket.AssignmentID != target.AssignmentID {
		return SendToWorkerResult{}, operationError("failed_precondition", "worker_unavailable", "worker Assignment is no longer current for its ticket")
	}
	eventID, err := s.newID("e")
	if err != nil {
		return SendToWorkerResult{}, err
	}
	payload, err := json.Marshal(map[string]string{"message": strings.TrimSpace(message)})
	if err != nil {
		return SendToWorkerResult{}, operationError("invalid_argument", "invalid_worker_message", "worker message cannot be encoded")
	}
	sourceName := "router"
	if source.Role == "worker" {
		sourceName = "worker:" + source.WorkerName
	}
	now := s.now()
	event := InboxEventState{
		ID:                 eventID,
		ProjectID:          source.ProjectID,
		RootTicketID:       rootTicketID,
		Source:             sourceName,
		Kind:               "worker_message",
		Summary:            summarizeWorkerMessage(message),
		Payload:            payload,
		TargetAssignmentID: target.AssignmentID,
		TargetWorkerName:   target.Name,
		TargetSession:      target.Session,
		RoutingPath:        "direct",
		State:              "queued",
		AcceptedAt:         now,
		UpdatedAt:          now,
	}
	projectConfig, _ := projectConfigByID(s.config, source.ProjectID)
	if len(workerPromptFromEvents([]InboxEventState{event})) > projectConfig.Inbox.MaxPromptBytes {
		return SendToWorkerResult{}, operationError("invalid_argument", "event_prompt_too_large", "one event exceeds the configured prompt budget")
	}
	if err := s.store.updateProject(source.ProjectID, func(project *ProjectState) error {
		if source.Role == "worker" {
			sender := project.Workers[workerKey(WorkerNamespace(source.ProjectID, source.RootTicketID), source.WorkerName)]
			assignment := project.Assignments[source.AssignmentID]
			if sender.AssignmentID != source.AssignmentID || assignment.Status != "active" || project.Tickets[assignment.TicketID].AssignmentID != source.AssignmentID {
				return capabilityInvalid()
			}
		}
		current := project.Workers[workerKey(namespace, targetName)]
		if current.AssignmentID != target.AssignmentID || project.Assignments[target.AssignmentID].Status != "active" || project.Tickets[target.TicketID].AssignmentID != target.AssignmentID {
			return operationError("failed_precondition", "worker_unavailable", "target Assignment changed before input acceptance")
		}
		if _, exists := project.Inbox[event.ID]; exists {
			return operationError("already_exists", "event_exists", "inbox event already exists")
		}
		project.NextInboxSeq++
		event.Sequence = project.NextInboxSeq
		project.Inbox[event.ID] = event
		return nil
	}); err != nil {
		return SendToWorkerResult{}, err
	}
	return SendToWorkerResult{EventID: event.ID, State: event.State, Delivered: false}, nil
}

func (s *WorkerService) bindRoute(projectID string, args bindRouteArgs) (BindRouteResult, error) {
	root, err := s.rootTicket(projectID, args.RootTicketID)
	if err != nil {
		return BindRouteResult{}, err
	}
	if err := validateRouteKey(args.RouteKey, true); err != nil {
		return BindRouteResult{}, err
	}
	if err := validateWorkerName(args.WorkerName); err != nil {
		return BindRouteResult{}, err
	}
	namespace := WorkerNamespace(projectID, root.ID)
	worker, err := s.worker(projectID, namespace, args.WorkerName)
	if err != nil {
		return BindRouteResult{}, err
	}
	if worker.AssignmentID != args.AssignmentID {
		return BindRouteResult{}, operationError("failed_precondition", "assignment_mismatch", "route binding must target the worker's current Assignment")
	}
	project := s.store.Snapshot().Projects[projectID]
	assignment, exists := project.Assignments[worker.AssignmentID]
	if !exists || assignment.Status != "active" {
		return BindRouteResult{}, operationError("failed_precondition", "assignment_inactive", "route binding must target an active Assignment")
	}
	ticket, exists := project.Tickets[assignment.TicketID]
	if !exists || ticket.AssignmentID != assignment.ID {
		return BindRouteResult{}, operationError("failed_precondition", "assignment_stale", "route binding must target the ticket's current Assignment")
	}
	binding := RouteBindingState{
		ProjectID:    projectID,
		RootTicketID: args.RootTicketID,
		RouteKey:     args.RouteKey,
		WorkerName:   args.WorkerName,
		AssignmentID: args.AssignmentID,
		CreatedAt:    s.now(),
	}
	if err := s.store.updateProject(projectID, func(project *ProjectState) error {
		currentWorker, exists := project.Workers[workerKey(namespace, args.WorkerName)]
		if !exists || currentWorker.ProjectID != projectID || currentWorker.RootTicketID != args.RootTicketID ||
			currentWorker.AssignmentID != args.AssignmentID {
			return operationError("failed_precondition", "assignment_mismatch", "route binding must target the worker's current Assignment")
		}
		currentAssignment, exists := project.Assignments[args.AssignmentID]
		if !exists || currentAssignment.Status != "active" {
			return operationError("failed_precondition", "assignment_inactive", "route binding must target an active Assignment")
		}
		currentTicket, exists := project.Tickets[currentAssignment.TicketID]
		if !exists || currentTicket.AssignmentID != currentAssignment.ID {
			return operationError("failed_precondition", "assignment_stale", "route binding must target the ticket's current Assignment")
		}
		project.RouteBindings[routeBindingKey(args.RootTicketID, args.RouteKey)] = binding
		return nil
	}); err != nil {
		return BindRouteResult{}, err
	}
	return BindRouteResult{
		RootTicketID: binding.RootTicketID,
		RouteKey:     binding.RouteKey,
		WorkerName:   binding.WorkerName,
		AssignmentID: binding.AssignmentID,
		CreatedAt:    binding.CreatedAt,
	}, nil
}

func (s *WorkerService) authenticate(token string) (CapabilityState, error) {
	if token == "" {
		return CapabilityState{}, capabilityInvalid()
	}
	state := s.store.Snapshot()
	capability, exists := state.Capabilities[capabilityDigest(token)]
	if !exists || !capability.RevokedAt.IsZero() {
		return CapabilityState{}, capabilityInvalid()
	}
	if _, exists := projectConfigByID(s.config, capability.ProjectID); !exists {
		return CapabilityState{}, capabilityInvalid()
	}
	if capability.Role == "worker" {
		project := state.Projects[capability.ProjectID]
		namespace := WorkerNamespace(capability.ProjectID, capability.RootTicketID)
		worker, exists := project.Workers[workerKey(namespace, capability.WorkerName)]
		if !exists || worker.AssignmentID != capability.AssignmentID {
			return CapabilityState{}, capabilityInvalid()
		}
		assignment, exists := project.Assignments[worker.AssignmentID]
		if !exists || assignment.Status != "active" || project.Tickets[assignment.TicketID].AssignmentID != assignment.ID {
			return CapabilityState{}, capabilityInvalid()
		}
	}
	return capability, nil
}

func (s *WorkerService) project(projectID string) (ProjectConfig, error) {
	project, exists := projectConfigByID(s.config, projectID)
	if !exists {
		return ProjectConfig{}, operationError("not_found", "project_not_found", "project is not configured")
	}
	return project, nil
}

func (s *WorkerService) rootTicket(projectID, rootTicketID string) (TicketState, error) {
	ticket, err := s.ticket(projectID, rootTicketID)
	if err != nil {
		return TicketState{}, err
	}
	if ticket.RootTicketID != ticket.ID || ticket.ParentTicketID != "" {
		return TicketState{}, workerNotFound()
	}
	return ticket, nil
}

func (s *WorkerService) ticket(projectID, ticketID string) (TicketState, error) {
	if ticketID == "" {
		return TicketState{}, operationError("invalid_argument", "invalid_ticket_id", "ticket ID is required")
	}
	ticket, exists := s.store.Snapshot().Projects[projectID].Tickets[ticketID]
	if !exists || ticket.ProjectID != projectID {
		return TicketState{}, workerNotFound()
	}
	return ticket, nil
}

func (s *WorkerService) worker(projectID, namespace, name string) (WorkerState, error) {
	if err := validateWorkerName(name); err != nil {
		return WorkerState{}, workerNotFound()
	}
	worker, exists := s.store.Snapshot().Projects[projectID].Workers[workerKey(namespace, name)]
	if !exists || worker.ProjectID != projectID || worker.Namespace != namespace || worker.Name != name {
		return WorkerState{}, workerNotFound()
	}
	return worker, nil
}

func projectConfigByID(config Config, projectID string) (ProjectConfig, bool) {
	for _, project := range config.Projects {
		if project.ID == projectID {
			return project, true
		}
	}
	return ProjectConfig{}, false
}

func workerConfig(project ProjectConfig, model, effort string) (string, string, error) {
	if model == "" {
		model = project.Worker.Model
	}
	if effort == "" {
		effort = project.Worker.Effort
	}
	if model != "" {
		if err := validateToolText("model", model, false); err != nil {
			return "", "", err
		}
	}
	if err := validateToolText("effort", effort, true); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(effort) == "" {
		return "", "", operationError("invalid_argument", "invalid_worker_config", "worker effort must not be blank")
	}
	return model, effort, nil
}

func massSAArgs(projectID, role, capabilityPath string) []string {
	return []string{
		"mcp",
		"--project", projectID,
		"--role", role,
		"--capability", capabilityPath,
	}
}

func massSAMCPServer(projectID, role, command, capabilityPath string) MCPServer {
	return MCPServer{
		Name:    "mass-sa",
		Type:    "stdio",
		Command: command,
		Args:    massSAArgs(projectID, role, capabilityPath),
	}
}

func validateWorkerName(name string) error {
	if !validDNSSubdomain(name) || uuidPattern.MatchString(name) {
		return operationError("invalid_argument", "invalid_worker_name", "worker name must be a non-UUID DNS subdomain")
	}
	return nil
}

func validateWorkerMessage(message string) error {
	if err := validateToolText("message", message, true); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		return operationError("invalid_argument", "invalid_worker_message", "worker message must not be blank")
	}
	if len(message) > maxWorkerMessageBytes {
		return operationError("invalid_argument", "worker_message_too_large", "worker message exceeds the size limit")
	}
	return nil
}

func validateToolText(name, value string, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return operationError("invalid_argument", "invalid_tool_argument", name+" must be valid UTF-8 without NUL")
	}
	if required && value == "" {
		return operationError("invalid_argument", "invalid_tool_argument", name+" is required")
	}
	return nil
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return operationError("invalid_argument", "invalid_tool_arguments", "tool arguments do not match the declared schema")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return operationError("invalid_argument", "invalid_tool_arguments", "tool arguments must contain one JSON object")
	}
	return nil
}

func workerKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func routeBindingKey(rootTicketID, routeKey string) string {
	return rootTicketID + "\x00" + routeKey
}

func viewWorker(worker WorkerState) WorkerView {
	return WorkerView{
		ProjectID:           worker.ProjectID,
		RootTicketID:        worker.RootTicketID,
		TicketID:            worker.TicketID,
		Name:                worker.Name,
		Namespace:           worker.Namespace,
		SessionID:           worker.Session.SessionID,
		AssignmentID:        worker.AssignmentID,
		RuntimeRef:          worker.RuntimeRef,
		Model:               worker.Model,
		Effort:              worker.Effort,
		State:               worker.State,
		InitialInputReceipt: append(json.RawMessage(nil), worker.InitialInputReceipt...),
	}
}

func summarizeWorkerMessage(message string) string {
	const maxRunes = 160
	runes := []rune(strings.TrimSpace(message))
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return string(runes)
}

func workerNotFound() error {
	return operationError("not_found", "worker_not_found", "worker was not found in this root Ticket")
}

func capabilityInvalid() error {
	return operationError("unauthorized", "capability_invalid", "capability is invalid or revoked")
}

func toolNotFound() error {
	return operationError("not_found", "tool_not_found", "tool is not available")
}
