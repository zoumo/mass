package sa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

// RouterStatus reports the durable router reference and requested model values.
type RouterStatus struct {
	ProjectID           string          `json:"projectId"`
	ProjectName         string          `json:"projectName"`
	SessionID           string          `json:"sessionId"`
	Namespace           string          `json:"namespace"`
	Name                string          `json:"name"`
	RuntimeRef          string          `json:"runtimeRef"`
	State               string          `json:"state"`
	Model               string          `json:"model,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	InitialInputReceipt json.RawMessage `json:"initialInputReceipt,omitempty"`
}

// RouterBootstrapOptions selects the optional tool service used to inject a
// stable router MCP capability at Session creation or validation time.
type RouterBootstrapOptions struct {
	Tools *WorkerService
}

// RouterBootstrapOption customizes router Session provisioning.
type RouterBootstrapOption func(*RouterBootstrapOptions)

// WithRouterTools injects the stable mass-sa router MCP server and validates it
// whenever an existing router Session is adopted or resumed.
func WithRouterTools(service *WorkerService) RouterBootstrapOption {
	return func(options *RouterBootstrapOptions) {
		options.Tools = service
	}
}

// BootstrapRouters creates or explicitly resumes one router Session per project.
// It never recreates a known Session whose state is unknown or missing.
func BootstrapRouters(ctx context.Context, config Config, store *StateStore, options ...RouterBootstrapOption) (statuses []RouterStatus, err error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, operationError("invalid_argument", "invalid_state_store", "state store is required")
	}
	var bootstrapOptions RouterBootstrapOptions
	for _, option := range options {
		option(&bootstrapOptions)
	}
	if bootstrapOptions.Tools != nil && bootstrapOptions.Tools.toolEndpoint() == "" {
		return nil, operationError("failed_precondition", "tool_endpoint_missing", "tool HTTP endpoint must be configured before bootstrapping router MCP")
	}
	client, err := OpenMASS(ctx, config.MASS)
	if err != nil {
		return nil, err
	}

	projects := append([]ProjectConfig(nil), config.Projects...)
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	var projectErrors []error
	for _, project := range projects {
		if ctxErr := ctx.Err(); ctxErr != nil {
			projectErrors = append(projectErrors, ctxErr)
			break
		}
		status, ensureErr := ensureProjectRouter(ctx, client, store, project, bootstrapOptions.Tools)
		if ensureErr != nil {
			projectErrors = append(projectErrors, fmt.Errorf("project %s: %w", project.ID, ensureErr))
			continue
		}
		statuses = append(statuses, status)
	}
	closeErr := client.Close()
	if closeErr != nil {
		projectErrors = append(projectErrors, fmt.Errorf("close MASS connection: %w", closeErr))
	}
	return statuses, errors.Join(projectErrors...)
}

func ensureProjectRouter(ctx context.Context, client *MASSClient, store *StateStore, project ProjectConfig, tools *WorkerService) (RouterStatus, error) {
	snapshot := store.Snapshot()
	record := snapshot.Projects[project.ID].Router
	var mcpServers []MCPServer
	capabilityFile := ""
	capabilityDigestValue := ""
	var capabilityState *CapabilityState
	if tools != nil {
		grant, err := tools.EnsureRouterCapability(project.ID)
		if err != nil {
			return RouterStatus{}, err
		}
		capabilityFile = grant.State.File
		capabilityDigestValue = capabilityDigest(grant.Token)
		copied := grant.State
		capabilityState = &copied
		mcpServers = []MCPServer{massSAMCPServer(project.ID, "router", tools.mcpCommand, capabilityFile)}
	}
	desired := desiredRouterSpec(project, mcpServers...)

	var session Session
	expectedSessionID := ""
	createdSession := false
	if record != nil {
		if err := validateStoredRouter(project, *record, capabilityFile, capabilityDigestValue); err != nil {
			return RouterStatus{}, err
		}
		expectedSessionID = record.SessionID
		found, err := client.GetSession(ctx, SessionRef{SessionID: record.SessionID})
		if err != nil {
			if IsErrorCode(err, "not_found") {
				return RouterStatus{}, operationError("failed_precondition", "known_router_missing", "persisted router Session was not found; refusing to create a replacement")
			}
			return RouterStatus{}, err
		}
		session = found
		if len(session.InitialInputReceipt) == 0 && len(record.InitialInputReceipt) != 0 {
			session.InitialInputReceipt = append(json.RawMessage(nil), record.InitialInputReceipt...)
		}
	} else {
		found, err := client.GetSession(ctx, SessionRef{Namespace: RouterNamespace, Name: RouterName(project)})
		switch {
		case err == nil:
			session = found
		case IsErrorCode(err, "not_found"):
			created, createErr := client.CreateSession(ctx, CreateSessionInput{
				Metadata:     SessionMetadata{Namespace: RouterNamespace, Name: RouterName(project)},
				Spec:         desired,
				InitialInput: InitializationPrompt("router", project.ID),
			})
			if createErr != nil {
				return RouterStatus{}, createErr
			}
			session = created
			createdSession = true
			if err := saveRouter(store, project, session, capabilityState, capabilityDigestValue); err != nil {
				return RouterStatus{}, err
			}
		default:
			return RouterStatus{}, err
		}
	}

	if err := validateRouterSession(project, session, expectedSessionID, mcpServers...); err != nil {
		return RouterStatus{}, err
	}
	if createdSession {
		settled, settleErr := awaitCreatedSession(ctx, client, session)
		if settleErr != nil {
			if len(session.InitialInputReceipt) != 0 {
				_ = saveRouter(store, project, session, capabilityState, capabilityDigestValue)
			}
			return RouterStatus{}, settleErr
		}
		session = settled
	}

	switch session.Status.State {
	case "idle", "running", "requires_action":
	case "offline":
		resumed, err := client.ResumeSession(ctx, SessionRef{SessionID: session.Status.SessionID})
		if err != nil {
			return RouterStatus{}, err
		}
		if err := validateRouterSession(project, resumed, session.Status.SessionID, mcpServers...); err != nil {
			return RouterStatus{}, err
		}
		if resumed.Status.State == "offline" || resumed.Status.State == "unknown" {
			_ = saveRouter(store, project, resumed, capabilityState, capabilityDigestValue)
			return RouterStatus{}, operationError("failed_precondition", "router_resume_incomplete", "router Session did not become available after explicit resume")
		}
		session = resumed
	case "unknown":
		saveErr := saveRouter(store, project, session, capabilityState, capabilityDigestValue)
		return RouterStatus{}, errors.Join(
			operationError("failed_precondition", "router_session_unknown", "router Session state is unknown; refusing automatic recreation"),
			saveErr,
		)
	case "opening", "closing":
		saveErr := saveRouter(store, project, session, capabilityState, capabilityDigestValue)
		return RouterStatus{}, errors.Join(
			operationError("failed_precondition", "router_session_transitioning", "router Session is in a transitional state"),
			saveErr,
		)
	default:
		saveErr := saveRouter(store, project, session, capabilityState, capabilityDigestValue)
		return RouterStatus{}, errors.Join(
			operationError("failed_precondition", "router_session_invalid_state", "router Session has an unsupported state: "+session.Status.State),
			saveErr,
		)
	}

	if err := saveRouter(store, project, session, capabilityState, capabilityDigestValue); err != nil {
		return RouterStatus{}, err
	}
	return statusFromSession(project, session), nil
}

func desiredRouterSpec(project ProjectConfig, mcpServers ...MCPServer) SessionSpec {
	return SessionSpec{
		RuntimeRef:   project.RuntimeRef,
		Cwd:          project.CWD,
		SystemPrompt: RouterSystemPrompt(project),
		Config: &InitialSessionConfig{
			Model:  project.Router.Model,
			Effort: project.Router.Effort,
		},
		MCPServers: append([]MCPServer(nil), mcpServers...),
	}
}

func validateStoredRouter(project ProjectConfig, router RouterState, capabilityFile, capabilityDigestValue string) error {
	if router.SessionID == "" ||
		router.Namespace != RouterNamespace ||
		router.Name != RouterName(project) ||
		router.RuntimeRef != project.RuntimeRef {
		return operationError("failed_precondition", "router_state_mismatch", "persisted router reference does not match project configuration")
	}
	if capabilityFile != "" && (router.CapabilityFile != capabilityFile || router.CapabilityDigest != capabilityDigestValue) {
		return operationError("failed_precondition", "router_capability_mismatch", "persisted router capability does not match the stable project capability")
	}
	return nil
}

func validateRouterSession(project ProjectConfig, session Session, expectedSessionID string, mcpServers ...MCPServer) error {
	if session.Status.SessionID == "" {
		return operationError("failed_precondition", "router_identity_missing", "router Session has no native session ID")
	}
	if expectedSessionID != "" && session.Status.SessionID != expectedSessionID {
		return operationError("failed_precondition", "router_identity_mismatch", "router Session identity changed during lookup or resume")
	}
	if session.Metadata.Namespace != RouterNamespace || session.Metadata.Name != RouterName(project) {
		return operationError("failed_precondition", "router_identity_mismatch", "router Session name does not match project configuration")
	}
	if session.Spec.RuntimeRef != project.RuntimeRef {
		return operationError("failed_precondition", "router_runtime_mismatch", "router Session Runtime does not match project configuration")
	}
	if session.Spec.Cwd != project.CWD {
		return operationError("failed_precondition", "router_cwd_mismatch", "router Session working directory does not match project configuration")
	}
	if session.Spec.SystemPrompt != RouterSystemPrompt(project) {
		return operationError("failed_precondition", "router_prompt_mismatch", "router Session prompt does not match current project memory and role")
	}
	actualModel, actualEffort := sessionConfig(session)
	if actualModel != project.Router.Model || actualEffort != project.Router.Effort {
		return operationError("failed_precondition", "router_config_mismatch", "router Session model or effort does not match project configuration")
	}
	if !reflect.DeepEqual(session.Spec.MCPServers, mcpServers) {
		return operationError("failed_precondition", "router_mcp_mismatch", "router Session MCP servers do not match the stable mass-sa capability")
	}
	return nil
}

func sessionConfig(session Session) (string, string) {
	if session.Spec.Config == nil {
		return "", ""
	}
	return session.Spec.Config.Model, session.Spec.Config.Effort
}

func saveRouter(store *StateStore, project ProjectConfig, session Session, capability *CapabilityState, capabilityDigestValue string) error {
	model, effort := sessionConfig(session)
	record := RouterState{
		SessionRef: SessionRef{
			SessionID: session.Status.SessionID,
			Namespace: session.Metadata.Namespace,
			Name:      session.Metadata.Name,
		},
		RuntimeRef:          session.Spec.RuntimeRef,
		Model:               model,
		Effort:              effort,
		ObservedState:       session.Status.State,
		InitialInputReceipt: append(json.RawMessage(nil), session.InitialInputReceipt...),
		UpdatedAt:           time.Now().UTC(),
	}
	if capability != nil {
		record.CapabilityFile = capability.File
		record.CapabilityDigest = capabilityDigestValue
	}
	return store.SetRouterAndCapability(project.ID, record, capability, capabilityDigestValue)
}

func statusFromSession(project ProjectConfig, session Session) RouterStatus {
	model, effort := sessionConfig(session)
	return RouterStatus{
		ProjectID:           project.ID,
		ProjectName:         project.Name,
		SessionID:           session.Status.SessionID,
		Namespace:           session.Metadata.Namespace,
		Name:                session.Metadata.Name,
		RuntimeRef:          session.Spec.RuntimeRef,
		State:               session.Status.State,
		Model:               model,
		Effort:              effort,
		InitialInputReceipt: append(json.RawMessage(nil), session.InitialInputReceipt...),
	}
}
