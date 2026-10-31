// Package claude converts actual SDK observations directly into SessionEvent.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
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
	if err == nil && len(o.Args) > 0 {
		return o, errors.New("claude SDK does not support additional process args; use an explicit launcher")
	}
	return o, err
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
	attribution              string
	uncertain                bool
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
	s := &session{address: a, source: events.NewSource(), ready: make(chan struct{}), done: make(chan struct{}), tools: map[string]string{}, results: map[string]string{}, submitted: map[string]bool{}, attribution: "unknown"}
	c, err := sdk.Open(ctx, sdk.SessionOptions{ResumeID: resume, CLIPath: d.options.Executable, WorkDir: spec.Cwd, Env: d.options.Environment("claude")})
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
		e := event.Event
		if e == nil {
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
			early = append(early, *e)
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
	// The first input has no prior local work to confuse with. Later inputs carry
	// a caller UUID, not a native TurnID; an echo is optional, never presumed.
	if count > 1 {
		text := input.Content[0].Text
		native = sdk.Input{User: &types.UserInput{UUID: &work, Message: types.InputMessage{Role: "user", Content: types.Content{Text: &text}}}}
	}
	_, err = s.client.Submit(ctx, native)
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

func (d *Driver) emit(s *session, item string, p events.Payload, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	meta := map[string]any{"attribution": s.attribution}
	if s.observationWork != "" {
		meta["submission"] = s.observationWork
	}
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

func (d *Driver) observe(s *session, early []sdk.Event) {
	defer close(s.done)
	for _, event := range early {
		s.mu.Lock()
		err := d.adapt(s, event, false)
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
			case sdk.ReceivedNative:
				if event.Event != nil {
					err = d.adapt(s, *event.Event, event.Parent != nil)
				}
			case sdk.ReceivedRequest:
				if event.Request != nil && !s.closing && event.Request.Context().Err() == nil {
					s.observationWork, s.attribution = "", "unknown"
					if s.submissions == 1 {
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

func (d *Driver) adapt(s *session, e sdk.Event, child bool) error {
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
		} else if s.submissions == 1 && s.work != "" {
			s.observationWork, s.attribution = s.work, "single_submission"
		}
		// Conflicting native identities are not evidence for either submission.
		if previous, ok := s.results[e.UUID]; e.Type == "result" && ok && previous != s.observationWork {
			s.observationWork, s.attribution = "", "unknown"
		}
	}
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
			if !root || s.observationWork == "" || s.observationWork != s.work || !s.active {
				return nil
			}
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
	d.interactions.RemoveSession(id)
	err = s.client.Close()
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
	if drain := d.pipe.Flush(ctx); drain != nil {
		return errors.Join(err, drain)
	}
	if cause := s.client.Err(); cause != nil {
		return errors.Join(err, fmt.Errorf("native observation terminated: %w", cause))
	}
	s.mu.Lock()
	cause := s.err
	s.mu.Unlock()
	if cause != nil {
		return errors.Join(err, fmt.Errorf("observation terminated: %w", cause))
	}
	if err != nil {
		return fmt.Errorf("native cleanup failed: %w", err)
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
