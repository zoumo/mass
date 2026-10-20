// Package claude converts actual SDK observations directly into SessionEvent.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	sdk "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/recovery"
)

type options struct{ executable, scenario string }

func parse(c runtime.Config) (options, error) {
	var o options
	for k, v := range c {
		s, ok := v.(string)
		if !ok {
			return o, fmt.Errorf("%s must be a string", k)
		}
		switch k {
		case "executable":
			o.executable = s
		case "scenario":
			o.scenario = s
		default:
			return o, fmt.Errorf("unknown Claude config %q", k)
		}
	}
	if !filepath.IsAbs(o.executable) || !filepath.IsAbs(o.scenario) {
		return o, errors.New("explicit absolute executable and synthetic scenario required")
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
	return &Driver{vault: recovery.Vault{Dir: f.RecoveryDir, Kind: "claude", Owner: name, Executable: o.executable}, options: o, pipe: events.NewEventPipe(128), sessions: map[string]*session{}}, nil
}

type session struct {
	command                  sync.Mutex
	work                     string
	cancelTarget             string
	cancelWork               string
	used                     bool
	mu                       sync.Mutex
	address                  runtime.Address
	source                   events.Source
	client                   *sdk.Client
	id                       string
	ready                    chan struct{}
	done                     chan struct{}
	closing, active, sending bool
	err                      error
	state                    *events.SessionState
	// Native message identity for partial events; no content is accumulated.
	messageID string
	tools     map[string]string
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
	interactions            runtime.PendingInteractions
}

func caps() runtime.Capabilities {
	return runtime.Capabilities{"textInput": true, "permission": true, "form": true}
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
	return d.open(ctx, "", a, spec)
}

func (d *Driver) ResumeSession(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, id, a, spec)
}

func (d *Driver) open(ctx context.Context, resume string, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
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
	d.mu.Lock()
	running := d.started && !d.stopped
	d.mu.Unlock()
	if !running {
		return runtime.DriverSession{}, noResources("driver not running")
	}
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
		if saved.ID != resume || saved.Cwd != spec.Cwd {
			return runtime.DriverSession{}, noResources("invalid Claude recovery context")
		}
	}
	s := &session{address: a, source: events.NewSource(), ready: make(chan struct{}), done: make(chan struct{}), tools: map[string]string{}}
	c, err := sdk.Open(ctx, sdk.Options{ResumeID: resume, CLIPath: d.options.executable, WorkDir: spec.Cwd, Env: []string{"CLAUDE_MOCK_SCENARIO=" + d.options.scenario}, PermissionHandler: func(p *sdk.PermissionRequest) {
		select {
		case <-s.ready:
		case <-p.Context().Done():
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.id == "" || s.closing {
			return
		}
		if err := d.permission(s, p); err != nil {
			s.err = err
			_ = d.emit(s, "", events.SessionError{Message: "Claude permission delivery failed"}, time.Time{})
		}
	}})
	if err != nil {
		close(s.ready)
		return runtime.DriverSession{}, err
	}
	s.client = c
	early := []sdk.Event{}
	// No hidden input: only a native system/init can establish creation identity.
	for {
		event, receiveErr := c.Receive(ctx)
		if receiveErr != nil {
			close(s.ready)
			return runtime.DriverSession{}, releasedOpenError(fmt.Errorf("claude missing native init identity: %w", receiveErr), c)
		}
		e, ok := event.(sdk.Event)
		if !ok {
			continue
		}
		if e.Type == "system" && e.Subtype == "init" && e.SessionID != "" {
			if resume != "" && e.SessionID != resume {
				close(s.ready)
				return runtime.DriverSession{}, releasedOpenError(errors.New("native resume identity mismatch"), c)
			}
			var init struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(e.Raw, &init); err != nil || init.Cwd != spec.Cwd {
				close(s.ready)
				return runtime.DriverSession{}, releasedOpenError(errors.New("native init cwd mismatch"), c)
			}
			s.id = e.SessionID
			break
		}
		// Pre-identity content is unsupported, rather than lost or given a fake ID.
		if e.Type == "assistant" || e.Type == "user" || e.Type == "stream_event" || e.Type == "result" {
			if len(early) == 128 {
				close(s.ready)
				return runtime.DriverSession{}, releasedOpenError(errors.New("pre-identity buffer exhausted"), c)
			}
			early = append(early, e)
		}
	}
	if resume == "" {
		if err := d.vault.Save(s.id, a, spec, map[string]string{"id": s.id, "cwd": spec.Cwd}); err != nil {
			close(s.ready)
			return runtime.DriverSession{}, releasedOpenError(err, c)
		}
	}
	d.mu.Lock()
	if d.sessions[s.id] != nil {
		d.mu.Unlock()
		close(s.ready)
		return runtime.DriverSession{}, releasedOpenError(errors.New("duplicate native identity"), c)
	}
	d.sessions[s.id] = s
	d.mu.Unlock()
	close(s.ready)
	go d.observe(s, early)
	return runtime.DriverSession{SessionID: s.id, Capabilities: caps(), Source: s.source}, nil
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
	s.command.Lock()
	defer s.command.Unlock()
	if len(input.Content) != 1 || input.Content[0].Type != "text" {
		return runtime.InputReceipt{}, runtime.Unsupported()
	}
	s.mu.Lock()
	if s.used && !s.active && !s.sending {
		s.mu.Unlock()
		return runtime.InputReceipt{}, runtime.FailedPrecondition("Claude has no native work identity: close/resume required before another input")
	}
	if s.active || s.sending || s.closing || s.err != nil || s.used {
		s.mu.Unlock()
		return runtime.InputReceipt{}, runtime.FailedPrecondition("session unavailable or work unsettled")
	}
	s.active = true
	s.used = true
	s.work = uuid.NewString()
	s.sending = true
	s.mu.Unlock()
	err = s.client.Send(ctx, input.Content[0].Text)
	s.mu.Lock()
	s.sending = false
	s.mu.Unlock()
	if err != nil {
		return runtime.InputReceipt{}, err
	}
	return runtime.InputReceipt{Submission: "sent", Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, nil
}

func (d *Driver) emit(s *session, item string, p events.Payload, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: s.address.Namespace, SessionName: s.address.SessionName, SessionID: s.id, ItemID: item, Time: at}, p, s.source.WithWork(s.work))
	if err != nil {
		return err
	}
	return d.pipe.Send(context.Background(), e)
}

func (d *Driver) state(s *session, state *events.SessionState, reason string) error {
	previous := s.state
	s.state = state
	return d.emit(s, "", events.StateUpdate{PreviousState: previous, State: state, StopReason: reason}, time.Time{})
}

func (d *Driver) observe(s *session, early []sdk.Event) {
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
			if e, ok := event.(sdk.Event); ok {
				err = d.adapt(s, e)
			}
		}
		if err != nil {
			if !s.closing {
				s.err = err
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

func (d *Driver) adapt(s *session, e sdk.Event) error {
	if e.SessionID != "" && e.SessionID != s.id {
		return nil
	} // unproven descendants are not this Session
	var raw struct {
		Time    time.Time `json:"timestamp"`
		Message struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		UUID  string `json:"uuid"`
		Event struct {
			Type    string `json:"type"`
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
			Delta struct{ Type, Text, Thinking string } `json:"delta"`
		} `json:"event"`
	}
	if err := json.Unmarshal(e.Raw, &raw); err != nil {
		return err
	}
	switch e.Type {
	case "stream_event":
		switch raw.Event.Type {
		case "message_start":
			s.messageID = raw.Event.Message.ID
			state := events.Running
			return d.state(s, &state, "")
		case "content_block_delta":
			if s.messageID == "" {
				return errors.New("partial message missing native identity")
			}
			switch raw.Event.Delta.Type {
			case "text_delta":
				return d.emit(s, s.messageID, events.MessageChunk{MessageID: s.messageID, Content: []events.ContentBlock{events.Text(raw.Event.Delta.Text)}}, raw.Time)
			case "thinking_delta":
				return d.emit(s, s.messageID, events.ThinkingChunk{MessageID: s.messageID, Content: []events.ContentBlock{events.Text(raw.Event.Delta.Thinking)}}, raw.Time)
			case "input_json_delta", "signature_delta":
				return nil // unsupported preview, never reconstruct full content
			default:
				return errors.New("unsupported Claude delta type")
			}
		}
	case "assistant", "user":
		id := raw.Message.ID
		if id == "" {
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
		text, thinking := []events.ContentBlock{}, []events.ContentBlock{}
		for _, b := range blocks {
			str := func(key string) string { var v string; _ = json.Unmarshal(b[key], &v); return v }
			switch str("type") {
			case "text":
				var value *string
				if err := json.Unmarshal(b["text"], &value); err != nil || value == nil {
					return errors.New("missing or invalid text")
				}
				text = append(text, events.Text(*value))
			case "thinking":
				var value *string
				if err := json.Unmarshal(b["thinking"], &value); err != nil || value == nil {
					return errors.New("missing or invalid thinking")
				}
				thinking = append(thinking, events.Text(*value))
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
				if e.Type == "user" {
					return d.emit(s, id, events.UserMessage(p), raw.Time)
				}
				return d.emit(s, id, events.AgentMessage(p), raw.Time)
			}
		}
	case "result":
		d.interactions.RemoveSession(s.id)
		s.active = false
		s.messageID = ""
		state := events.Idle
		return d.state(s, &state, e.Subtype)
	}
	return nil
}

func (d *Driver) permission(s *session, p *sdk.PermissionRequest) error {
	request := events.PermissionRequest(p.ToolUseID(), p.ToolName())
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
	ref, err := d.interactions.Add(s.id, s.source.WithWork(s.work), func(ctx context.Context, a map[string]any) error {
		answer := sdk.PermissionAnswer{Behavior: sdk.PermissionAllow, UpdatedInput: p.Input()}
		if request.Kind == "form" {
			if len(a) != 2 || a["action"] != "accept" {
				return runtime.InvalidArgument("expected explicit form accept/content")
			}
			content, ok := a["content"].(map[string]any)
			if !ok || content["answers"] == nil {
				return runtime.InvalidArgument("expected native question answers")
			}
			data, err := json.Marshal(content)
			if err != nil {
				return err
			}
			answer.UpdatedInput = data
		} else {
			allow, err := runtime.PermissionAllowed(a)
			if err != nil {
				return err
			}
			if !allow {
				answer = sdk.PermissionAnswer{Behavior: sdk.PermissionDeny, Message: "Rejected"}
			}
		}
		return p.Answer(ctx, answer)
	})
	if err != nil {
		return err
	}
	state := events.RequiresAction
	if err := d.state(s, &state, ""); err != nil {
		return err
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
	s, err := d.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if (s.active || s.sending) && s.err == nil {
		s.mu.Unlock()
		return runtime.ErrWorkUnsettled
	}
	s.closing = true
	s.mu.Unlock()
	err = s.client.Close()
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
	if err := d.pipe.Flush(ctx); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.sessions, id)
	d.mu.Unlock()
	d.interactions.RemoveSession(id)
	return err
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
	}
	target := s.cancelTarget
	s.mu.Unlock()
	if !active {
		return runtime.CancelReceipt{Result: "noActiveWork"}, nil
	}
	if err := s.client.Interrupt(ctx); err != nil {
		return runtime.CancelReceipt{}, fmt.Errorf("outcome_unknown: interrupt: %w", err)
	}
	return runtime.CancelReceipt{Target: target, Result: "controlAcknowledged"}, nil
}

func releasedOpenError(cause error, c *sdk.Client) error {
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
