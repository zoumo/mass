// Package codex adapts native SDK observations directly into SessionEvent.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	sdk "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/recovery"
)

type options = runtime.ProcessConfig

func parse(c runtime.Config) (options, error) {
	o, err := runtime.ParseProcessConfig(c)
	return o, err
}

type Factory struct{ RecoveryDir string }

func (Factory) Validate(c runtime.Config) error { _, err := parse(c); return err }
func (f Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	o, err := parse(c)
	if err != nil {
		return nil, err
	}
	return &Driver{vault: recovery.Vault{Dir: f.RecoveryDir, Kind: "codex", Owner: name, Executable: o.Executable}, options: o, pipe: events.NewEventPipe(128), sessions: map[string]*session{}}, nil
}

type session struct {
	command         sync.Mutex
	native          *sdk.Session
	closing         bool
	work            string
	cancelWork      string
	cancelTarget    string
	cancelAttempted bool
	cancelErr       error
	retired         map[string]bool
	address         runtime.Address
	source          events.Source
	active          bool
	sending         bool
	state           *events.SessionState
	err             error
	done            chan struct{}
	released        chan struct{}
	releaseOnce     sync.Once
}
type Driver struct {
	opening      int
	loads        map[string]*nativeLoad
	vault        recovery.Vault
	mu           sync.Mutex
	op           sync.Mutex
	options      options
	pipe         *events.EventPipe
	taken        bool
	client       *sdk.Client
	sessions     map[string]*session
	stopped      bool
	stopping     bool
	life         context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	interactions runtime.PendingInteractions
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

func caps() runtime.Capabilities {
	return runtime.Capabilities{"textInput": true, "permission": true, "form": true}
}

var supportedSessionFeatures = runtime.FeatureSupport{AgentQuestions: true}

func threadConfig(features runtime.SessionFeatures) ct.Optional[map[string]json.RawMessage] {
	if features.AgentQuestionsEnabled() {
		return ct.Optional[map[string]json.RawMessage]{}
	}
	return ct.Some(map[string]json.RawMessage{
		"tools.experimental_request_user_input.enabled": json.RawMessage("false"),
	})
}

func (d *Driver) Start(ctx context.Context) (runtime.Capabilities, error) {
	d.op.Lock()
	defer d.op.Unlock()
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		return nil, errors.New("driver stopping")
	}
	if d.client != nil {
		d.mu.Unlock()
		return caps(), nil
	}
	if d.stopped {
		d.mu.Unlock()
		return nil, errors.New("driver stopped")
	}
	d.mu.Unlock()
	life, cancel := context.WithCancel(context.Background())
	args := d.options.Args
	if args == nil {
		args = []string{"app-server"}
	}
	c, err := sdk.NewStdio(ctx, sdk.StdioOptions{Executable: d.options.Executable, Args: args, Env: d.options.Environment("codex"), Initialize: ct.InitializeParams{ClientInfo: ct.ClientInfo{Name: "mass", Version: "2"}}})
	if err != nil {
		cancel()
		return nil, err
	}
	d.mu.Lock()
	d.client = c
	d.life = life
	d.cancel = cancel
	d.mu.Unlock()
	return caps(), nil
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
	// Once dispatched on the shared connection, retain the original turn response.
	// Owner Stop waits for this same open; it must not lose the receipt or resend.
	receipt, err := d.SendPrompt(context.WithoutCancel(ctx), native.SessionID, input)
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
	// Retain the original shared dispatch response, including an early terminal.
	receipt, err := d.SendPrompt(context.WithoutCancel(ctx), native.SessionID, input)
	if err != nil {
		if !errors.Is(err, runtime.ErrFailedPrecondition) && !errors.Is(err, runtime.ErrInvalidArgument) {
			err = errors.Join(&runtime.OperationError{Code: "outcome_unknown", Reason: "resume_input_unverified", Message: "New input dispatch was not verified; do not resend"}, err)
		}
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
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.opening--; d.mu.Unlock() }()
	if err := d.vault.Check(); err != nil {
		return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
	}
	if !filepath.IsAbs(spec.Cwd) {
		return runtime.DriverSession{}, noResources("absolute cwd required")
	}
	if len(spec.AdditionalDirs) > 0 {
		return runtime.DriverSession{}, noResources("additionalDirs not supported yet")
	}
	effective := runtime.ResolveSessionFeatures(spec.Features, supportedSessionFeatures)
	d.mu.Lock()
	c := d.client
	stopped := d.stopped
	d.mu.Unlock()
	if c == nil || stopped {
		return runtime.DriverSession{}, noResources("driver not running")
	}
	var thread ct.Thread
	var native *sdk.Session
	var err error
	keep := false
	defer func() {
		if native != nil && !keep {
			_ = native.Close()
		}
	}()
	if resume != "" {
		material, err := d.vault.Load(resume, address, spec)
		if err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		var saved ct.Thread
		if err := json.Unmarshal(material.Context, &saved); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if saved.ID != resume || saved.SessionID == "" || saved.Ephemeral || saved.Cwd != spec.Cwd {
			return runtime.DriverSession{}, noResources("invalid native recovery context")
		}
		native, err = sdk.Open(ctx, sdk.SessionOptions{Client: c, Resume: &ct.ThreadResumeParams{ThreadID: resume, ExcludeTurns: ct.Some(true), Config: threadConfig(effective)}})
		if err != nil {
			return runtime.DriverSession{}, err
		}
		response, _ := native.ResumeResponse()
		thread = response.Thread
		if thread.ID != saved.ID || thread.SessionID != saved.SessionID || thread.Cwd != saved.Cwd || thread.CreatedAt != saved.CreatedAt || thread.ModelProvider != saved.ModelProvider || thread.Ephemeral || !reflect.DeepEqual(thread.Path, saved.Path) {
			return runtime.DriverSession{}, errors.New("native recovery ownership mismatch")
		}
		// Resume metadata may contain old turns: they are never adapted or replayed.
	} else {
		params := ct.ThreadStartParams{Cwd: ct.Some(spec.Cwd), Ephemeral: ct.Some(false), Config: threadConfig(effective)}
		if spec.SystemPrompt != "" {
			params.BaseInstructions = ct.Some(spec.SystemPrompt)
		}
		if err := ctx.Err(); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		// A dispatched thread/start on the shared connection must retain its
		// original response: canceling the wait can lose the only native identity.
		// Stop still owns this opening and waits for its late identity cleanup.
		native, err = sdk.Open(context.WithoutCancel(ctx), sdk.SessionOptions{Client: c, Thread: params})
		if err != nil {
			return runtime.DriverSession{}, err
		}
		response, _ := native.StartResponse()
		thread = response.Thread
	}
	id := thread.ID
	if id == "" || thread.SessionID == "" || thread.Ephemeral || thread.Cwd != spec.Cwd {
		return runtime.DriverSession{}, errors.New("outcome_unknown: missing or mismatched native identity/context")
	}
	if resume == "" {
		if err := d.vault.Save(id, address, spec, thread); err != nil {
			return runtime.DriverSession{}, fmt.Errorf("outcome_unknown: save recovery material: %w", err)
		}
	}
	s := &session{native: native, address: address, source: events.NewSource(), done: make(chan struct{}), released: make(chan struct{}), retired: map[string]bool{}}
	d.mu.Lock()
	if _, ok := d.sessions[id]; ok {
		d.mu.Unlock()
		return runtime.DriverSession{}, errors.Join(errors.New("duplicate native identity"), native.Close())
	}
	d.sessions[id] = s
	d.mu.Unlock()
	// Only the native response's idle observation permits an initial idle event.
	if thread.Status.Idle != nil {
		state := events.Idle
		d.mu.Lock()
		s.state = &state
		d.mu.Unlock()
		if err := d.emit(s, id, "", events.StateUpdate{State: &state}, ""); err != nil {
			return runtime.DriverSession{}, err
		}
	}
	keep = true
	d.wg.Add(1)
	go d.observe(id, s)
	return runtime.DriverSession{SessionID: id, Capabilities: caps(), EffectiveFeatures: effective, Source: s.source}, nil
}

func (d *Driver) SendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	if err := runtime.ValidateTextInput(input); err != nil {
		return runtime.InputReceipt{}, err
	}
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.InputReceipt{}, runtime.FailedPrecondition("session unavailable")
	}
	s.command.Lock()
	defer s.command.Unlock()
	d.mu.Lock()
	if d.sessions[id] != s || d.stopped || d.stopping || s.closing || s.active || s.sending || s.err != nil || len(s.retired) >= 128 {
		d.mu.Unlock()
		return runtime.InputReceipt{}, runtime.FailedPrecondition("session unavailable or work unsettled")
	}
	s.sending = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		s.sending = false
		d.mu.Unlock()
	}()
	items := make([]ct.UserInput, 0, len(input.Content))
	for _, b := range input.Content {
		items = append(items, ct.UserInput{Text: &ct.TextUserInput{Type: "text", Text: b.Text, TextElements: ct.Some([]ct.TextElement{})}})
	}
	d.mu.Lock()
	s.active = true // reserve unsettled work before dispatch; receipt alone is not running evidence
	d.mu.Unlock()
	submission, err := s.native.Submit(ctx, sdk.Input{Input: items})
	result := submission.Response
	if err != nil {
		return runtime.InputReceipt{Acceptance: runtime.Acceptance{}}, fmt.Errorf("outcome_unknown: start turn: %w", err)
	}
	if result.Turn.ID == "" {
		return runtime.InputReceipt{}, errors.New("outcome_unknown: missing turn identity")
	}
	d.mu.Lock()
	if s.work == "" && !s.retired[result.Turn.ID] {
		s.work = result.Turn.ID
	}
	d.mu.Unlock()
	return runtime.InputReceipt{Submission: result.Turn.ID, Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}}, nil
}

func (d *Driver) emit(s *session, id, item string, p events.Payload, work string, observedAt ...time.Time) error {
	return d.emitWithMeta(s, id, item, p, work, nil, observedAt...)
}

func (d *Driver) emitWithMeta(s *session, id, item string, p events.Payload, work string, meta map[string]any, observedAt ...time.Time) error {
	eventTime := time.Now().UTC()
	if len(observedAt) > 0 && !observedAt[0].IsZero() {
		eventTime = observedAt[0]
	}
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: s.address.Namespace, SessionName: s.address.SessionName, SessionID: id, ItemID: item, Time: eventTime, Meta: meta}, p, s.source.WithWork(work))
	if err != nil {
		return err
	}
	return d.pipe.Send(d.life, e)
}

func (d *Driver) observe(id string, s *session) {
	defer d.wg.Done()
	defer close(s.done)
	for {
		received, err := s.native.Receive(d.life)
		if err != nil {
			d.mu.Lock()
			closing := s.closing
			d.mu.Unlock()
			if !closing || !errors.Is(err, io.EOF) {
				d.observationFailed(id, s, err)
			}
			return
		}
		switch received.Kind {
		case sdk.ReceivedEvent:
			if received.Event != nil {
				if err := d.adapt(id, s, *received.Event); err != nil {
					d.observationFailed(id, s, err)
					return
				}
			}
		case sdk.ReceivedRequest:
			if received.Request != nil {
				d.handleRequest(s, *received.Request)
			}
		}
	}
}

func (d *Driver) adapt(id string, s *session, event ct.Event) error {
	if event.DecodeError != nil {
		return fmt.Errorf("invalid native observation: %w", event.DecodeError)
	}
	if event.Method == "thread/closed" {
		var v struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(event.Params, &v); err != nil {
			return err
		}
		if v.ThreadID != id {
			return errors.New("release identity mismatch")
		}
		s.releaseOnce.Do(func() { close(s.released) })
		return nil
	}
	// SDK observation diagnostics are not native session facts.
	if event.Method == "" {
		return nil
	}
	var v struct {
		CompletedAtMs *int64    `json:"completedAtMs"`
		StartedAtMs   *int64    `json:"startedAtMs"`
		Time          time.Time `json:"time"`
		ThreadID      string    `json:"threadId"`
		TurnID        string    `json:"turnId"`
		ItemID        string    `json:"itemId"`
		Delta         string    `json:"delta"`
		Turn          struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
		Item struct {
			ID               string          `json:"id"`
			Type             string          `json:"type"`
			Text             string          `json:"text"`
			Content          json.RawMessage `json:"content"`
			Summary          []string        `json:"summary"`
			Command          string          `json:"command"`
			Status           string          `json:"status"`
			Cwd              string          `json:"cwd"`
			Phase            string          `json:"phase"`
			ExitCode         *int32          `json:"exitCode"`
			DurationMs       *int64          `json:"durationMs"`
			AggregatedOutput json.RawMessage `json:"aggregatedOutput"`
		} `json:"item"`
	}
	if err := json.Unmarshal(event.Params, &v); err != nil {
		return err
	}
	if v.ThreadID != id {
		return nil
	} // descendants require later explicit association support
	if v.CompletedAtMs != nil {
		v.Time = time.UnixMilli(*v.CompletedAtMs).UTC()
	} else if v.StartedAtMs != nil {
		v.Time = time.UnixMilli(*v.StartedAtMs).UTC()
	}
	if v.TurnID != "" {
		d.mu.Lock()
		owned := s.work == v.TurnID || s.retired[v.TurnID]
		d.mu.Unlock()
		if !owned {
			return nil
		}
	}
	emit := func(item string, p events.Payload, work string) error { return d.emit(s, id, item, p, work, v.Time) }
	switch event.Method {
	case "turn/started", "turn/completed":
		if v.Turn.ID == "" {
			return errors.New("native turn event missing identity")
		}
		state := events.Running
		if event.Method == "turn/completed" {
			state = events.Idle
		}
		d.mu.Lock()
		if s.retired[v.Turn.ID] || (!s.active && s.work == "") || (s.work != "" && s.work != v.Turn.ID) {
			d.mu.Unlock()
			return nil
		}
		if s.work == "" {
			s.work = v.Turn.ID
		}
		previous := s.state
		s.state = &state
		s.active = state == events.Running
		if !s.active {
			s.retired[v.Turn.ID] = true
			s.work = ""
		}
		d.mu.Unlock()
		if state == events.Idle {
			d.interactions.RemoveSession(id)
		}
		return emit("", events.StateUpdate{PreviousState: previous, State: &state, StopReason: v.Turn.Status}, v.Turn.ID)
	case "item/agentMessage/delta":
		return emit(v.ItemID, events.MessageChunk{MessageID: v.ItemID, Content: []events.ContentBlock{events.Text(v.Delta)}}, v.TurnID)
	case "item/reasoning/summaryTextDelta":
		return emit(v.ItemID, events.ThinkingChunk{MessageID: v.ItemID, Content: []events.ContentBlock{events.Text(v.Delta)}}, v.TurnID)
	case "item/started", "item/completed":
		item := v.Item
		if item.ID == "" {
			return errors.New("native item missing identity")
		}
		switch item.Type {
		case "agentMessage":
			if event.Method == "item/completed" {
				return emit(item.ID, events.AgentMessage{MessageID: item.ID, Phase: item.Phase, Content: []events.ContentBlock{events.Text(item.Text)}}, v.TurnID)
			}
		case "userMessage":
			if event.Method == "item/completed" {
				var native []ct.UserInput
				if err := json.Unmarshal(item.Content, &native); err != nil {
					return err
				}
				if native == nil {
					return errors.New("missing native user content")
				}
				content := make([]events.ContentBlock, 0, len(native))
				for _, block := range native {
					if block.Text == nil {
						return errors.New("unsupported native user content")
					}
					content = append(content, events.Text(block.Text.Text))
				}
				// Native spans and extensions are provenance, not MASS content fields.
				decoder := json.NewDecoder(bytes.NewReader(event.Params))
				decoder.UseNumber()
				var raw map[string]any
				if err := decoder.Decode(&raw); err != nil {
					return err
				}
				return d.emitWithMeta(s, id, item.ID, events.UserMessage{MessageID: item.ID, Content: content}, v.TurnID, map[string]any{"native": raw}, v.Time)
			}
		case "reasoning":
			if event.Method == "item/completed" {
				content := []events.ContentBlock{}
				for _, text := range item.Summary {
					content = append(content, events.Text(text))
				}
				return emit(item.ID, events.AgentThinking{MessageID: item.ID, Content: content}, v.TurnID)
			}
		case "commandExecution":
			if event.Method == "item/started" {
				return emit(item.ID, events.ToolCall{ID: item.ID, Kind: "execute", RawInput: map[string]any{"command": item.Command, "cwd": item.Cwd}}, v.TurnID)
			}
			status := "unknown"
			if item.Status == "completed" || item.Status == "failed" || item.Status == "declined" {
				status = item.Status
			}
			var duration *float64
			if item.DurationMs != nil {
				value := float64(*item.DurationMs)
				duration = &value
			}
			p := events.ToolResult{ExitCode: item.ExitCode, DurationMs: duration, ID: item.ID, Kind: "execute", Status: status, Output: &events.Output{Availability: "unavailable", Completeness: "unknown"}}
			if item.AggregatedOutput != nil {
				var value any
				dec := json.NewDecoder(bytes.NewReader(item.AggregatedOutput))
				dec.UseNumber()
				if err := dec.Decode(&value); err != nil {
					return err
				}
				p.RawOutput = &value
				if text, ok := value.(string); ok {
					p.Content = []events.ContentBlock{events.Text(text)}
				}
				p.Output = &events.Output{Availability: "provided", Completeness: "retained"}
			}
			return emit(item.ID, p, v.TurnID)
		}
	}
	return nil
}

func (d *Driver) CloseSession(_ context.Context, id string) error {
	d.mu.Lock()
	s := d.sessions[id]
	if s == nil {
		d.mu.Unlock()
		return nil
	}
	s.closing = true
	d.mu.Unlock()
	d.interactions.RemoveSession(id)
	if err := s.native.Close(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.sessions[id] == s {
		delete(d.sessions, id)
	}
	d.interactions.RemoveSession(id)
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
	c := d.client
	d.stopping = true
	d.mu.Unlock()
	if c != nil {
		if err := c.Close(); err != nil {
			return err
		}
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()
	d.pipe.End(nil)
	return nil
}

func (d *Driver) handleRequest(s *session, r sdk.IncomingRequest) {
	var v struct{ ThreadID, TurnID, ItemID string }
	if json.Unmarshal(r.Params(), &v) != nil {
		return
	}
	d.mu.Lock()
	// Requests may precede the turn/start response; retain their native identity.
	if d.sessions[v.ThreadID] == s && s.sending && s.work == "" && v.TurnID != "" && !s.retired[v.TurnID] {
		s.work = v.TurnID
	}
	valid := s != nil && !s.closing && d.sessions[v.ThreadID] == s && s.err == nil && r.Handle().Err() == nil && r.Context().Err() == nil && v.TurnID != "" && s.work == v.TurnID && !s.retired[v.TurnID]
	d.mu.Unlock()
	if !valid {
		return
	}
	request := events.PermissionRequest(v.ItemID, r.Method())
	request.Content = []events.ContentBlock{events.Text(string(r.Params()))}
	method := r.Method()
	decisions := map[string]string{"allow-once": "accept", "reject-once": "decline"}
	var commandChoices map[string]commandApprovalChoice
	switch method {
	case "item/commandExecution/requestApproval":
		var supported bool
		request.Options, commandChoices, supported = commandApprovalChoices(r.Params())
		if !supported {
			_ = d.emit(s, v.ThreadID, "", events.SessionError{Message: "unsupported native permission options", Reason: "unsupported_native_interaction"}, v.TurnID)
			return
		}
	case "item/fileChange/requestApproval":
	case "item/tool/requestUserInput":
		var schema map[string]any
		decoder := json.NewDecoder(bytes.NewReader(r.Params()))
		decoder.UseNumber()
		if decoder.Decode(&schema) != nil {
			return
		}
		request = events.InteractionRequest{Kind: "form", Message: "Answer native Codex questions", RequestedSchema: schema}
	default:
		_ = d.emit(s, v.ThreadID, "", events.SessionError{Message: "unsupported native interaction", Reason: "unsupported_native_interaction"}, v.TurnID)
		return
	}
	live := func() bool { return r.Handle().Err() == nil && r.Context().Err() == nil }
	unsent := func(err error) bool {
		var native *sdk.AnswerError
		if errors.As(err, &native) {
			return native.Dispatch == sdk.NotSent
		}
		return errors.Is(err, runtime.ErrInvalidArgument)
	}
	ref, err := d.interactions.Add(v.ThreadID, s.source.WithWork(v.TurnID), live, unsent, func(ctx context.Context, a map[string]any) error {
		if method == "item/tool/requestUserInput" {
			if len(a) != 2 || a["action"] != "accept" {
				return runtime.InvalidArgument("expected explicit form accept/content")
			}
			content, ok := a["content"].(map[string]any)
			if !ok || content["answers"] == nil {
				return runtime.InvalidArgument("expected native question answers")
			}
			raw, err := json.Marshal(content)
			if err != nil {
				return runtime.InvalidArgument("invalid native question answers")
			}
			var response ct.ToolRequestUserInputResponse
			var params struct {
				Questions []ct.ToolRequestUserInputQuestion `json:"questions"`
			}
			if json.Unmarshal(raw, &response) != nil || json.Unmarshal(r.Params(), &params) != nil || response.Answers == nil {
				return runtime.InvalidArgument("invalid native question answers")
			}
			for id, value := range response.Answers {
				found := false
				for _, question := range params.Questions {
					if question.ID == id {
						found = true
						break
					}
				}
				if !found || value.Answers == nil {
					return runtime.InvalidArgument("answers must use native question IDs and string arrays")
				}
			}
			return r.Answer(ctx, response)
		} else {
			option, ok := a["optionId"].(string)
			decision, offered := decisions[option]
			choice, commandOffered := commandChoices[option]
			if method == "item/commandExecution/requestApproval" {
				offered = commandOffered
			}
			if len(a) != 2 || a["outcome"] != "selected" || !ok || !offered {
				return runtime.InvalidArgument("expected a native offered permission option")
			}
			options := sdk.ApprovalOptions{}
			if method == "item/commandExecution/requestApproval" {
				options.CommandExecution = &ct.CommandExecutionRequestApprovalResponse{Decision: choice.decision}
				if choice.approve {
					return r.Approve(ctx, options)
				}
				return r.Deny(ctx, options)
			}
			if decision == "accept" || decision == "acceptForSession" {
				return r.Approve(ctx, options)
			}
			return r.Deny(ctx, options)
		}
	})
	if err == nil {
		state := events.RequiresAction
		d.mu.Lock()
		previous := s.state
		s.state = &state
		d.mu.Unlock()
		err = d.emit(s, v.ThreadID, "", events.StateUpdate{PreviousState: previous, State: &state}, v.TurnID)
	}
	if err == nil {
		err = d.emit(s, v.ThreadID, "", events.InteractionRequested{Interaction: ref, Request: request, Blocking: true}, v.TurnID)
	}
	if err != nil {
		_ = d.emit(s, v.ThreadID, "", events.SessionError{Message: "interaction delivery failed"}, v.TurnID)
	}
}

func (d *Driver) AnswerInteraction(ctx context.Context, id, ref string, answer any) (runtime.Acceptance, error) {
	a, ok := answer.(map[string]any)
	if !ok || a == nil {
		return runtime.Acceptance{}, runtime.InvalidArgument("interaction requires an object answer")
	}
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.Acceptance{}, runtime.FailedPrecondition("session unavailable")
	}
	s.command.Lock()
	defer s.command.Unlock()
	d.mu.Lock()
	source := s.source.WithWork(s.work)
	valid := d.sessions[id] == s && !d.stopping && !d.stopped && s.err == nil
	d.mu.Unlock()
	if !valid {
		return runtime.Acceptance{}, errors.New("original interaction run unavailable")
	}
	if err := d.interactions.Answer(ctx, id, source, ref, a); err != nil {
		return runtime.Acceptance{}, err
	}
	// SDK Answer proves a write, not native acceptance or tool success.
	return runtime.Acceptance{Sent: runtime.Yes()}, nil
}

func (d *Driver) observationFailed(id string, s *session, err error) {
	d.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	previous := s.state
	s.state = nil
	d.mu.Unlock()
	_ = d.emit(s, id, "", events.SessionError{Message: "Codex session observation failed"}, "")
	_ = d.emit(s, id, "", events.StateUpdate{PreviousState: previous}, "")
}

func (d *Driver) Cancel(ctx context.Context, id string) (runtime.CancelReceipt, error) {
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.CancelReceipt{}, runtime.FailedPrecondition("session unavailable")
	}
	s.command.Lock()
	defer s.command.Unlock()
	d.mu.Lock()
	work := s.work
	active := s.active
	if active && s.cancelWork != work {
		s.cancelWork, s.cancelTarget = work, uuid.NewString()
		s.cancelAttempted, s.cancelErr = false, nil
	}
	target := s.cancelTarget
	attempted, previousErr := s.cancelAttempted, s.cancelErr
	d.mu.Unlock()
	if !active {
		return runtime.CancelReceipt{Result: "noActiveWork"}, nil
	}
	if work == "" {
		return runtime.CancelReceipt{}, errors.New("outcome_unknown: no confirmed native turn to cancel")
	}
	if attempted {
		if previousErr != nil {
			return runtime.CancelReceipt{}, previousErr
		}
		return runtime.CancelReceipt{Target: target, Result: "controlAcknowledged"}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtime.CancelReceipt{}, fmt.Errorf("not_sent: cancel: %w", err)
	}
	err := s.native.Interrupt(ctx, sdk.InterruptOptions{TurnID: work})
	var call *sdk.CallError
	notSent := errors.As(err, &call) && call.Dispatch == sdk.NotSent
	if err != nil {
		var rejected *sdk.RPCError
		switch {
		case notSent:
			err = fmt.Errorf("not_sent: cancel: %w", err)
		case errors.As(err, &rejected):
			err = fmt.Errorf("native_rejected: cancel: %w", err)
		default:
			err = fmt.Errorf("outcome_unknown: cancel: %w", err)
		}
	}
	// Only a proven non-dispatch permits a later explicit attempt for this work.
	d.mu.Lock()
	s.cancelAttempted, s.cancelErr = !notSent, err
	d.mu.Unlock()
	if err != nil {
		return runtime.CancelReceipt{}, err
	}
	return runtime.CancelReceipt{Target: target, Result: "controlAcknowledged"}, nil
}

func noResources(message string) error {
	return errors.Join(runtime.ErrResourcesReleased, errors.New(message))
}
