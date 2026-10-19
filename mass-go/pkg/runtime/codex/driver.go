// Package codex adapts native SDK observations directly into SessionEvent.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/zoumo/agent-sdk-go/codex"
	ct "github.com/zoumo/agent-sdk-go/codex/types"
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
			return o, fmt.Errorf("unknown Codex config %q", k)
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
	return &Driver{vault: recovery.Vault{Dir: f.RecoveryDir, Kind: "codex", Owner: name, Executable: o.executable}, options: o, pipe: runtime.NewEventPipe(128), sessions: map[string]*session{}}, nil
}

type session struct {
	command     sync.Mutex
	work        string
	retired     map[string]bool
	address     runtime.Address
	source      runtime.Source
	sub         *sdk.Subscription
	active      bool
	sending     bool
	state       *runtime.SessionState
	err         error
	done        chan struct{}
	released    chan struct{}
	releaseOnce sync.Once
}
type Driver struct {
	opening      int
	vault        recovery.Vault
	mu           sync.Mutex
	op           sync.Mutex
	options      options
	pipe         *runtime.EventPipe
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

func (d *Driver) Events() (runtime.Stream, error) {
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
	c, err := sdk.NewStdio(ctx, sdk.StdioOptions{Executable: d.options.executable, Args: []string{"app-server"}, Env: []string{"CODEX_MOCK_SCENARIO=" + d.options.scenario}, Initialize: ct.InitializeParams{ClientInfo: ct.ClientInfo{Name: "mass", Version: "2"}}})
	if err != nil {
		cancel()
		return nil, err
	}
	d.mu.Lock()
	d.client = c
	d.life = life
	d.cancel = cancel
	d.mu.Unlock()
	d.wg.Add(1)
	go d.requests(c)
	return caps(), nil
}
func (d *Driver) OpenSession(ctx context.Context, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, "", address, spec)
}
func (d *Driver) ResumeSession(ctx context.Context, id string, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.open(ctx, id, address, spec)
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
	d.mu.Lock()
	c := d.client
	stopped := d.stopped
	d.mu.Unlock()
	if c == nil || stopped {
		return runtime.DriverSession{}, noResources("driver not running")
	}
	var thread ct.Thread
	if resume != "" {
		material, err := d.vault.Load(resume, address, spec)
		if err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		var saved ct.Thread
		if err = json.Unmarshal(material.Context, &saved); err != nil {
			return runtime.DriverSession{}, errors.Join(runtime.ErrResourcesReleased, err)
		}
		if saved.ID != resume || saved.SessionID != resume || saved.Ephemeral || string(saved.Cwd) != spec.Cwd {
			return runtime.DriverSession{}, noResources("invalid native recovery context")
		}
		response, err := c.ResumeThread(ctx, ct.ThreadResumeParams{ThreadID: resume, ExcludeTurns: ct.Some(true)})
		if err != nil {
			return runtime.DriverSession{}, err
		}
		thread = response.Thread
		if thread.ID != saved.ID || thread.SessionID != saved.SessionID || thread.Cwd != saved.Cwd || thread.CreatedAt != saved.CreatedAt || thread.ModelProvider != saved.ModelProvider || thread.Ephemeral || !bytes.Equal(marshal(thread.Path), marshal(saved.Path)) {
			return runtime.DriverSession{}, errors.New("native recovery ownership mismatch")
		}
		// Resume metadata may contain old turns: they are never adapted or replayed.
	} else {
		params := ct.ThreadStartParams{Cwd: ct.Some(spec.Cwd), Ephemeral: ct.Some(false)}
		if spec.SystemPrompt != "" {
			params.BaseInstructions = ct.Some(spec.SystemPrompt)
		}
		response, err := c.StartThread(ctx, params)
		if err != nil {
			return runtime.DriverSession{}, err
		}
		thread = response.Thread
	}
	id := thread.ID
	if id == "" || thread.SessionID != id || thread.Ephemeral || string(thread.Cwd) != spec.Cwd {
		return runtime.DriverSession{}, errors.New("outcome_unknown: missing or mismatched native identity/context")
	}
	if resume == "" {
		if err := d.vault.Save(id, address, spec, thread); err != nil {
			return runtime.DriverSession{}, fmt.Errorf("outcome_unknown: save recovery material: %w", err)
		}
	}
	sub, err := c.Subscribe(d.life, id, ct.SubscriptionOptions{EventQueue: 128})
	if err != nil {
		return runtime.DriverSession{}, err
	}
	s := &session{address: address, source: runtime.NewSource(), sub: sub, done: make(chan struct{}), released: make(chan struct{}), retired: map[string]bool{}}
	d.mu.Lock()
	if _, ok := d.sessions[id]; ok {
		d.mu.Unlock()
		sub.Abort()
		return runtime.DriverSession{}, errors.New("duplicate native identity")
	}
	d.sessions[id] = s
	d.mu.Unlock()
	// Only the native response's idle observation permits an initial idle event.
	if thread.Status.Idle != nil {
		state := runtime.Idle
		d.mu.Lock()
		s.state = &state
		d.mu.Unlock()
		if err = d.emit(s, id, "", runtime.StateUpdate{State: &state}, ""); err != nil {
			return runtime.DriverSession{}, err
		}
	}
	d.wg.Add(1)
	go d.observe(id, s)
	return runtime.DriverSession{SessionID: id, Capabilities: caps(), Source: s.source}, nil
}
func (d *Driver) SendPrompt(ctx context.Context, id string, input runtime.Input) (runtime.InputReceipt, error) {
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.InputReceipt{}, errors.New("session unavailable")
	}
	s.command.Lock()
	defer s.command.Unlock()
	d.mu.Lock()
	c := d.client
	if d.sessions[id] != s || d.stopped || d.stopping || s.active || s.sending || s.err != nil || len(s.retired) >= 128 {
		d.mu.Unlock()
		return runtime.InputReceipt{}, errors.New("session unavailable or work unsettled")
	}
	s.sending = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); s.sending = false; d.mu.Unlock() }()
	if len(input.Content) == 0 {
		return runtime.InputReceipt{}, errors.New("empty input")
	}
	items := make([]ct.UserInput, 0, len(input.Content))
	for _, b := range input.Content {
		if b.Type != "text" {
			return runtime.InputReceipt{}, errors.New("unsupported input content")
		}
		items = append(items, ct.UserInput{Text: &ct.TextUserInput{Type: "text", Text: b.Text, TextElements: ct.Some([]ct.TextElement{})}})
	}
	d.mu.Lock()
	s.active = true // reserve unsettled work before dispatch; receipt alone is not running evidence
	d.mu.Unlock()
	result, err := c.StartTurn(ctx, ct.TurnStartParams{ThreadID: id, Input: items})
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
func (d *Driver) emit(s *session, id, item string, p runtime.Payload, work string, observedAt ...time.Time) error {
	eventTime := time.Now().UTC()
	if len(observedAt) > 0 && !observedAt[0].IsZero() {
		eventTime = observedAt[0]
	}
	e, err := runtime.NewEvent(runtime.EventBase{EventID: runtime.UUID(), Namespace: s.address.Namespace, SessionName: s.address.SessionName, SessionID: id, ItemID: item, Time: eventTime}, p, s.source.WithWork(work))
	if err != nil {
		return err
	}
	return d.pipe.Send(d.life, e)
}
func (d *Driver) observe(id string, s *session) {
	defer d.wg.Done()
	defer close(s.done)
	for event := range s.sub.Events() {
		if err := d.adapt(id, s, event); err != nil {
			d.observationFailed(id, s, err)
			return
		}
	}
	if err := s.sub.Err(); err != nil {
		d.observationFailed(id, s, err)
	} else {
		select {
		case <-s.released:
		default:
			d.observationFailed(id, s, errors.New("unexpected native EOF"))
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
			ID               string                 `json:"id"`
			Type             string                 `json:"type"`
			Text             string                 `json:"text"`
			Content          []runtime.ContentBlock `json:"content"`
			Summary          []string               `json:"summary"`
			Command          string                 `json:"command"`
			Status           string                 `json:"status"`
			Cwd              string                 `json:"cwd"`
			Phase            string                 `json:"phase"`
			ExitCode         *int32                 `json:"exitCode"`
			DurationMs       *int64                 `json:"durationMs"`
			AggregatedOutput json.RawMessage        `json:"aggregatedOutput"`
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
	emit := func(item string, p runtime.Payload, work string) error { return d.emit(s, id, item, p, work, v.Time) }
	switch event.Method {
	case "turn/started", "turn/completed":
		if v.Turn.ID == "" {
			return errors.New("native turn event missing identity")
		}
		state := runtime.Running
		if event.Method == "turn/completed" {
			state = runtime.Idle
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
		s.active = state == runtime.Running
		if !s.active {
			s.retired[v.Turn.ID] = true
			s.work = ""
		}
		d.mu.Unlock()
		if state == runtime.Idle {
			d.interactions.RemoveSession(id)
		}
		return emit("", runtime.StateUpdate{PreviousState: previous, State: &state, StopReason: v.Turn.Status}, v.Turn.ID)
	case "item/agentMessage/delta":
		return emit(v.ItemID, runtime.MessageChunk{MessageID: v.ItemID, Content: []runtime.ContentBlock{runtime.Text(v.Delta)}}, v.TurnID)
	case "item/reasoning/summaryTextDelta":
		return emit(v.ItemID, runtime.ThinkingChunk{MessageID: v.ItemID, Content: []runtime.ContentBlock{runtime.Text(v.Delta)}}, v.TurnID)
	case "item/started", "item/completed":
		item := v.Item
		if item.ID == "" {
			return errors.New("native item missing identity")
		}
		switch item.Type {
		case "agentMessage":
			if event.Method == "item/completed" {
				return emit(item.ID, runtime.AgentMessage{MessageID: item.ID, Phase: item.Phase, Content: []runtime.ContentBlock{runtime.Text(item.Text)}}, v.TurnID)
			}
		case "userMessage":
			if event.Method == "item/completed" {
				return emit(item.ID, runtime.UserMessage{MessageID: item.ID, Content: item.Content}, v.TurnID)
			}
		case "reasoning":
			if event.Method == "item/completed" {
				content := []runtime.ContentBlock{}
				for _, text := range item.Summary {
					content = append(content, runtime.Text(text))
				}
				return emit(item.ID, runtime.AgentThinking{MessageID: item.ID, Content: content}, v.TurnID)
			}
		case "commandExecution":
			if event.Method == "item/started" {
				return emit(item.ID, runtime.ToolCall{ID: item.ID, Kind: "execute", RawInput: map[string]any{"command": item.Command, "cwd": item.Cwd}}, v.TurnID)
			}
			status := "unknown"
			if item.Status == "completed" || item.Status == "failed" || item.Status == "declined" {
				status = item.Status
			}
			p := runtime.ToolResult{ExitCode: item.ExitCode, DurationMs: item.DurationMs, ID: item.ID, Kind: "execute", Status: status, Output: &runtime.Output{Availability: "unavailable", Completeness: "unknown"}}
			if item.AggregatedOutput != nil {
				var value any
				dec := json.NewDecoder(bytes.NewReader(item.AggregatedOutput))
				dec.UseNumber()
				if err := dec.Decode(&value); err != nil {
					return err
				}
				p.RawOutput = &value
				if text, ok := value.(string); ok {
					p.Content = []runtime.ContentBlock{runtime.Text(text)}
				}
				p.Output = &runtime.Output{Availability: "provided", Completeness: "retained"}
			}
			return emit(item.ID, p, v.TurnID)
		}
	}
	return nil
}
func (d *Driver) CloseSession(ctx context.Context, id string) error {
	d.mu.Lock()
	s := d.sessions[id]
	if s == nil {
		d.mu.Unlock()
		return nil
	}
	if (s.active || s.sending) && s.err == nil {
		d.mu.Unlock()
		return runtime.ErrWorkUnsettled
	}
	d.mu.Unlock()
	var release ct.ThreadUnsubscribeResponse
	if err := d.client.Call(ctx, "thread/unsubscribe", ct.ThreadUnsubscribeParams{ThreadID: id}, &release); err != nil {
		return err
	}
	switch release.Status {
	case ct.ThreadUnsubscribeStatusNotLoaded:
	case ct.ThreadUnsubscribeStatusUnsubscribed:
		select {
		case <-s.released:
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return errors.New("native resource release not confirmed")
	}
	if err := s.sub.Close(); err != nil {
		return err
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := d.pipe.Flush(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.sessions, id)
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

// Requests is independent of observation; only original SDK handles may reply.
func (d *Driver) requests(c *sdk.Client) {
	defer d.wg.Done()
	for {
		select {
		case <-d.life.Done():
			return
		case r, ok := <-c.Requests():
			if !ok {
				return
			}
			var v struct{ ThreadID, TurnID, ItemID string }
			if json.Unmarshal(r.Params(), &v) != nil {
				continue
			}
			d.mu.Lock()
			s := d.sessions[v.ThreadID]
			valid := s != nil && s.err == nil && v.TurnID != "" && s.work == v.TurnID && !s.retired[v.TurnID]
			d.mu.Unlock()
			if !valid {
				continue
			}
			request := runtime.PermissionRequest(v.ItemID, r.Method())
			method := r.Method()
			switch method {
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			case "item/tool/requestUserInput":
				var schema map[string]any
				if json.Unmarshal(r.Params(), &schema) != nil {
					continue
				}
				request = runtime.InteractionRequest{Kind: "form", Message: "Answer native Codex questions", RequestedSchema: schema}
			default:
				_ = d.emit(s, v.ThreadID, "", runtime.SessionError{Message: "unsupported native interaction", Reason: method}, v.TurnID)
				continue
			}
			ref, err := d.interactions.Add(v.ThreadID, s.source.WithWork(v.TurnID), func(ctx context.Context, a map[string]any) error {
				var response any
				if method == "item/tool/requestUserInput" {
					if len(a) != 2 || a["action"] != "accept" {
						return errors.New("expected explicit form accept/content")
					}
					content, ok := a["content"].(map[string]any)
					if !ok || content["answers"] == nil {
						return errors.New("expected native question answers")
					}
					response = content
				} else {
					allow, err := runtime.PermissionAllowed(a)
					if err != nil {
						return err
					}
					decision := "decline"
					if allow {
						decision = "accept"
					}
					response = map[string]any{"decision": decision}
				}
				return r.Handle().Answer(ctx, response)
			})
			if err == nil {
				state := runtime.RequiresAction
				d.mu.Lock()
				previous := s.state
				s.state = &state
				d.mu.Unlock()
				err = d.emit(s, v.ThreadID, "", runtime.StateUpdate{PreviousState: previous, State: &state}, v.TurnID)
			}
			if err == nil {
				err = d.emit(s, v.ThreadID, "", runtime.InteractionRequested{Interaction: ref, Request: request, Blocking: true}, v.TurnID)
			}
			if err != nil {
				_ = d.emit(s, v.ThreadID, "", runtime.SessionError{Message: "interaction delivery failed"}, v.TurnID)
			}
		}
	}
}
func (d *Driver) AnswerInteraction(ctx context.Context, id, ref string, a map[string]any) (runtime.Acceptance, error) {
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.Acceptance{}, errors.New("session unavailable")
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
	s.err = err
	previous := s.state
	s.state = nil
	d.mu.Unlock()
	_ = d.emit(s, id, "", runtime.SessionError{Message: "Codex session observation failed"}, "")
	_ = d.emit(s, id, "", runtime.StateUpdate{PreviousState: previous}, "")
}

func marshal(v any) []byte { b, _ := json.Marshal(v); return b }

func (d *Driver) Cancel(ctx context.Context, id string) (runtime.Acceptance, error) {
	d.mu.Lock()
	s := d.sessions[id]
	d.mu.Unlock()
	if s == nil {
		return runtime.Acceptance{}, errors.New("session unavailable")
	}
	s.command.Lock()
	defer s.command.Unlock()
	d.mu.Lock()
	work, c := s.work, d.client
	active := s.active
	d.mu.Unlock()
	if !active {
		return runtime.Acceptance{}, nil
	}
	if work == "" {
		return runtime.Acceptance{}, errors.New("outcome_unknown: no confirmed native turn to cancel")
	}
	_, err := c.InterruptTurn(ctx, ct.TurnInterruptParams{ThreadID: id, TurnID: work})
	if err != nil {
		return runtime.Acceptance{}, fmt.Errorf("outcome_unknown: cancel: %w", err)
	}
	return runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes()}, nil
}

func noResources(message string) error {
	return errors.Join(runtime.ErrResourcesReleased, errors.New(message))
}
