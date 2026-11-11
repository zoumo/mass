// Package claude orchestrates the Claude SDK provider behind MASS's Driver
// lifecycle. Live event normalization is owned by agent/claude.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/zoumo/agent-sdk-go/agent"
	agentclaude "github.com/zoumo/agent-sdk-go/agent/claude"
	nativeclaude "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
	"github.com/zoumo/mass/mass-go/pkg/runtime/recovery"
)

type options = runtime.ProcessConfig

func parse(c runtime.Config) (options, error) {
	o, err := runtime.ParseProcessConfig(c)
	if err != nil {
		return o, err
	}
	if len(o.Args) > 0 {
		return o, errors.New("claude SDK does not support additional process args; use an explicit launcher")
	}
	if configDir := o.Env["CLAUDE_CONFIG_DIR"]; configDir != "" && !filepath.IsAbs(configDir) {
		return o, errors.New("explicit absolute Claude config directory required")
	}
	return o, nil
}

type Factory struct{ RecoveryDir string }

func (Factory) Validate(c runtime.Config) error { _, err := parse(c); return err }

func (f Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	o, err := parse(c)
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	provider := agentclaude.New(agentclaude.Config{
		CLIPath:   o.Executable,
		Env:       o.Environment("claude"),
		ConfigDir: o.Env["CLAUDE_CONFIG_DIR"],
	})
	return &Driver{
		provider: provider,
		vault:    recovery.Vault{Dir: f.RecoveryDir, Kind: "claude", Owner: name, Executable: o.Executable},
		options:  o,
		pipe:     events.NewEventPipe(128),
		sessions: map[string]*session{},
		life:     life,
		cancel:   cancel,
	}, nil
}

type session struct {
	command   sync.Mutex
	closeOnce sync.Once
	closeErr  error
	native    agent.Session
	relay     *agentrelay.Relay
	address   runtime.Address
	source    events.Source

	mu           sync.Mutex
	closing      bool
	err          error
	cancelSet    bool
	cancelResult runtime.CancelReceipt
	cancelErr    error
}

func (s *session) clearCancelLocked() {
	s.cancelSet = false
	s.cancelResult = runtime.CancelReceipt{}
	s.cancelErr = nil
}

type Driver struct {
	opening int
	loading int
	vault   recovery.Vault

	mu       sync.Mutex
	op       sync.Mutex
	options  options
	pipe     *events.EventPipe
	taken    bool
	started  bool
	stopping bool
	stopped  bool
	stopErr  error
	provider agent.Provider
	sessions map[string]*session
	loads    map[string]*nativeLoad
	life     context.Context
	cancel   context.CancelFunc
}

var supportedSessionFeatures = runtime.FeatureSupport{AgentQuestions: true}

func (d *Driver) Events() (events.Stream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.taken {
		return nil, errors.New("Driver events already acquired")
	}
	d.taken = true
	return d.pipe, nil
}

func (d *Driver) Start(ctx context.Context) (runtime.Capabilities, error) {
	d.op.Lock()
	defer d.op.Unlock()
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		return nil, errors.New("driver stopping")
	}
	if d.stopped {
		d.mu.Unlock()
		return nil, errors.New("driver stopped")
	}
	d.mu.Unlock()
	capabilities, err := d.provider.Start(ctx)
	if err != nil {
		return nil, mapClaudeError(err)
	}
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	return agentrelay.MapCapabilities(capabilities), nil
}

func (d *Driver) OpenSession(ctx context.Context, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, "", address, spec, nil)
}

func (*Driver) ValidateInitialInput(input runtime.Input) error {
	return validateClaudeInput(input)
}

func (d *Driver) OpenSessionWithInput(ctx context.Context, address runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	return d.open(ctx, "", address, spec, &input)
}

func (d *Driver) ResumeSession(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, id, address, spec, nil)
}

func (d *Driver) ResumeSessionWithInput(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	return d.open(ctx, id, address, spec, &input)
}

func validateClaudeInput(input runtime.Input) error {
	if err := runtime.ValidateTextInput(input); err != nil {
		return err
	}
	if len(input.Content) != 1 {
		return &runtime.OperationError{
			Code:    "unsupported",
			Reason:  "multiple_content_blocks_unsupported",
			Message: "Claude text input supports exactly one content block",
		}
	}
	return nil
}

func (d *Driver) open(ctx context.Context, resume string, address runtime.Address, spec runtime.SessionSpec, initial *runtime.Input) (runtime.DriverSession, error) {
	d.mu.Lock()
	if d.stopped || d.stopping || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		return runtime.DriverSession{}, noResources("driver unavailable or session capacity exhausted")
	}
	d.opening++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.opening--
		d.mu.Unlock()
	}()

	if err := (agent.OpenOptions{Cwd: spec.Cwd, AdditionalSystemPrompt: spec.SystemPrompt}).Validate(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, agentrelay.MapError(err))
	}
	if err := d.vault.Check(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	if !filepath.IsAbs(spec.Cwd) {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, runtime.InvalidArgument("absolute cwd required"))
	}
	if len(spec.AdditionalDirs) > 0 {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, runtime.UnsupportedOption(
			"additional_dirs_unsupported",
			"Claude runtime does not support additional directories",
		))
	}
	if resume != "" {
		material, err := d.vault.Load(resume, address, spec)
		if err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		var saved struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(material.Context, &saved); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if saved.ID != resume || !runtime.EqualNativePath(saved.Cwd, spec.Cwd) {
			return runtime.DriverSession{}, noResources("invalid Claude recovery context")
		}
	}

	servers, err := claudeMCPServers(spec.MCPServers, d.options)
	if err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	effective := runtime.ResolveSessionFeatures(spec.Features, supportedSessionFeatures)
	openOptions := sessionOpenOptions(spec, effective, servers)

	d.mu.Lock()
	started := d.started
	stopped := d.stopped || d.stopping
	d.mu.Unlock()
	if !started || stopped {
		return runtime.DriverSession{}, noResources("driver not running")
	}

	var native agent.Session
	if resume == "" {
		native, err = d.provider.Open(ctx, openOptions)
	} else {
		native, err = d.provider.Resume(ctx, agent.ResumeOptions{SessionID: resume, OpenOptions: openOptions})
	}
	if err != nil {
		cause := mapClaudeError(err)
		if failedNativeCleanup(err) {
			return runtime.DriverSession{}, cause
		}
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, cause)
	}
	id := native.ID()

	s := &session{
		native:  native,
		address: address,
		source:  events.NewSource(),
	}
	d.mu.Lock()
	if d.sessions[id] != nil {
		d.mu.Unlock()
		if cleanup := native.Close(context.Background()); cleanup != nil {
			return runtime.DriverSession{}, mapClaudeError(cleanup)
		}
		return runtime.DriverSession{}, noResources("duplicate native identity")
	}
	d.sessions[id] = s
	d.mu.Unlock()

	remove := func() {
		d.mu.Lock()
		if d.sessions[id] == s {
			delete(d.sessions, id)
		}
		d.mu.Unlock()
	}
	fail := func(cause error) (runtime.DriverSession, error) {
		if cleanup := s.arcClose(); cleanup != nil {
			return runtime.DriverSession{SessionID: id, Source: s.source}, errors.Join(cause, cleanup)
		}
		remove()
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, cause)
	}
	if err := d.startRelay(s); err != nil {
		return fail(err)
	}

	if resume == "" {
		if err := d.vault.Save(id, address, spec, map[string]string{"id": id, "cwd": spec.Cwd}); err != nil {
			return fail(err)
		}
	}

	var receipt *runtime.InputReceipt
	if initial != nil {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		// The session is established; retain the SDK operation's receipt even if
		// the caller abandons its wait.
		result, inputErr := native.StartOrSteer(context.WithoutCancel(ctx), *initial)
		if inputErr != nil {
			cause := mapClaudeError(inputErr)
			if resume != "" && inputMayBeUnknown(inputErr) {
				cause = errors.Join(&runtime.OperationError{
					Code:    "outcome_unknown",
					Reason:  "resume_input_unverified",
					Message: "New input dispatch was not verified; do not resend",
				}, cause)
			}
			return fail(cause)
		}
		if result.Result == agent.InputNotApplied {
			return fail(errors.Join(
				runtime.ErrFailedPrecondition,
				runtime.ErrWorkUnsettled,
				errors.New("claude input was not applied"),
			))
		}
		mapped := agentrelay.MapInputReceipt(result)
		receipt = &mapped
	}

	return runtime.DriverSession{
		Observation:         s.relay,
		SessionID:           id,
		Capabilities:        agentrelay.MapCapabilities(native.Capabilities()),
		EffectiveFeatures:   agentrelay.MapEffectiveFeatures(native.EffectiveFeatures()),
		Source:              s.source,
		InitialInputReceipt: receipt,
	}, nil
}

func sessionOpenOptions(spec runtime.SessionSpec, features runtime.SessionFeatures, servers []agent.MCPServer) agent.OpenOptions {
	return agent.OpenOptions{
		Cwd:                    spec.Cwd,
		AdditionalSystemPrompt: spec.SystemPrompt,
		Features:               agent.Features{AgentQuestions: features.AgentQuestions},
		MCPServers:             servers,
	}
}

func (s *session) closeNative(ctx context.Context) error {
	s.closeOnce.Do(func() { s.closeErr = mapClaudeError(s.native.Close(context.WithoutCancel(ctx))) })
	return s.closeErr
}

func (s *session) arcClose() error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	closeErr := s.closeNative(context.Background())
	if closeErr != nil {
		return closeErr
	}
	if s.relay != nil {
		<-s.relay.Done()
	}
	return closeErr
}

func (d *Driver) startRelay(s *session) error {
	relay, err := agentrelay.Start(d.life, agentrelay.Config{
		Session: s.native,
		Address: s.address,
		Source:  s.source,
		ExpectedEOF: func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.closing
		},
		Sink: func(_ context.Context, event events.SessionEvent) error {
			s.mu.Lock()
			if state, ok := event.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle {
				s.clearCancelLocked()
			}
			s.mu.Unlock()
			return d.pipe.Send(d.life, event)
		},
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	s.relay = relay
	d.mu.Unlock()
	go func() {
		<-relay.Done()
		s.mu.Lock()
		closing := s.closing
		if !closing {
			s.err = errors.Join(errors.New("claude event observation ended"), s.native.Err(), relay.Err())
		}
		s.mu.Unlock()
	}()
	return nil
}

func (d *Driver) get(id string) (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil || d.stopped || d.stopping {
		return nil, runtime.FailedPrecondition("session unavailable")
	}
	return s, nil
}

func (d *Driver) SendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	if err := validateClaudeInput(input); err != nil {
		return runtime.InputReceipt{}, err
	}
	s, err := d.get(id)
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	s.command.Lock()
	defer s.command.Unlock()
	s.mu.Lock()
	if s.closing || s.err != nil {
		s.mu.Unlock()
		return runtime.InputReceipt{}, runtime.FailedPrecondition("session unavailable")
	}
	s.mu.Unlock()

	result, err := s.native.StartOrSteer(ctx, input)
	if err != nil {
		return runtime.InputReceipt{}, mapClaudeError(err)
	}
	if result.Result == agent.InputAppliedNewWork {
		s.mu.Lock()
		s.clearCancelLocked()
		s.mu.Unlock()
	}
	return agentrelay.MapInputReceipt(result), nil
}

func (d *Driver) AnswerInteraction(ctx context.Context, id, ref string, answer any) (runtime.Acceptance, error) {
	typed, err := claudeInteractionAnswer(answer)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	s, err := d.get(id)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	s.mu.Lock()
	if s.closing || s.err != nil {
		s.mu.Unlock()
		return runtime.Acceptance{}, runtime.FailedPrecondition("session unavailable")
	}
	s.mu.Unlock()
	receipt, err := s.native.Answer(ctx, agent.InteractionRef(ref), typed)
	if err != nil {
		return runtime.Acceptance{}, mapClaudeError(err)
	}
	return agentrelay.MapAcceptance(receipt.Acceptance), nil
}

func claudeInteractionAnswer(value any) (agent.Answer, error) {
	answer, ok := value.(map[string]any)
	if !ok || answer == nil {
		return nil, runtime.InvalidArgument("interaction requires an object answer")
	}
	if action, form := answer["action"]; form {
		switch action {
		case "accept":
			if len(answer) != 2 {
				return nil, runtime.InvalidArgument("expected explicit form accept/content")
			}
			content, valid := answer["content"].(map[string]any)
			if !valid || content["answers"] == nil {
				return nil, runtime.InvalidArgument("expected native question answers")
			}
			if err := validateClaudeQuestionAnswers(content["answers"]); err != nil {
				return nil, err
			}
			return agent.FormAnswer{Outcome: agent.FormAccept, Content: content}, nil
		case "decline":
			if len(answer) != 1 {
				return nil, runtime.InvalidArgument("declined form must not contain content")
			}
			return agent.FormAnswer{Outcome: agent.FormDecline}, nil
		case "cancel":
			if len(answer) != 1 {
				return nil, runtime.InvalidArgument("canceled form must not contain content")
			}
			return agent.FormAnswer{Outcome: agent.FormCancel}, nil
		default:
			return nil, runtime.InvalidArgument("invalid form answer action")
		}
	}

	outcome, ok := answer["outcome"].(string)
	if !ok {
		return nil, runtime.InvalidArgument("invalid permission answer outcome")
	}
	switch outcome {
	case "selected":
		if len(answer) != 2 {
			return nil, runtime.InvalidArgument("selected permission answer requires only an optionId")
		}
		option, ok := answer["optionId"].(string)
		if !ok || option == "" {
			return nil, runtime.InvalidArgument("selected permission answer requires an optionId")
		}
		switch option {
		case "allow-once", "reject-once":
		default:
			return nil, runtime.InvalidArgument("invalid permission option")
		}
		return agent.PermissionAnswer{Outcome: agent.PermissionSelected, OptionID: option}, nil
	case string(agent.PermissionCancelled):
		if len(answer) != 1 {
			return nil, runtime.InvalidArgument("canceled permission answer must not select an option")
		}
		return agent.PermissionAnswer{Outcome: agent.PermissionCancelled}, nil
	default:
		return nil, runtime.InvalidArgument("invalid permission answer outcome")
	}
}

func validateClaudeQuestionAnswers(value any) error {
	switch answers := value.(type) {
	case map[string]any:
		for question, answer := range answers {
			if question == "" {
				return runtime.InvalidArgument("question answers require native question text")
			}
			switch value := answer.(type) {
			case string:
			case []any:
				for _, item := range value {
					if _, ok := item.(string); !ok {
						return runtime.InvalidArgument("question answers must be strings or string arrays")
					}
				}
			default:
				return runtime.InvalidArgument("question answers must be strings or string arrays")
			}
		}
		return nil
	case map[string]string:
		return nil
	default:
		return runtime.InvalidArgument("expected answers keyed by full native question text")
	}
}

func (d *Driver) CloseSession(ctx context.Context, id string) error {
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	closeErr := s.closeNative(ctx)
	if closeErr != nil {
		return closeErr
	}
	if s.relay != nil {
		select {
		case <-s.relay.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := d.pipe.Flush(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	if d.sessions[id] == s {
		delete(d.sessions, id)
	}
	d.mu.Unlock()
	return nil
}

func (d *Driver) Stop(ctx context.Context) error {
	d.op.Lock()
	defer d.op.Unlock()
	d.mu.Lock()
	if d.stopped {
		err := d.stopErr
		d.mu.Unlock()
		return err
	}
	if d.stopping {
		d.mu.Unlock()
		return errors.New("driver stopping")
	}
	if len(d.sessions) != 0 || d.opening != 0 || d.loading != 0 {
		d.mu.Unlock()
		return errors.New("close sessions before stopping")
	}
	d.stopping = true
	d.mu.Unlock()
	err := d.provider.Stop(context.WithoutCancel(ctx))
	if agentrelay.StopRejected(err) {
		d.mu.Lock()
		d.stopping = false
		d.mu.Unlock()
		return mapClaudeError(err)
	}
	stopErr := mapClaudeError(err)
	d.cancel()
	d.mu.Lock()
	d.stopping = false
	d.stopped = true
	d.stopErr = stopErr
	d.mu.Unlock()
	d.pipe.End(nil)
	return stopErr
}

func (d *Driver) Cancel(ctx context.Context, id string) (runtime.CancelReceipt, error) {
	s, err := d.get(id)
	if err != nil {
		return runtime.CancelReceipt{}, err
	}
	s.command.Lock()
	defer s.command.Unlock()
	if err := ctx.Err(); err != nil {
		return runtime.CancelReceipt{}, fmt.Errorf("not_sent: interrupt: %w", err)
	}
	s.mu.Lock()
	if s.cancelSet {
		receipt, err := s.cancelResult, s.cancelErr
		s.mu.Unlock()
		return receipt, err
	}
	s.mu.Unlock()

	receipt, err := s.native.Interrupt(ctx, agent.InterruptOptions{})
	if err != nil {
		mapped := mapClaudeError(err)
		switch {
		case isNotSent(err):
			return runtime.CancelReceipt{}, fmt.Errorf("not_sent: interrupt: %w", mapped)
		case agent.CodeOf(err) == agent.ErrorNativeRejected:
			mapped = fmt.Errorf("native_rejected: interrupt: %w", mapped)
		default:
			mapped = fmt.Errorf("outcome_unknown: interrupt: %w", mapped)
		}
		s.mu.Lock()
		s.cancelSet = true
		s.cancelErr = mapped
		s.mu.Unlock()
		return runtime.CancelReceipt{}, mapped
	}
	mapped := agentrelay.MapCancelReceipt(receipt)
	if receipt.Result == agent.InterruptAcknowledged {
		s.mu.Lock()
		s.cancelSet = true
		s.cancelResult = mapped
		s.mu.Unlock()
	}
	return mapped, nil
}

func isNotSent(err error) bool {
	var dispatch *nativeclaude.DispatchError
	if errors.As(err, &dispatch) {
		return dispatch.State == nativeclaude.NotSent
	}
	switch agent.CodeOf(err) {
	case agent.ErrorInvalidArgument, agent.ErrorUnsupported, agent.ErrorClosed, agent.ErrorFailedPrecondition:
		return true
	default:
		return false
	}
}

func inputMayBeUnknown(err error) bool {
	if err == nil {
		return false
	}
	var dispatch *nativeclaude.DispatchError
	if errors.As(err, &dispatch) {
		return dispatch.State != nativeclaude.NotSent
	}
	switch agent.CodeOf(err) {
	case agent.ErrorOutcomeUnknown, agent.ErrorUnavailable:
		return true
	default:
		return false
	}
}

func mapClaudeError(err error) error {
	if err == nil {
		return nil
	}
	var public *runtime.OperationError
	if errors.As(err, &public) {
		return err
	}
	var native *agent.Error
	if errors.As(err, &native) && native != nil {
		return agentrelay.MapError(err)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return errors.Join(&runtime.OperationError{
			Code:    "unavailable",
			Reason:  "operation_canceled",
			Message: "Agent operation was canceled",
		}, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.Join(&runtime.OperationError{
			Code:    "unavailable",
			Reason:  "operation_deadline_exceeded",
			Message: "Agent operation exceeded its deadline",
		}, context.DeadlineExceeded)
	default:
		return &runtime.OperationError{
			Code:    "unavailable",
			Reason:  "agent_operation",
			Message: "Agent operation failed",
		}
	}
}

//nolint:errorlint // Inspect each error-tree node so a matching branch cannot hide another failed close.
func failedNativeCleanup(err error) bool {
	if errors.Is(err, nativeclaude.ErrShutdownTimeout) {
		return true
	}
	if native, ok := err.(*agent.Error); ok && strings.HasPrefix(native.Reason, "close_") {
		// SDK Close waits for cleaned before returning the session terminal.
		// Its business outcome may still be unknown; only shutdown timeout
		// indicates unfinished cleanup. Separate close failures remain checked.
		terminal, ok := native.Cause.(*nativeclaude.Error)
		return !ok || terminal.Op != "session"
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return slices.ContainsFunc(joined.Unwrap(), failedNativeCleanup)
	}
	if cause := errors.Unwrap(err); cause != nil {
		return failedNativeCleanup(cause)
	}
	return false
}

func noResources(message string) error {
	return errors.Join(runtime.ErrResourcesReleased, errors.New(message))
}

// SessionObservation exposes the relay for a binding created by history load.
func (d *Driver) SessionObservation(id string) runtime.SessionObservation {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.sessions[id]; s != nil && s.relay != nil {
		return s.relay
	}
	return nil
}
