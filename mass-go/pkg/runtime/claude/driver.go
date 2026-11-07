// Package claude converts actual SDK observations directly into SessionEvent.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	sdk "github.com/zoumo/agent-sdk-go/claude"
	"github.com/zoumo/agent-sdk-go/claude/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
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
	return o, nil
}

type Factory struct{ RecoveryDir string }

func (Factory) Validate(c runtime.Config) error { _, err := parse(c); return err }
func (f Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	o, err := parse(c)
	if err != nil {
		return nil, err
	}
	return &Driver{vault: recovery.Vault{Dir: f.RecoveryDir, Kind: "claude", Owner: name, Executable: o.Executable}, options: o, pipe: events.NewEventPipe(128), sessions: map[string]*session{}}, nil
}

type session struct {
	command                  sync.Mutex
	work                     string
	cancelTarget             string
	cancelWork               string
	cancelAttempted          bool
	cancelErr                error
	submissions              int
	observationWork          string
	observation              *sdk.Received
	diagnostics              []any
	attribution              string
	uncertain                bool
	requireLink              bool
	results                  map[string]string
	submitted                map[string]bool
	mu                       sync.Mutex
	address                  runtime.Address
	source                   events.Source
	client                   *sdk.Session
	id                       string
	ready                    chan struct{}
	done                     chan struct{}
	closing, active, sending bool
	err                      error
	state                    *events.SessionState
	// Native message identity for partial events; no content is accumulated.
	messageID     string
	messageRoot   bool
	messages      map[string]messageClaim
	tools         map[string]string
	lanes         map[string]*liveLane
	blocks        map[blockKey]*liveBlock
	seenAssistant map[string]struct{}
}
type Driver struct {
	opening                 int
	vault                   recovery.Vault
	mu                      sync.Mutex
	op                      sync.Mutex
	options                 options
	pipe                    *events.EventPipe
	taken, started, stopped bool
	sessions                map[string]*session
	loads                   map[string]*nativeLoad
	interactions            runtime.PendingInteractions
}

func caps() runtime.Capabilities {
	return runtime.Capabilities{"textInput": true, "permission": true, "form": true}
}

var supportedSessionFeatures = runtime.FeatureSupport{AgentQuestions: true}

func sessionOptions(features runtime.SessionFeatures, mcpServers *[]runtime.MCPServer, resumeID, cwd, executable string, env []string) (sdk.SessionOptions, error) {
	converted, err := claudeMCPServers(mcpServers)
	if err != nil {
		return sdk.SessionOptions{}, err
	}
	options := sdk.SessionOptions{ResumeID: resumeID, CLIPath: executable, WorkDir: cwd, Env: env, MCPServers: converted}
	if !features.AgentQuestionsEnabled() {
		options.DisallowedTools = []string{"AskUserQuestion"}
	}
	return options, nil
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

func (d *Driver) Start(context.Context) (runtime.Capabilities, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return nil, errors.New("driver stopped")
	}
	d.started = true
	return caps(), nil
}

func (d *Driver) OpenSession(ctx context.Context, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, "", a, spec, nil)
}

func (*Driver) ValidateInitialInput(input runtime.Input) error {
	return runtime.ValidateTextInput(input)
}

func (d *Driver) OpenSessionWithInput(ctx context.Context, a runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	return d.open(ctx, "", a, spec, &input)
}

func (d *Driver) ResumeSession(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, id, a, spec, nil)
}

func (d *Driver) ResumeSessionWithInput(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec, input runtime.Input) (runtime.DriverSession, error) {
	if err := d.ValidateInitialInput(input); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	// open validates saved targeting before Submit, then gates success and early
	// facts on observed init identity/cwd. Local write does not prove that identity.
	return d.open(ctx, id, a, spec, &input)
}

func (d *Driver) open(ctx context.Context, resume string, a runtime.Address, spec runtime.SessionSpec, initial *runtime.Input) (runtime.DriverSession, error) {
	d.mu.Lock()
	if d.stopped || len(d.sessions)+d.opening >= 128 {
		d.mu.Unlock()
		return runtime.DriverSession{}, noResources("driver unavailable or session capacity exhausted")
	}
	d.opening++
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.opening--; d.mu.Unlock() }()
	if err := d.vault.Check(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	if !filepath.IsAbs(spec.Cwd) {
		return runtime.DriverSession{}, noResources("absolute cwd required")
	}
	if spec.SystemPrompt != "" || len(spec.AdditionalDirs) > 0 {
		return runtime.DriverSession{}, noResources("Claude SDK does not support systemPrompt or additionalDirs")
	}
	effective := runtime.ResolveSessionFeatures(spec.Features, supportedSessionFeatures)
	d.mu.Lock()
	running := d.started && !d.stopped
	d.mu.Unlock()
	if !running {
		return runtime.DriverSession{}, noResources("driver not running")
	}
	env := d.options.Environment("claude")
	if resume != "" {
		m, err := d.vault.Load(resume, a, spec)
		if err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		var saved struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(m.Context, &saved); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if saved.ID != resume || !runtime.EqualNativePath(saved.Cwd, spec.Cwd) {
			return runtime.DriverSession{}, noResources("invalid Claude recovery context")
		}
		if initial == nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, &runtime.OperationError{
				Code:    "unsupported",
				Reason:  "resume_identity_unobservable",
				Message: "Claude zero-input resume cannot observe native session identity; provide explicit initialInput",
			})
		}
	}
	s := &session{address: a, source: events.NewSource(), ready: make(chan struct{}), done: make(chan struct{}), tools: map[string]string{}, results: map[string]string{}, submitted: map[string]bool{}, attribution: "unknown", requireLink: resume != ""}
	options, err := sessionOptions(effective, spec.MCPServers, resume, spec.Cwd, d.options.Executable, env)
	if err != nil {
		return runtime.DriverSession{}, noResources(err.Error())
	}
	c, err := sdk.Open(ctx, options)
	if err != nil {
		close(s.ready)
		// Failed SDK startup closes its unreturned connection before returning.
		// Native outcome uncertainty is not resource ownership; a cleanup timeout is.
		if !errors.Is(err, sdk.ErrShutdownTimeout) {
			err = errors.Join(runtime.ErrResourcesReleased, err)
		}
		return runtime.DriverSession{}, err
	}
	s.client = c
	var receipt *runtime.InputReceipt
	fail := func(cause error) error {
		s.mu.Lock()
		uncertain := s.uncertain
		s.mu.Unlock()
		if resume != "" && (receipt != nil || uncertain) {
			// Releasing the connection does not undo an already written input.
			cause = errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_input_unverified", Message: "New input dispatch or original session activation was not verified; do not resend"}, cause)
		}
		return releasedOpenError(cause, c)
	}
	if initial != nil {
		accepted, err := d.submit(ctx, s, *initial)
		if err != nil {
			close(s.ready)
			return runtime.DriverSession{}, fail(err)
		}
		receipt = &accepted
	}
	early := []sdk.Received{}
	// Only native system/init establishes identity, with or without explicit input.
	for {
		event, receiveErr := c.Receive(ctx)
		if receiveErr != nil {
			close(s.ready)
			return runtime.DriverSession{}, fail(fmt.Errorf("claude missing native init identity: %w", receiveErr))
		}
		e := event.Event
		if e == nil && event.Kind == sdk.ReceivedObservation {
			if len(early) == 128 {
				close(s.ready)
				return runtime.DriverSession{}, fail(runtime.Exhausted("pre_identity_buffer"))
			}
			early = append(early, event)
			continue
		}
		if e == nil {
			// Before identity, a live request cannot be safely exposed or answered.
			// Fail with owned cleanup rather than silently skipping it and waiting forever.
			close(s.ready)
			return runtime.DriverSession{}, fail(&runtime.OperationError{Code: "outcome_unknown", Reason: "pre_identity_native_observation", Message: "Native request or diagnostic arrived before a validated session identity"})
		}
		if e.Type == "system" && e.Subtype == "init" && e.SessionID != "" {
			if resume != "" && e.SessionID != resume {
				close(s.ready)
				return runtime.DriverSession{}, fail(errors.New("native resume identity mismatch"))
			}
			var init struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(e.Raw, &init); err != nil || !runtime.EqualNativePath(init.Cwd, spec.Cwd) {
				close(s.ready)
				return runtime.DriverSession{}, fail(errors.New("native init cwd mismatch"))
			}
			s.id = e.SessionID
			break
		}
		// Pre-identity content is unsupported, rather than lost or given a fake ID.
		if e.Type == "assistant" || e.Type == "user" || e.Type == "stream_event" || e.Type == "result" {
			if len(early) == 128 {
				close(s.ready)
				return runtime.DriverSession{}, fail(errors.New("pre-identity buffer exhausted"))
			}
			early = append(early, event)
		}
	}
	if resume == "" {
		if err := d.vault.Save(s.id, a, spec, map[string]string{"id": s.id, "cwd": spec.Cwd}); err != nil {
			close(s.ready)
			return runtime.DriverSession{}, fail(err)
		}
	}
	d.mu.Lock()
	if d.sessions[s.id] != nil {
		d.mu.Unlock()
		close(s.ready)
		return runtime.DriverSession{}, fail(errors.New("duplicate native identity"))
	}
	d.sessions[s.id] = s
	d.mu.Unlock()
	close(s.ready)
	go d.observe(s, early)
	return runtime.DriverSession{SessionID: s.id, Capabilities: caps(), EffectiveFeatures: effective, Source: s.source, InitialInputReceipt: receipt}, nil
}

func (d *Driver) get(id string) (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil || d.stopped {
		return nil, runtime.FailedPrecondition("session unavailable")
	}
	return s, nil
}

func (d *Driver) SendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	s, err := d.get(id)
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	return d.submit(ctx, s, input)
}

func (d *Driver) submit(ctx context.Context, s *session, input runtime.Input) (runtime.InputReceipt, error) {
	s.command.Lock()
	defer s.command.Unlock()
	if err := runtime.ValidateTextInput(input); err != nil {
		return runtime.InputReceipt{}, err
	}
	s.mu.Lock()
	if s.active || s.sending || s.closing || s.err != nil || s.uncertain {
		s.mu.Unlock()
		return runtime.InputReceipt{}, runtime.FailedPrecondition("session unavailable or work unsettled")
	}
	if len(s.submitted) >= 128 {
		s.mu.Unlock()
		return runtime.InputReceipt{}, runtime.Exhausted("submission_attribution_capacity")
	}
	s.active = true
	s.submissions++
	s.work = uuid.NewString()
	work := s.work
	s.submitted[work] = true
	count := s.submissions
	s.sending = true
	s.mu.Unlock()
	native := sdk.Input{Text: input.Content[0].Text}
	if len(input.Content) > 1 {
		blocks := make([]types.ContentBlock, len(input.Content))
		for i, b := range input.Content {
			blocks[i] = types.ContentBlock{Type: "text", Text: &types.TextBlock{Text: b.Text}}
		}
		native = sdk.Input{User: &types.UserInput{Message: types.InputMessage{Role: "user", Content: types.Content{Blocks: blocks}}}}
	}
	// Only a fresh session can infer its first submission. Resumed sessions and
	// later inputs carry a caller UUID, not a TurnID; an echo is never presumed.
	if count > 1 || s.requireLink {
		if native.User == nil {
			text := input.Content[0].Text
			native = sdk.Input{User: &types.UserInput{Message: types.InputMessage{Role: "user", Content: types.Content{Text: &text}}}}
		}
		native.User.UUID = &work
	}
	_, err := s.client.Submit(ctx, native)
	s.mu.Lock()
	s.sending = false
	if err != nil {
		var dispatch *sdk.DispatchError
		if errors.As(err, &dispatch) && dispatch.State == sdk.NotSent {
			s.active = false
			s.submissions--
			s.work = ""
			delete(s.submitted, work)
			err = errors.Join(runtime.FailedPrecondition("Claude input was not sent"), err)
		} else {
			s.uncertain = true
		}
	}
	s.mu.Unlock()
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	return runtime.InputReceipt{Submission: work, Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, nil
}

func identityMeta(id sdk.SubagentIdentity) map[string]any {
	meta := map[string]any{}
	for key, value := range map[string]string{"association_id": id.AssociationID, "run_id": id.RunID, "session_id": id.SessionID, "agent_id": id.AgentID, "task_id": id.TaskID, "tool_use_id": id.ToolUseID} {
		if value != "" {
			meta[key] = value
		}
	}
	return meta
}

func (d *Driver) emit(s *session, item string, p events.Payload, at time.Time) error {
	return d.emitWithClaudeMeta(s, item, p, at, nil)
}

func (d *Driver) emitWithClaudeMeta(s *session, item string, p events.Payload, at time.Time, claude map[string]any) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	meta := map[string]any{"attribution": s.attribution}
	if len(s.diagnostics) > 0 {
		meta["observations"] = s.diagnostics
	}
	if r := s.observation; r != nil {
		meta["source"], meta["origin"], meta["nativeRaw"] = identityMeta(r.Source), r.Origin, string(r.Raw)
		if r.Parent != nil {
			meta["parent"] = identityMeta(*r.Parent)
		}
	}
	if s.observationWork != "" {
		meta["submission"] = s.observationWork
	}
	maps.Copy(meta, claude)
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: s.address.Namespace, SessionName: s.address.SessionName, SessionID: s.id, ItemID: item, Time: at, Meta: map[string]any{"claude": meta}}, p, s.source.WithWork(s.observationWork))
	if err != nil {
		return err
	}
	// Snapshot under the session lock, but never hold it across backpressure:
	// Close must be able to seal the native FIFO while this observer drains.
	s.mu.Unlock()
	defer s.mu.Lock()
	return d.pipe.Send(context.Background(), e)
}

func (d *Driver) state(s *session, state *events.SessionState, reason string) error {
	previous := s.state
	s.state = state
	return d.emit(s, "", events.StateUpdate{PreviousState: previous, State: state, StopReason: reason}, time.Time{})
}

func (d *Driver) observe(s *session, early []sdk.Received) {
	defer close(s.done)
	for _, event := range early {
		s.mu.Lock()
		err := d.adapt(s, event)
		if err != nil {
			s.err = err
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
	for {
		event, err := s.client.Receive(context.Background())
		s.mu.Lock()
		if err == nil {
			switch event.Kind {
			case sdk.ReceivedObservation:
				err = d.adapt(s, event)
			case sdk.ReceivedNative:
				if event.Event != nil {
					err = d.adapt(s, event)
				}
			case sdk.ReceivedRequest:
				if event.Request != nil && !s.closing && event.Request.Context().Err() == nil {
					s.observationWork, s.attribution = "", "unknown"
					if s.submissions == 1 && !s.requireLink {
						s.observationWork, s.attribution = s.work, "single_submission"
					}
					var linkage struct {
						UUID string `json:"user_message_uuid"`
					}
					if json.Unmarshal(event.Request.Raw(), &linkage) == nil && linkage.UUID != "" {
						s.observationWork, s.attribution = "", "unknown"
						if s.submitted[linkage.UUID] {
							s.observationWork, s.attribution = linkage.UUID, "user_message_uuid"
						}
					}
					if event.Parent != nil {
						s.observationWork, s.attribution = "", "unknown"
					}
					if s.observationWork != "" && s.observationWork == s.work && s.active {
						err = d.permission(s, event.Request)
					} else {
						err = d.emit(s, "", events.SessionError{Message: "Claude interaction is not attributable to current work; no current answer authority admitted", Reason: "interaction_attribution_unknown"}, time.Time{})
						if err == nil && s.observationWork == "" && s.active && event.Parent == nil {
							s.uncertain = true
							err = d.state(s, nil, "interaction_attribution_unknown")
						}
					}
				}
			}
		}
		if err != nil {
			if !s.closing || !errors.Is(err, io.EOF) {
				if s.err == nil {
					s.err = err
				}
				s.observationWork, s.attribution = "", "unknown"
				_ = d.emit(s, "", events.SessionError{Message: "Claude observation failed", Reason: "native_observation_failed"}, time.Time{})
				_ = d.state(s, nil, "")
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func decode(raw json.RawMessage) (any, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err := dec.Decode(&value)
	return value, err
}

func (d *Driver) adapt(s *session, received sdk.Received) error {
	if received.Kind == sdk.ReceivedObservation && received.Observation != nil {
		if len(s.diagnostics) == 128 {
			return runtime.Exhausted("native_diagnostic_capacity")
		}
		o := received.Observation
		s.diagnostics = append(s.diagnostics, map[string]any{"reason": o.Reason, "evidence": o.Evidence, "source": identityMeta(o.Source)})
		return nil
	}
	if received.Event == nil {
		return errors.New("unsupported pre-identity native record")
	}
	e := *received.Event
	child := received.Parent != nil
	s.observation = &received
	defer func() { s.observation = nil }()
	if e.SessionID != "" && e.SessionID != s.id {
		return nil
	} // unproven descendants are not this Session
	s.observationWork, s.attribution = "", "unknown"
	root := !child && (e.ParentToolUseID == nil || *e.ParentToolUseID == "") && (e.AgentID == nil || *e.AgentID == "")
	if root {
		if e.UserMessageUUID != nil && *e.UserMessageUUID != "" {
			// A known caller UUID is evidence; arbitrary request IDs are not.
			if s.submitted[*e.UserMessageUUID] {
				s.observationWork, s.attribution = *e.UserMessageUUID, "user_message_uuid"
			}
		} else if previous, ok := s.results[e.UUID]; e.UUID != "" && ok {
			s.observationWork, s.attribution = previous, "prior_result"
		} else if s.submissions == 1 && s.work != "" && !s.requireLink {
			s.observationWork, s.attribution = s.work, "single_submission"
		}
		// Conflicting native identities are not evidence for either submission.
		if previous, ok := s.results[e.UUID]; e.Type == "result" && ok && previous != s.observationWork {
			s.observationWork, s.attribution = "", "unknown"
		}
	}
	var raw struct {
		Time          time.Time `json:"timestamp"`
		Aborted       bool      `json:"aborted"`
		APIBlockIndex *int      `json:"apiBlockIndex"`
		Message       struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		UUID  string `json:"uuid"`
		Event struct {
			Type         string          `json:"type"`
			Index        *int            `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
			Message      struct {
				ID string `json:"id"`
			} `json:"message"`
			Delta struct{ Type, Text, Thinking string } `json:"delta"`
		} `json:"event"`
	}
	if err := json.Unmarshal(e.Raw, &raw); err != nil {
		return err
	}
	trusted := root && e.SessionID == s.id && received.Origin == "live" && received.Source == (sdk.SubagentIdentity{SessionID: s.id})
	parent := ""
	if e.ParentToolUseID != nil {
		parent = *e.ParentToolUseID
	}
	switch e.Type {
	case "stream_event":
		switch raw.Event.Type {
		case "message_start":
			if err := s.beginResponse(parent, raw.Event.Message.ID); err != nil {
				return err
			}
			if parent == "" {
				s.messageID = raw.Event.Message.ID
				s.messageRoot = trusted
				s.linkMessage(s.messageID, true, trusted, e.UserMessageUUID)
			}
			if !root || s.observationWork == "" || s.observationWork != s.work || !s.active {
				return nil
			}
			state := events.Running
			return d.state(s, &state, "")
		case "message_stop":
			if parent == "" {
				s.messageRoot = false
			}
		case "content_block_start":
			if raw.Event.Index == nil || *raw.Event.Index < 0 {
				return nil
			}
			var block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			}
			if len(raw.Event.ContentBlock) > 0 {
				if err := json.Unmarshal(raw.Event.ContentBlock, &block); err != nil {
					return err
				}
			}
			if err := s.noteBlockStart(parent, *raw.Event.Index, block.Type, block.ID); err != nil {
				return err
			}
		case "content_block_stop":
			if raw.Event.Index == nil || *raw.Event.Index < 0 {
				return nil
			}
			s.noteBlockStop(parent, *raw.Event.Index)
		case "content_block_delta":
			response := s.response(parent)
			if response == "" || raw.Event.Index == nil || *raw.Event.Index < 0 {
				return nil
			}
			index := *raw.Event.Index
			var kind, text string
			switch raw.Event.Delta.Type {
			case "text_delta":
				kind, text = "text", raw.Event.Delta.Text
			case "thinking_delta":
				kind, text = "thinking", raw.Event.Delta.Thinking
			case "input_json_delta", "signature_delta":
				return nil // unsupported preview, never reconstruct full content
			default:
				return errors.New("unsupported Claude delta type")
			}
			if text == "" {
				return nil
			}
			key := blockKey{parent: parent, response: response, index: index}
			block, ok, err := s.deltaBlock(key, kind)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if block.completed {
				return nil
			}
			if parent == "" {
				s.linkPreview(trusted && s.messageRoot, e.UserMessageUUID)
			}
			return d.emitChunk(s, key, kind, text, raw.Time)
		}
	case "assistant", "user":
		if e.Type == "assistant" {
			duplicate, err := s.markAssistantFrame(parent, raw.UUID)
			if err != nil {
				return err
			}
			if duplicate || raw.Aborted {
				return nil
			}
			s.linkMessage(raw.Message.ID, false, trusted, e.UserMessageUUID)
		}
		id := raw.Message.ID
		if id == "" && e.Type == "user" {
			id = raw.UUID
		}
		var blocks []map[string]json.RawMessage
		if len(raw.Message.Content) > 0 && raw.Message.Content[0] == '"' {
			var text string
			if err := json.Unmarshal(raw.Message.Content, &text); err != nil {
				return err
			}
			blocks = []map[string]json.RawMessage{{"type": json.RawMessage(`"text"`), "text": raw.Message.Content}}
		} else if err := json.Unmarshal(raw.Message.Content, &blocks); err != nil {
			return err
		}
		if e.Type == "assistant" && raw.APIBlockIndex != nil && len(blocks) == 1 {
			if _, state := blockCoordinate(blocks[0]); state == coordinateMissing {
				blocks[0]["apiBlockIndex"] = json.RawMessage(strconv.Itoa(*raw.APIBlockIndex))
			}
		}
		text, thinking := []events.ContentBlock{}, []events.ContentBlock{}
		for _, b := range blocks {
			str := func(key string) string { var v string; _ = json.Unmarshal(b[key], &v); return v }
			switch str("type") {
			case "text":
				if e.Type == "assistant" {
					continue
				}
				var value *string
				if err := json.Unmarshal(b["text"], &value); err != nil || value == nil {
					return errors.New("missing or invalid text")
				}
				text = append(text, events.Text(*value))
			case "thinking":
				if e.Type == "assistant" {
					continue
				}
				var value *string
				if err := json.Unmarshal(b["thinking"], &value); err != nil || value == nil {
					return errors.New("missing or invalid thinking")
				}
				thinking = append(thinking, events.Text(*value))
			case "redacted_thinking":
				continue
			case "tool_use":
				tool := str("id")
				kind := str("name")
				if kind == "" || tool == "" {
					return errors.New("tool use missing identity")
				}
				input, err := decode(b["input"])
				if err != nil {
					return err
				}
				if len(s.tools) >= 128 {
					return errors.New("pending tool limit")
				}
				s.tools[tool] = kind
				if err := d.emit(s, tool, events.ToolCall{ID: tool, Kind: kind, RawInput: input}, raw.Time); err != nil {
					return err
				}
			case "tool_result":
				tool := str("tool_use_id")
				kind := s.tools[tool]
				if kind == "" {
					return errors.New("tool result has no observed tool use")
				}
				p := events.ToolResult{ID: tool, Kind: kind, Status: "completed", Output: &events.Output{Availability: "unavailable", Completeness: "unknown"}}
				var failed bool
				if value, ok := b["is_error"]; ok {
					if err := json.Unmarshal(value, &failed); err != nil {
						return err
					}
				}
				if failed {
					p.Status = "failed"
				}
				if content, ok := b["content"]; ok {
					value, err := decode(content)
					if err != nil {
						return err
					}
					p.RawOutput = &value
					p.Output = &events.Output{Availability: "provided", Completeness: "retained"}
					if str, ok := value.(string); ok {
						p.Content = []events.ContentBlock{events.Text(str)}
					}
					if _, ok := value.([]any); ok {
						p.Content = []events.ContentBlock{}
						for _, block := range value.([]any) {
							m, ok := block.(map[string]any)
							if !ok || m["type"] != "text" {
								return errors.New("unsupported tool result content")
							}
							v, ok := m["text"].(string)
							if !ok {
								return errors.New("invalid text block")
							}
							p.Content = append(p.Content, events.Text(v))
						}
					}
				}
				delete(s.tools, tool)
				if err := d.emit(s, tool, p, raw.Time); err != nil {
					return err
				}
			default:
				return errors.New("unsupported Claude complete content block")
			}
		}
		if e.Type == "assistant" {
			response := raw.Message.ID
			if response == "" {
				response = s.response(parent)
			}
			return d.completeAssistant(s, parent, response, blocks, raw.Time)
		}
		if len(text) > 0 || len(thinking) > 0 {
			if id == "" {
				return errors.New("complete message missing native identity")
			}
			if len(thinking) > 0 {
				if err := d.emit(s, id, events.AgentThinking{MessageID: id, Content: thinking}, raw.Time); err != nil {
					return err
				}
			}
			if len(text) > 0 {
				p := events.MessagePayload{MessageID: id, Content: text}
				return d.emit(s, id, events.UserMessage(p), raw.Time)
			}
		}
	case "result":
		if !root || !s.active || (s.observationWork != "" && s.observationWork != s.work) {
			return nil
		}
		if s.observationWork == "" {
			s.uncertain = true
			if err := d.emit(s, "", events.SessionError{Message: "Claude result has no proven submission linkage; work remains unsettled", Reason: "work_attribution_unknown"}, raw.Time); err != nil {
				return err
			}
			return d.state(s, nil, "work_attribution_unknown")
		}
		if e.UUID != "" {
			if len(s.results) >= 128 {
				return errors.New("claude result attribution capacity exhausted")
			}
			s.results[e.UUID] = s.observationWork
		}
		s.active = false
		s.uncertain = false
		s.messageID = ""
		state := events.Idle
		return d.state(s, &state, e.Subtype)
	}
	return nil
}

func (d *Driver) permission(s *session, r *sdk.IncomingRequest) error {
	if r.Method() != "can_use_tool" || r.Handle() == nil {
		return d.emit(s, "", events.SessionError{Message: "unsupported native interaction", Reason: "unsupported_native_interaction"}, time.Time{})
	}
	p := r.Handle()
	request := events.PermissionRequest(p.ToolUseID(), p.ToolName())
	request.Content = []events.ContentBlock{events.Text(string(r.Params()))}
	if p.ToolName() == "AskUserQuestion" {
		value, err := decode(p.Input())
		if err != nil {
			return err
		}
		schema, ok := value.(map[string]any)
		if !ok {
			return errors.New("invalid question input")
		}
		request = events.InteractionRequest{Kind: "form", Message: "Answer native Claude questions", RequestedSchema: schema}
	}
	live := func() bool { return r.Context().Err() == nil }
	unsent := func(err error) bool {
		var native *sdk.DispatchError
		if errors.As(err, &native) {
			return native.State == sdk.NotSent
		}
		return errors.Is(err, runtime.ErrInvalidArgument)
	}
	source := s.source.WithWork(s.observationWork)
	ref, err := d.interactions.Add(s.id, source, live, unsent, func(ctx context.Context, a map[string]any) error {
		if request.Kind == "form" {
			if len(a) != 2 || a["action"] != "accept" {
				return runtime.InvalidArgument("expected explicit form accept/content")
			}
			content, ok := a["content"].(map[string]any)
			if !ok || content["answers"] == nil {
				return runtime.InvalidArgument("expected native question answers")
			}
			values, ok := content["answers"].(map[string]any)
			if !ok {
				return runtime.InvalidArgument("expected answers keyed by full native question text")
			}
			answers := make(map[string]string, len(values))
			for question, value := range values {
				text, ok := value.(string)
				if !ok {
					return runtime.InvalidArgument("question answers must be strings")
				}
				answers[question] = text
			}
			err := r.Answer(ctx, sdk.QuestionAnswers{Answers: answers})
			var native *sdk.DispatchError
			if errors.As(err, &native) && native.State == sdk.NotSent && errors.Is(err, sdk.ErrInvalidPermissionAnswer) {
				return errors.Join(runtime.ErrInvalidArgument, err)
			}
			return err
		} else {
			selection := a
			var updated json.RawMessage
			if input, exists := a["updatedInput"]; exists {
				if len(a) != 3 || a["optionId"] != "allow-once" {
					return runtime.InvalidArgument("updatedInput requires an allow-once selection")
				}
				object, ok := input.(map[string]any)
				if !ok || object == nil {
					return runtime.InvalidArgument("updatedInput must be an object")
				}
				var err error
				updated, err = json.Marshal(object)
				if err != nil {
					return runtime.InvalidArgument("invalid updatedInput")
				}
				selection = map[string]any{"outcome": a["outcome"], "optionId": a["optionId"]}
			}
			allow, err := runtime.PermissionAllowed(selection)
			if err != nil {
				return err
			}
			if !allow {
				return r.Deny(ctx, sdk.DenialOptions{Message: "Rejected"})
			}
			return r.Approve(ctx, sdk.ApprovalOptions{UpdatedInput: updated})
		}
	})
	if err != nil {
		return err
	}
	state := events.RequiresAction
	if err := d.state(s, &state, ""); err != nil {
		return err
	}
	if s.closing {
		return nil
	}
	select {
	case <-r.Context().Done():
		return nil
	default:
	}
	return d.emit(s, "", events.InteractionRequested{Interaction: ref, Request: request, Blocking: true}, time.Time{})
}

func (d *Driver) AnswerInteraction(ctx context.Context, id, ref string, answer any) (runtime.Acceptance, error) {
	a, ok := answer.(map[string]any)
	if !ok || a == nil {
		return runtime.Acceptance{}, runtime.InvalidArgument("interaction requires an object answer")
	}
	s, err := d.get(id)
	if err != nil {
		return runtime.Acceptance{}, err
	}
	s.command.Lock()
	defer s.command.Unlock()
	s.mu.Lock()
	source := s.source.WithWork(s.work)
	valid := !s.closing && s.err == nil
	s.mu.Unlock()
	if !valid {
		return runtime.Acceptance{}, errors.New("original interaction run unavailable")
	}
	if err := d.interactions.Answer(ctx, id, source, ref, a); err != nil {
		return runtime.Acceptance{}, err
	}
	return runtime.Acceptance{Sent: runtime.Yes()}, nil
}

func (d *Driver) CloseSession(ctx context.Context, id string) error {
	d.mu.Lock()
	s := d.sessions[id]
	stopped := d.stopped
	d.mu.Unlock()
	if s == nil {
		if stopped {
			return runtime.FailedPrecondition("session unavailable")
		}
		return nil
	}
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	d.interactions.RemoveSession(id)
	closeErr := s.client.Close()
	select {
	case <-s.done:
	case <-ctx.Done():
		if closeErr != nil {
			return closeErr
		}
		return ctx.Err()
	}
	if err := d.pipe.Flush(ctx); err != nil {
		return errors.Join(closeErr, err)
	}
	if cause := s.client.Err(); cause != nil {
		return errors.Join(closeErr, fmt.Errorf("native observation terminated: %w", cause))
	}
	s.mu.Lock()
	cause := s.err
	s.mu.Unlock()
	if cause != nil {
		return errors.Join(closeErr, fmt.Errorf("observation terminated: %w", cause))
	}
	if closeErr != nil {
		return fmt.Errorf("native cleanup failed: %w", closeErr)
	}
	d.mu.Lock()
	if d.sessions[id] == s {
		delete(d.sessions, id)
	}
	d.interactions.RemoveSession(id)
	d.mu.Unlock()
	return nil
}

func (d *Driver) Stop(context.Context) error {
	d.op.Lock()
	defer d.op.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sessions) > 0 || d.opening != 0 {
		return errors.New("close sessions before stopping")
	}
	d.stopped = true
	d.pipe.End(nil)
	return nil
}

func (d *Driver) Cancel(ctx context.Context, id string) (runtime.CancelReceipt, error) {
	s, err := d.get(id)
	if err != nil {
		return runtime.CancelReceipt{}, err
	}
	s.command.Lock()
	defer s.command.Unlock()
	s.mu.Lock()
	active := s.active
	if active && (s.cancelTarget == "" || s.cancelWork != s.work) {
		s.cancelWork, s.cancelTarget = s.work, uuid.NewString()
		s.cancelAttempted, s.cancelErr = false, nil
	}
	target := s.cancelTarget
	attempted, previousErr := s.cancelAttempted, s.cancelErr
	s.mu.Unlock()
	if !active {
		return runtime.CancelReceipt{Result: "noActiveWork"}, nil
	}
	if attempted {
		if previousErr != nil {
			return runtime.CancelReceipt{}, previousErr
		}
		return runtime.CancelReceipt{Target: target, Result: "controlAcknowledged"}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtime.CancelReceipt{}, fmt.Errorf("not_sent: interrupt: %w", err)
	}
	err = s.client.Interrupt(ctx, sdk.InterruptOptions{})
	var dispatch *sdk.DispatchError
	notSent := errors.As(err, &dispatch) && dispatch.State == sdk.NotSent
	if err != nil {
		var rejected *sdk.ControlError
		switch {
		case notSent:
			err = fmt.Errorf("not_sent: interrupt: %w", err)
		case errors.As(err, &rejected):
			err = fmt.Errorf("native_rejected: interrupt: %w", err)
		default:
			err = fmt.Errorf("outcome_unknown: interrupt: %w", err)
		}
	}
	s.mu.Lock()
	s.cancelAttempted, s.cancelErr = !notSent, err
	s.mu.Unlock()
	if err != nil {
		return runtime.CancelReceipt{}, err
	}
	return runtime.CancelReceipt{Target: target, Result: "controlAcknowledged"}, nil
}

func releasedOpenError(cause error, c *sdk.Session) error {
	cleanup := c.Close()
	var native *sdk.Error
	if cleanup == nil || (errors.As(cleanup, &native) && native.ExitCode != nil) {
		return errors.Join(runtime.ErrResourcesReleased, cause, cleanup)
	}
	return errors.Join(cause, cleanup)
}

func noResources(message string) error {
	return errors.Join(runtime.ErrResourcesReleased, errors.New(message))
}
