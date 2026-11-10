// Package codex adapts the Codex agent provider to MASS runtime ownership.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/zoumo/agent-sdk-go/agent"
	agentcodex "github.com/zoumo/agent-sdk-go/agent/codex"
	sdk "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
	"github.com/zoumo/mass/mass-go/pkg/runtime/recovery"
)

type options = runtime.ProcessConfig

func parse(c runtime.Config) (options, error) {
	return runtime.ParseProcessConfig(c)
}

// codexReadClient is the read-only subset MASS uses for recovery and history.
// The concrete SDK client remains owned by the provider.
type codexReadClient interface {
	ServerInfo() ct.InitializeResponse
	ReadThread(context.Context, ct.ThreadReadParams) (ct.ThreadReadResponse, error)
	ListThreadTurns(context.Context, ct.ThreadTurnsListParams) (ct.ThreadTurnsListResponse, error)
	ListThreadItems(context.Context, ct.ThreadItemsListParams) (ct.ThreadItemsListResponse, error)
}

type Factory struct{ RecoveryDir string }

func (Factory) Validate(c runtime.Config) error { _, err := parse(c); return err }

func (f Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	o, err := parse(c)
	if err != nil {
		return nil, err
	}
	args := o.Args
	if args == nil {
		args = []string{"app-server"}
	}
	provider := agentcodex.New(agentcodex.Config{Stdio: sdk.StdioOptions{
		Executable: o.Executable,
		Args:       args,
		Env:        o.Environment("codex"),
		Initialize: ct.InitializeParams{ClientInfo: ct.ClientInfo{Name: "mass", Version: "2"}},
	}})
	return newDriver(
		recovery.Vault{Dir: f.RecoveryDir, Kind: "codex", Owner: name, Executable: o.Executable},
		o,
		provider,
		func() (codexReadClient, error) {
			client, err := provider.NativeClient()
			if err != nil {
				return nil, err
			}
			return client, nil
		},
	), nil
}

func newDriver(vault recovery.Vault, options options, provider agent.Provider, nativeClient func() (codexReadClient, error)) *Driver {
	return &Driver{
		vault:        vault,
		options:      options,
		provider:     provider,
		nativeClient: nativeClient,
		pipe:         events.NewEventPipe(128),
		sessions:     map[string]*session{},
	}
}

type session struct {
	native    agent.Session
	relay     *agentrelay.Relay
	address   runtime.Address
	source    events.Source
	closing   atomic.Bool
	closeMu   sync.Mutex
	cancelOp  sync.Mutex
	controlMu sync.Mutex

	activeWork    string
	lastFinished  string
	cancelWork    string
	cancelReceipt runtime.CancelReceipt
	cancelErr     error
}

func (s *session) ordinaryAvailable() bool {
	return s.observationError() == nil
}

func (s *session) observationError() error {
	if s == nil || s.relay == nil {
		return runtime.FailedPrecondition("session observation unavailable")
	}
	if err := s.relay.Err(); err != nil {
		return err
	}
	select {
	case <-s.relay.Done():
		return runtime.FailedPrecondition("session observation ended")
	default:
		return nil
	}
}

type Driver struct {
	opening       int
	loads         map[string]*nativeLoad
	vault         recovery.Vault
	mu            sync.Mutex
	op            sync.Mutex
	options       options
	pipe          *events.EventPipe
	taken         bool
	provider      agent.Provider
	nativeClient  func() (codexReadClient, error)
	providerReady bool
	providerCaps  runtime.Capabilities
	sessions      map[string]*session
	stopped       bool
	stopping      bool
	stopErr       error
	life          context.Context
	cancel        context.CancelFunc
}

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
	if d.providerReady {
		capabilities := copyCapabilities(d.providerCaps)
		d.mu.Unlock()
		return capabilities, nil
	}
	if d.stopped {
		d.mu.Unlock()
		return nil, errors.New("driver stopped")
	}
	provider := d.provider
	d.mu.Unlock()
	if provider == nil {
		return nil, errors.New("provider unavailable")
	}

	capabilities, err := provider.Start(ctx)
	if err != nil {
		return nil, agentrelay.MapError(err)
	}
	life, cancel := context.WithCancel(context.Background())

	d.mu.Lock()
	d.providerReady = true
	d.providerCaps = agentrelay.MapCapabilities(capabilities)
	d.life = life
	d.cancel = cancel
	mapped := copyCapabilities(d.providerCaps)
	d.mu.Unlock()
	return mapped, nil
}

func (d *Driver) OpenSession(ctx context.Context, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, "", address, spec)
}

func (*Driver) ValidateInitialInput(input runtime.Input) error {
	return runtime.ValidateTextInput(input)
}

func (d *Driver) OpenSessionWithInput(ctx context.Context, address runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	native, err := d.OpenSession(ctx, address, spec)
	if err != nil {
		return native, err
	}
	if err := ctx.Err(); err != nil {
		return native, err
	}
	receipt, err := d.sendPrompt(context.WithoutCancel(ctx), native.SessionID, input)
	if err != nil {
		return native, err
	}
	native.InitialInputReceipt = &receipt
	return native, nil
}

func (d *Driver) ResumeSession(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, id, address, spec)
}

func (d *Driver) ResumeSessionWithInput(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	native, err := d.ResumeSession(ctx, id, address, spec)
	if err != nil {
		return native, err
	}
	if err := ctx.Err(); err != nil {
		return native, err
	}
	receipt, err := d.sendPrompt(context.WithoutCancel(ctx), native.SessionID, input)
	if err != nil {
		return native, err
	}
	native.InitialInputReceipt = &receipt
	return native, nil
}

func (d *Driver) open(ctx context.Context, resume string, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	if d.stopped || d.stopping || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		return runtime.DriverSession{}, noResources("driver unavailable or session capacity exhausted")
	}
	d.opening++
	provider := d.provider
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.opening--
		d.mu.Unlock()
	}()

	if err := d.vault.Check(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	if !filepath.IsAbs(spec.Cwd) {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, runtime.InvalidArgument("absolute cwd required"))
	}
	if spec.SystemPrompt != "" {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, runtime.UnsupportedOption("system_prompt_unsupported", "System prompts are not supported by this Codex runtime"))
	}
	if len(spec.AdditionalDirs) > 0 {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, runtime.UnsupportedOption("additional_dirs_unsupported", "Additional directories are not supported by this Codex runtime"))
	}
	if err := validateCodexSessionMCPIsolation(spec.MCPServers); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	reader, err := d.reader()
	if err != nil {
		return runtime.DriverSession{}, err
	}

	var saved ct.Thread
	if resume != "" {
		material, err := d.vault.Load(resume, address, spec)
		if err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if err := json.Unmarshal(material.Context, &saved); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if err := validateRecoveryMaterial(saved, resume, spec.Cwd); err != nil {
			return runtime.DriverSession{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	openOptions := agent.OpenOptions{
		Cwd:      spec.Cwd,
		Features: agentFeatures(spec.Features),
	}
	dispatchCtx := context.WithoutCancel(ctx)
	var native agent.Session
	if resume != "" {
		native, err = provider.Resume(dispatchCtx, agent.ResumeOptions{SessionID: resume, OpenOptions: openOptions})
	} else {
		native, err = provider.Open(dispatchCtx, openOptions)
	}
	if err != nil {
		return runtime.DriverSession{}, agentrelay.MapError(err)
	}
	if native == nil {
		return runtime.DriverSession{}, outcomeUnknown("native_identity_unverified", "Provider returned no native session", nil)
	}
	keep := false
	defer func() {
		if !keep {
			_ = native.Close(context.Background())
		}
	}()

	id := native.ID()
	if id == "" || (resume != "" && id != resume) {
		return runtime.DriverSession{}, outcomeUnknown("native_identity_unverified", "Native session identity was not verified", nil)
	}
	read, err := reader.ReadThread(dispatchCtx, ct.ThreadReadParams{ThreadID: id, IncludeTurns: ct.Some(false)})
	if err != nil {
		return runtime.DriverSession{}, outcomeUnknown("native_identity_unverified", "Native session identity could not be read; do not retry by identity", err)
	}
	if err := validateOpenedThread(read.Thread, id, spec.Cwd); err != nil {
		return runtime.DriverSession{}, outcomeUnknown("native_identity_unverified", "Native session identity or context did not match", err)
	}
	if resume != "" {
		if !sameRecoveryIdentity(saved, read.Thread) {
			return runtime.DriverSession{}, errors.New("native recovery ownership mismatch")
		}
	} else if err := d.vault.Save(id, address, spec, read.Thread); err != nil {
		return runtime.DriverSession{}, outcomeUnknown("recovery_material_unverified", "Recovery material could not be saved", err)
	}

	s, err := d.bindSession(native, address, events.NewSource())
	if err != nil {
		return runtime.DriverSession{}, err
	}
	keep = true
	return runtime.DriverSession{
		SessionID:         id,
		Capabilities:      agentrelay.MapCapabilities(native.Capabilities()),
		EffectiveFeatures: agentrelay.MapEffectiveFeatures(native.EffectiveFeatures()),
		Source:            s.source,
	}, nil
}

func (d *Driver) bindSession(native agent.Session, address runtime.Address, source events.Source) (*session, error) {
	if native == nil || native.ID() == "" {
		return nil, errors.New("bound agent session has no native identity")
	}
	d.mu.Lock()
	life := d.life
	unavailable := d.stopped || d.stopping || life == nil
	d.mu.Unlock()
	if unavailable {
		return nil, noResources("driver unavailable")
	}
	s := &session{native: native, address: address, source: source}
	relay, err := agentrelay.Start(life, agentrelay.Config{
		Session: native,
		Address: address,
		Source:  source,
		ExpectedEOF: func() bool {
			return s.closing.Load()
		},
		ObservationFailure: func(event agent.Event) error {
			problem, ok := event.Payload.(agent.SessionErrorPayload)
			if !ok || problem.Reason != "unsupported_native_item" {
				return nil
			}
			return &runtime.OperationError{
				Code:    string(agent.ErrorUnavailable),
				Reason:  problem.Reason,
				Message: problem.Message,
			}
		},
		Sink: func(ctx context.Context, event events.SessionEvent) error {
			if work := codexTurnID(event); work != "" {
				s.observeWork(event, work)
				remapped, err := events.NewEvent(event.EventBase, event.Payload(), event.Source().WithWork(work))
				if err != nil {
					return err
				}
				event = remapped
			}
			return d.pipe.Send(ctx, event)
		},
	})
	if err != nil {
		return nil, err
	}
	s.relay = relay

	d.mu.Lock()
	if d.stopped || d.stopping || d.sessions[native.ID()] != nil {
		d.mu.Unlock()
		relay.Close()
		return nil, errors.New("duplicate native identity")
	}
	d.sessions[native.ID()] = s
	d.mu.Unlock()
	return s, nil
}

func (d *Driver) SendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	if err := runtime.ValidateTextInput(input); err != nil {
		return runtime.InputReceipt{}, err
	}
	return d.sendPrompt(ctx, id, input)
}

func (d *Driver) sendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	s, err := d.ordinarySession(id)
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	receipt, err := s.native.StartOrSteer(ctx, input)
	if err != nil {
		return runtime.InputReceipt{}, agentrelay.MapError(err)
	}
	mapped := agentrelay.MapInputReceipt(receipt)
	if mapped.Result != agent.InputNotApplied && mapped.Submission != "" {
		s.controlMu.Lock()
		if mapped.Submission != s.lastFinished {
			s.setActiveWorkLocked(mapped.Submission)
		}
		s.controlMu.Unlock()
	}
	return mapped, nil
}

func (d *Driver) AnswerInteraction(ctx context.Context, id, ref string, answer any) (runtime.Acceptance, error) {
	parsed, kind, err := parseAnswer(answer)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	if err := agent.ValidateAnswer(kind, parsed); err != nil {
		return runtime.Acceptance{}, agentrelay.MapError(err)
	}
	s, err := d.ordinarySession(id)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	receipt, err := s.native.Answer(ctx, agent.InteractionRef(ref), parsed)
	if err != nil {
		return runtime.Acceptance{}, agentrelay.MapError(err)
	}
	return agentrelay.MapInteractionReceipt(receipt).Evidence, nil
}

func parseAnswer(answer any) (agent.Answer, agent.InteractionKind, error) {
	value, ok := answer.(map[string]any)
	if !ok || value == nil {
		return nil, "", runtime.InvalidArgument("interaction requires an object answer")
	}
	if outcome, permission := value["outcome"]; permission {
		for field := range value {
			if field != "outcome" && field != "optionId" {
				return nil, "", runtime.InvalidArgument("permission answer contains unsupported fields")
			}
		}
		text, valid := outcome.(string)
		if !valid {
			return nil, "", runtime.InvalidArgument("permission outcome must be a string")
		}
		parsed := agent.PermissionAnswer{Outcome: agent.PermissionOutcome(text)}
		if option, present := value["optionId"]; present {
			optionID, valid := option.(string)
			if !valid {
				return nil, "", runtime.InvalidArgument("permission optionId must be a string")
			}
			parsed.OptionID = optionID
		}
		return parsed, agent.InteractionPermission, nil
	}
	action, form := value["action"]
	if !form {
		return nil, "", runtime.InvalidArgument("interaction answer kind is required")
	}
	for field := range value {
		if field != "action" && field != "content" {
			return nil, "", runtime.InvalidArgument("form answer contains unsupported fields")
		}
	}
	text, ok := action.(string)
	if !ok {
		return nil, "", runtime.InvalidArgument("form action must be a string")
	}
	parsed := agent.FormAnswer{Outcome: agent.FormOutcome(text)}
	if content, present := value["content"]; present {
		object, ok := content.(map[string]any)
		if !ok || object == nil {
			return nil, "", runtime.InvalidArgument("form content must be an object")
		}
		if _, err := json.Marshal(object); err != nil {
			return nil, "", runtime.InvalidArgument("form content must be JSON-compatible")
		}
		if text != string(agent.FormAccept) {
			return nil, "", runtime.InvalidArgument("declined or canceled form answer must not contain content")
		}
		parsed.Content = object
	}
	return parsed, agent.InteractionForm, nil
}

func (d *Driver) Cancel(ctx context.Context, id string) (runtime.CancelReceipt, error) {
	d.mu.Lock()
	s := d.sessions[id]
	stopped, stopping := d.stopped, d.stopping
	d.mu.Unlock()
	switch {
	case s == nil:
		return runtime.CancelReceipt{}, runtime.FailedPrecondition("session not found")
	case stopped || stopping:
		return runtime.CancelReceipt{}, runtime.FailedPrecondition("driver unavailable")
	case s.closing.Load():
		return runtime.CancelReceipt{}, runtime.FailedPrecondition("session closing")
	}
	s.cancelOp.Lock()
	defer s.cancelOp.Unlock()
	s.controlMu.Lock()
	if receipt, cancelErr, ok := s.cachedCancelLocked(); ok {
		s.controlMu.Unlock()
		return receipt, cancelErr
	}
	work := s.workForCancelLocked()
	s.controlMu.Unlock()
	if _, err := d.ordinarySession(id); err != nil {
		return runtime.CancelReceipt{}, err
	}
	if work == "" {
		return runtime.CancelReceipt{Result: "noActiveWork"}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtime.CancelReceipt{}, fmt.Errorf("not_sent: interrupt: %w", err)
	}
	receipt, err := s.native.Interrupt(ctx, agent.InterruptOptions{})
	if err != nil {
		mapped, notSent := mapCancelError(err)
		s.controlMu.Lock()
		if !notSent {
			if work == "" {
				work = s.workForCancelLocked()
			}
			if work != "" {
				s.cacheCancelLocked(work, runtime.CancelReceipt{}, mapped)
			}
		}
		s.controlMu.Unlock()
		return runtime.CancelReceipt{}, mapped
	}
	mapped := agentrelay.MapCancelReceipt(receipt)
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if mapped.Result == "noActiveWork" {
		s.clearActiveWorkLocked()
		return mapped, nil
	}
	if mapped.Target != "" && (s.activeWork == "" || s.activeWork == mapped.Target) {
		s.setActiveWorkLocked(mapped.Target)
	}
	if work == "" {
		work = mapped.Target
	}
	if work != "" {
		s.cacheCancelLocked(work, mapped, nil)
	}
	return mapped, nil
}

func mapCancelError(err error) (error, bool) {
	var call *sdk.CallError
	if errors.As(err, &call) && call.Dispatch == sdk.NotSent {
		return fmt.Errorf("not_sent: interrupt: %w", agentrelay.MapError(err)), true
	}
	mapped := agentrelay.MapError(err)
	if agent.CodeOf(err) == agent.ErrorNativeRejected {
		return fmt.Errorf("native_rejected: interrupt: %w", mapped), false
	}
	return fmt.Errorf("outcome_unknown: interrupt: %w", mapped), false
}

func (s *session) observeWork(event events.SessionEvent, work string) {
	state, ok := event.Payload().(events.StateUpdate)
	if !ok || state.State == nil {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if *state.State == events.Idle {
		s.lastFinished = work
		if s.activeWork == "" || s.activeWork == work {
			s.clearActiveWorkLocked()
		}
		return
	}
	s.setActiveWorkLocked(work)
}

func (s *session) setActiveWorkLocked(work string) {
	if work == "" || s.activeWork == work {
		return
	}
	s.activeWork = work
	if s.cancelWork != work {
		s.clearCancelLocked()
	}
}

func (s *session) clearActiveWorkLocked() {
	s.activeWork = ""
}

func (s *session) workForCancelLocked() string {
	return s.activeWork
}

func (s *session) cachedCancelLocked() (runtime.CancelReceipt, error, bool) {
	work := s.workForCancelLocked()
	if work == "" || s.cancelWork != work {
		return runtime.CancelReceipt{}, nil, false
	}
	return s.cancelReceipt, s.cancelErr, true
}

func (s *session) cacheCancelLocked(work string, receipt runtime.CancelReceipt, err error) {
	s.cancelWork = work
	s.cancelReceipt = receipt
	s.cancelErr = err
}

func (s *session) clearCancelLocked() {
	s.cancelWork = ""
	s.cancelReceipt = runtime.CancelReceipt{}
	s.cancelErr = nil
}

func (d *Driver) CloseSession(ctx context.Context, id string) error {
	d.mu.Lock()
	s := d.sessions[id]
	if s == nil {
		d.mu.Unlock()
		return nil
	}
	s.closing.Store(true)
	d.mu.Unlock()

	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	closeErr := agentrelay.MapError(s.native.Close(context.WithoutCancel(ctx)))
	if closeErr != nil {
		return closeErr
	}
	select {
	case <-s.relay.Done():
	default:
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
		d.mu.Unlock()
		return nil
	}
	if len(d.sessions) != 0 || d.opening != 0 {
		d.mu.Unlock()
		return errors.New("close sessions before stopping")
	}
	d.stopping = true
	provider := d.provider
	d.mu.Unlock()

	if provider != nil {
		if err := provider.Stop(context.WithoutCancel(ctx)); err != nil {
			mapped := agentrelay.MapError(err)
			d.mu.Lock()
			d.stopErr = mapped
			d.mu.Unlock()
			return mapped
		}
	}
	d.mu.Lock()
	d.stopped = true
	d.stopping = false
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.pipe.End(nil)
	return nil
}

func (d *Driver) ordinarySession(id string) (*session, error) {
	d.mu.Lock()
	s := d.sessions[id]
	unavailable := d.stopped || d.stopping || s == nil || s.closing.Load()
	d.mu.Unlock()
	if unavailable {
		return nil, runtime.FailedPrecondition("session unavailable")
	}
	if err := s.observationError(); err != nil {
		return nil, err
	}
	return s, nil
}

func (d *Driver) reader() (codexReadClient, error) {
	d.mu.Lock()
	active := d.providerReady && !d.stopped && !d.stopping
	read := d.nativeClient
	d.mu.Unlock()
	if !active || read == nil {
		return nil, noResources("driver not running")
	}
	client, err := read()
	if err != nil {
		return nil, agentrelay.MapError(err)
	}
	if client == nil {
		return nil, noResources("native reader unavailable")
	}
	return client, nil
}

func validateRecoveryMaterial(saved ct.Thread, id, cwd string) error {
	if saved.ID != id || saved.SessionID == "" || saved.Ephemeral || !runtime.EqualNativePath(saved.Cwd, cwd) {
		return noResources("invalid native recovery context")
	}
	return nil
}

func validateOpenedThread(thread ct.Thread, id, cwd string) error {
	if thread.ID != id || thread.SessionID == "" || thread.Ephemeral || !runtime.EqualNativePath(thread.Cwd, cwd) {
		return errors.New("invalid native thread identity")
	}
	return nil
}

func sameRecoveryIdentity(saved, observed ct.Thread) bool {
	return observed.ID == saved.ID &&
		observed.SessionID == saved.SessionID &&
		runtime.EqualNativePath(observed.Cwd, saved.Cwd) &&
		sameNativeCreatedAt(observed.CreatedAt, saved.CreatedAt) &&
		observed.ModelProvider == saved.ModelProvider &&
		!observed.Ephemeral &&
		reflect.DeepEqual(observed.Path, saved.Path)
}

func agentFeatures(features *runtime.SessionFeatures) agent.Features {
	if features == nil || features.AgentQuestions == nil {
		return agent.Features{}
	}
	enabled := *features.AgentQuestions
	return agent.Features{AgentQuestions: &enabled}
}

func copyCapabilities(capabilities runtime.Capabilities) runtime.Capabilities {
	return maps.Clone(capabilities)
}

func noResources(message string) error {
	return errors.Join(runtime.ErrResourcesReleased, errors.New(message))
}

func outcomeUnknown(reason, message string, cause error) error {
	err := &runtime.OperationError{Code: "outcome_unknown", Reason: reason, Message: message}
	if cause != nil {
		return errors.Join(err, cause)
	}
	return err
}

func codexTurnID(event events.SessionEvent) string {
	codexMeta, ok := event.Meta["codex"].(map[string]any)
	if !ok {
		return ""
	}
	turnID, _ := codexMeta["turnId"].(string)
	return turnID
}

func sameNativeCreatedAt(a, b int64) bool {
	return a > 0 && b > 0 && a-b >= -1 && a-b <= 1
}
