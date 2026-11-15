package sa

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateVersion = 3

// State is the durable mass-sa coordination state.
type State struct {
	Version      int                        `json:"version"`
	Projects     map[string]ProjectState    `json:"projects"`
	Capabilities map[string]CapabilityState `json:"capabilities,omitempty"`
}

// ProjectState keeps the durable references for one project.
type ProjectState struct {
	Delegations   map[string]DelegationState    `json:"delegations,omitempty"`
	Outputs       map[string]OutputRecord       `json:"outputs,omitempty"`
	Consumers     map[string]OutputConsumer     `json:"outputConsumers,omitempty"`
	NextOutputSeq uint64                        `json:"nextOutputSequence,omitempty"`
	Triggers      map[string]TriggerState       `json:"triggers,omitempty"`
	Threads       map[string]ThreadState        `json:"threads,omitempty"`
	Router        *RouterState                  `json:"router,omitempty"`
	Tickets       map[string]TicketState        `json:"tickets,omitempty"`
	Workers       map[string]WorkerState        `json:"workers,omitempty"`
	Assignments   map[string]AssignmentState    `json:"assignments,omitempty"`
	RouteBindings map[string]RouteBindingState  `json:"routeBindings,omitempty"`
	Inbox         map[string]InboxEventState    `json:"inbox,omitempty"`
	Deliveries    map[string]DeliveryBatchState `json:"deliveries,omitempty"`
	NextInboxSeq  uint64                        `json:"nextInboxSequence,omitempty"`
}

// RouterState identifies the project's durable router Session reference.
type RouterState struct {
	SessionRef
	RuntimeRef          string          `json:"runtimeRef"`
	Model               string          `json:"model,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	CapabilityFile      string          `json:"capabilityFile"`
	CapabilityDigest    string          `json:"capabilityDigest"`
	ObservedState       string          `json:"observedState"`
	InitialInputReceipt json.RawMessage `json:"initialInputReceipt,omitempty"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

// IssueReference is the durable identity of one Multica Issue.
type IssueReference struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier,omitempty"`
}

// ReportState records one worker report written to a ticket Issue.
type ReportState struct {
	AssignmentID  string     `json:"assignmentId"`
	Session       SessionRef `json:"session"`
	SourceEventID string     `json:"sourceEventId"`
	SourceItemID  string     `json:"sourceItemId,omitempty"`
	SourceTurnID  string     `json:"sourceTurnId,omitempty"`
	Status        string     `json:"status"`
	Structured    bool       `json:"structured"`
	ParseError    string     `json:"parseError,omitempty"`
	CommentID     string     `json:"commentId,omitempty"`
	ErrorReason   string     `json:"errorReason,omitempty"`
	ReportedAt    time.Time  `json:"reportedAt"`
}

// TicketState is one business task backed by a child Multica Issue.
type TicketState struct {
	Authorization       *WorkAuthorization `json:"authorization,omitempty"`
	Execution           *WorkerExecution   `json:"execution,omitempty"`
	CreatedByAssignment string             `json:"createdByAssignment,omitempty"`
	CreatedBySession    SessionRef         `json:"createdBySession,omitempty"`
	DelegationRequestID string             `json:"delegationRequestId,omitempty"`
	Depth               int                `json:"depth,omitempty"`
	BusinessState       string             `json:"businessState,omitempty"`
	Revision            uint64             `json:"revision,omitempty"`
	OutputTriggerID     string             `json:"outputTriggerId,omitempty"`
	OutputSubscribers   []OutputTarget     `json:"outputSubscribers,omitempty"`
	Transition          *TicketTransition  `json:"transition,omitempty"`
	ParentTicketID      string             `json:"parentTicketId,omitempty"`
	RootTicketID        string             `json:"rootTicketId,omitempty"`
	Namespace           string             `json:"namespace,omitempty"`
	ID                  string             `json:"id"`
	ProjectID           string             `json:"projectId"`
	Title               string             `json:"title"`
	Objective           string             `json:"objective"`
	Acceptance          string             `json:"acceptance"`
	Issue               IssueReference     `json:"issue"`
	Status              string             `json:"status"`
	AssignmentID        string             `json:"assignmentId,omitempty"`
	BusinessCommentID   string             `json:"businessCommentId,omitempty"`
	Reports             []ReportState      `json:"reports,omitempty"`
	CreatedAt           time.Time          `json:"createdAt"`
	UpdatedAt           time.Time          `json:"updatedAt"`
}

// CapabilityState is the durable, non-secret scope of one tool capability.
// The map key is the SHA-256 digest of the bearer token.
type CapabilityState struct {
	ProjectID    string    `json:"projectId"`
	Role         string    `json:"role"`
	RootTicketID string    `json:"rootTicketId,omitempty"`
	WorkerName   string    `json:"workerName,omitempty"`
	AssignmentID string    `json:"assignmentId,omitempty"`
	File         string    `json:"filePath"`
	CreatedAt    time.Time `json:"createdAt"`
	RevokedAt    time.Time `json:"revokedAt,omitempty"`
}

// WorkerState maps one MASS Session and Assignment to a root ticket.
type WorkerState struct {
	Name                string          `json:"name"`
	Namespace           string          `json:"namespace"`
	ProjectID           string          `json:"projectId"`
	RootTicketID        string          `json:"rootTicketId"`
	TicketID            string          `json:"ticketId"`
	AssignmentID        string          `json:"assignmentId"`
	Session             SessionRef      `json:"session"`
	RuntimeRef          string          `json:"runtimeRef"`
	Model               string          `json:"model,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	State               string          `json:"state"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	StoppedAt           time.Time       `json:"stoppedAt,omitempty"`
	InitialInputReceipt json.RawMessage `json:"initialInputReceipt,omitempty"`
}

// AssignmentState is one worker execution attempt for a ticket.
type AssignmentState struct {
	ID                     string     `json:"id"`
	ProjectID              string     `json:"projectId"`
	RootTicketID           string     `json:"rootTicketId"`
	TicketID               string     `json:"ticketId"`
	WorkerName             string     `json:"workerName"`
	Namespace              string     `json:"namespace"`
	Session                SessionRef `json:"session"`
	RuntimeRef             string     `json:"runtimeRef"`
	Model                  string     `json:"model,omitempty"`
	Effort                 string     `json:"effort,omitempty"`
	Status                 string     `json:"status"`
	ExecutionID            string     `json:"executionId,omitempty"`
	ExecutionState         string     `json:"executionState,omitempty"`
	AwaitingResumedInput   bool       `json:"awaitingResumedInput,omitempty"`
	PromptAcceptedAt       time.Time  `json:"promptAcceptedAt,omitempty"`
	ReconciliationRequired bool       `json:"reconciliationRequired,omitempty"`
	ReconciliationReason   string     `json:"reconciliationReason,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	StoppedAt              time.Time  `json:"stoppedAt,omitempty"`
}

// RouteBindingState binds one root Ticket route key to the current Assignment.
type RouteBindingState struct {
	ProjectID    string    `json:"projectId"`
	RootTicketID string    `json:"rootTicketId"`
	RouteKey     string    `json:"routeKey"`
	WorkerName   string    `json:"workerName"`
	AssignmentID string    `json:"assignmentId"`
	CreatedAt    time.Time `json:"createdAt"`
}

// InboxEventState is a durably accepted event. T05 owns routing and batching;
// T04 execution marks prompt-accepted events delivered or unknown.
type InboxEventState struct {
	ChildLifecycle     *ChildLifecycleFact `json:"childLifecycle,omitempty"`
	TriggerID          string              `json:"triggerId,omitempty"`
	ThreadID           string              `json:"threadId,omitempty"`
	TicketID           string              `json:"ticketId,omitempty"`
	ID                 string              `json:"id"`
	Sequence           uint64              `json:"sequence,omitempty"`
	ProjectID          string              `json:"projectId"`
	RootTicketID       string              `json:"rootTicketId"`
	RouteKey           string              `json:"routeKey,omitempty"`
	Source             string              `json:"source"`
	Kind               string              `json:"kind"`
	Summary            string              `json:"summary"`
	Payload            json.RawMessage     `json:"payload"`
	TargetAssignmentID string              `json:"targetAssignmentId"`
	TargetWorkerName   string              `json:"targetWorkerName"`
	TargetSession      SessionRef          `json:"targetSession"`
	RoutingPath        string              `json:"routingPath"`
	State              string              `json:"state"`
	SourceEventID      string              `json:"sourceEventId,omitempty"`
	ExecutionID        string              `json:"executionId,omitempty"`
	ErrorReason        string              `json:"errorReason,omitempty"`
	AcceptedAt         time.Time           `json:"acceptedAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	DeliveredAt        time.Time           `json:"deliveredAt,omitempty"`
}

// DeliveryBatchState records one prompt attempted for accepted inbox events.
// Event identities and delivery state stay durable even after Session history
// is released.
type DeliveryBatchState struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"projectId"`
	RootTicketID       string     `json:"rootTicketId"`
	TargetAssignmentID string     `json:"targetAssignmentId,omitempty"`
	TargetWorkerName   string     `json:"targetWorkerName"`
	TargetSession      SessionRef `json:"targetSession"`
	RoutingPath        string     `json:"routingPath"`
	EventIDs           []string   `json:"eventIds"`
	ExecutionID        string     `json:"executionId,omitempty"`
	PromptBytes        int        `json:"promptBytes"`
	State              string     `json:"state"`
	ErrorReason        string     `json:"errorReason,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	DeliveredAt        time.Time  `json:"deliveredAt,omitempty"`
	ResultState        string     `json:"resultState,omitempty"`
	FinalEventID       string     `json:"finalEventId,omitempty"`
	FinalCommentID     string     `json:"finalCommentId,omitempty"`
	Structured         bool       `json:"structured,omitempty"`
	ParseError         string     `json:"parseError,omitempty"`
	ResultError        string     `json:"resultError,omitempty"`
}

// SessionRef is a MASS Session identity. Exactly one addressing form is used
// when calling massd; stored references include both stable forms.
type SessionRef struct {
	SessionID string `json:"sessionId,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// StateStore owns a single atomic JSON state file.
type StateStore struct {
	path  string
	mu    sync.Mutex
	state State
}

// OpenStateStore loads the state file or starts with empty state.
func OpenStateStore(path string) (*StateStore, error) {
	store := &StateStore{
		path: path,
		state: State{
			Version:      stateVersion,
			Projects:     map[string]ProjectState{},
			Capabilities: map[string]CapabilityState{},
		},
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, operationError("unavailable", "state_unavailable", "state file is unavailable")
	}
	var loaded State
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, operationError("invalid_argument", "invalid_state", "state JSON is invalid")
	}
	if loaded.Version != stateVersion {
		return nil, operationError("failed_precondition", "unsupported_state_version", fmt.Sprintf("state version %d is not supported", loaded.Version))
	}
	if loaded.Projects == nil {
		loaded.Projects = map[string]ProjectState{}
	}
	if loaded.Capabilities == nil {
		loaded.Capabilities = map[string]CapabilityState{}
	}
	store.state = loaded
	return store, nil
}

// Snapshot returns a detached copy of the current state.
func (s *StateStore) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

// SetRouter commits one project's router reference with an atomic replacement.
func (s *StateStore) SetRouter(projectID string, router RouterState) error {
	return s.SetRouterAndCapability(projectID, router, nil, "")
}

// SetRouterAndCapability commits a router reference and its capability digest
// in one atomic state replacement.
func (s *StateStore) SetRouterAndCapability(projectID string, router RouterState, capability *CapabilityState, digest string) error {
	return s.updateState(func(state *State) error {
		project := state.Projects[projectID]
		project.Router = &router
		if capability != nil {
			if digest == "" {
				return operationError("invalid_argument", "invalid_capability", "capability digest is required")
			}
			state.Capabilities[digest] = *capability
		}
		state.Projects[projectID] = project
		return nil
	})
}

// updateState commits one whole-state mutation with an atomic replacement.
// The callback receives a detached copy; state is replaced only after the
// callback succeeds and the new file is committed.
func (s *StateStore) updateState(mutate func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneState(s.state)
	if err := mutate(&next); err != nil {
		return err
	}
	normalizeProjectMaps(&next)
	committed, err := writeStateFile(s.path, next)
	if committed {
		s.state = next
	}
	if err != nil {
		return err
	}
	return nil
}

// updateProject commits one project mutation with an atomic replacement.
// The callback receives a detached copy; state is replaced only after the
// callback succeeds and the new file is committed.
func (s *StateStore) updateProject(projectID string, mutate func(*ProjectState) error) error {
	return s.updateState(func(state *State) error {
		project := state.Projects[projectID]
		normalizeProject(&project)
		if err := mutate(&project); err != nil {
			return err
		}
		state.Projects[projectID] = project
		return nil
	})
}

func cloneState(state State) State {
	clone := State{
		Version:      state.Version,
		Projects:     make(map[string]ProjectState, len(state.Projects)),
		Capabilities: make(map[string]CapabilityState, len(state.Capabilities)),
	}
	for hash, capability := range state.Capabilities {
		clone.Capabilities[hash] = capability
	}
	for id, project := range state.Projects {
		delegations := make(map[string]DelegationState, len(project.Delegations))
		for key, delegation := range project.Delegations {
			delegations[key] = delegation
		}
		project.Delegations = delegations
		project.Outputs = cloneOutputs(project.Outputs)
		project.Consumers = cloneConsumers(project.Consumers)
		triggers := make(map[string]TriggerState, len(project.Triggers))
		for key, trigger := range project.Triggers {
			trigger.Content = append(json.RawMessage(nil), trigger.Content...)
			trigger.TicketIDs = append([]string(nil), trigger.TicketIDs...)
			triggers[key] = trigger
		}
		project.Triggers = triggers
		threads := make(map[string]ThreadState, len(project.Threads))
		for key, thread := range project.Threads {
			thread.TicketIDs = append([]string(nil), thread.TicketIDs...)
			threads[key] = thread
		}
		project.Threads = threads
		if project.Router != nil {
			router := *project.Router
			router.InitialInputReceipt = append(json.RawMessage(nil), project.Router.InitialInputReceipt...)
			project.Router = &router
		}
		if project.Tickets != nil {
			tickets := make(map[string]TicketState, len(project.Tickets))
			for ticketID, ticket := range project.Tickets {
				if ticket.Authorization != nil {
					grant := *ticket.Authorization
					grant.Resources = append([]string(nil), grant.Resources...)
					grant.Tools = append([]string(nil), grant.Tools...)
					grant.RuntimeRefs = append([]string(nil), grant.RuntimeRefs...)
					grant.Models = append([]string(nil), grant.Models...)
					grant.Efforts = append([]string(nil), grant.Efforts...)
					ticket.Authorization = &grant
				}
				if ticket.Execution != nil {
					execution := *ticket.Execution
					ticket.Execution = &execution
				}
				ticket.OutputSubscribers = append([]OutputTarget(nil), ticket.OutputSubscribers...)
				if ticket.Transition != nil {
					transition := *ticket.Transition
					ticket.Transition = &transition
				}
				if ticket.Reports != nil {
					ticket.Reports = append([]ReportState(nil), ticket.Reports...)
				}
				tickets[ticketID] = ticket
			}
			project.Tickets = tickets
		}
		if project.Workers != nil {
			workers := make(map[string]WorkerState, len(project.Workers))
			for key, worker := range project.Workers {
				worker.InitialInputReceipt = append(json.RawMessage(nil), worker.InitialInputReceipt...)
				workers[key] = worker
			}
			project.Workers = workers
		}
		if project.Assignments != nil {
			assignments := make(map[string]AssignmentState, len(project.Assignments))
			for assignmentID, assignment := range project.Assignments {
				assignments[assignmentID] = assignment
			}
			project.Assignments = assignments
		}
		if project.RouteBindings != nil {
			bindings := make(map[string]RouteBindingState, len(project.RouteBindings))
			for key, binding := range project.RouteBindings {
				bindings[key] = binding
			}
			project.RouteBindings = bindings
		}
		if project.Inbox != nil {
			inbox := make(map[string]InboxEventState, len(project.Inbox))
			for eventID, event := range project.Inbox {
				if event.Payload != nil {
					event.Payload = append(json.RawMessage(nil), event.Payload...)
				}
				inbox[eventID] = event
			}
			project.Inbox = inbox
		}
		if project.Deliveries != nil {
			deliveries := make(map[string]DeliveryBatchState, len(project.Deliveries))
			for batchID, batch := range project.Deliveries {
				batch.EventIDs = append([]string(nil), batch.EventIDs...)
				deliveries[batchID] = batch
			}
			project.Deliveries = deliveries
		}
		clone.Projects[id] = project
	}
	return clone
}

func normalizeProjectMaps(state *State) {
	for projectID, project := range state.Projects {
		normalizeProject(&project)
		state.Projects[projectID] = project
	}
}

func normalizeProject(project *ProjectState) {
	if project.Delegations == nil {
		project.Delegations = map[string]DelegationState{}
	}
	if project.Outputs == nil {
		project.Outputs = map[string]OutputRecord{}
	}
	if project.Consumers == nil {
		project.Consumers = map[string]OutputConsumer{}
	}
	if project.Triggers == nil {
		project.Triggers = map[string]TriggerState{}
	}
	if project.Threads == nil {
		project.Threads = map[string]ThreadState{}
	}
	if project.Tickets == nil {
		project.Tickets = map[string]TicketState{}
	}
	if project.Workers == nil {
		project.Workers = map[string]WorkerState{}
	}
	if project.Assignments == nil {
		project.Assignments = map[string]AssignmentState{}
	}
	if project.RouteBindings == nil {
		project.RouteBindings = map[string]RouteBindingState{}
	}
	if project.Inbox == nil {
		project.Inbox = map[string]InboxEventState{}
	}
	if project.Deliveries == nil {
		project.Deliveries = map[string]DeliveryBatchState{}
	}
}

func writeStateFile(path string, state State) (committed bool, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, operationError("unavailable", "state_unavailable", "state directory cannot be created")
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, operationError("unavailable", "state_unavailable", "state temporary file cannot be created")
	}
	tempName := temp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tempName)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return false, operationError("unavailable", "state_unavailable", "state file permissions cannot be set")
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(state); err != nil {
		_ = temp.Close()
		return false, operationError("unavailable", "state_unavailable", "state file cannot be encoded")
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return false, operationError("unavailable", "state_unavailable", "state file cannot be synchronized")
	}
	if err = temp.Close(); err != nil {
		return false, operationError("unavailable", "state_unavailable", "state file cannot be closed")
	}
	if err = os.Rename(tempName, path); err != nil {
		return false, operationError("unavailable", "state_unavailable", "state file cannot be replaced")
	}
	committed = true
	if err = syncDirectory(dir); err != nil {
		return true, operationError("unavailable", "state_unavailable", "state directory cannot be synchronized")
	}
	return true, nil
}

func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
