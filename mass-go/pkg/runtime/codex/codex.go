// Package codex adapts the native Codex SDK to the experimental runtime.
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	sdk "github.com/zoumo/agent-sdk-go/codex"
	"github.com/zoumo/agent-sdk-go/codex/types"
	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type driver struct {
	options  rt.Config
	name     string
	mu       sync.Mutex
	client   *sdk.Client
	sessions map[string]*session
	cancel   context.CancelFunc
	closed   bool
}
type session struct {
	conn     *driver
	id       string
	sub      *sdk.Subscription
	ready    bool
	next     sync.Mutex
	requests chan sdk.Request
	done     chan struct{}
	once     sync.Once
}

var _ rt.Session = (*session)(nil)

// New prepares a named Runtime without starting a native process.
func New(name string, options rt.Config, revision uint64) *managed.Runtime {
	options.Env = append([]string{}, options.Env...)
	return managed.New(name, "codex", revision, &driver{name: name, options: options}, options.CreationLimits)
}
func (c *driver) Start(ctx context.Context) (rt.Health, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.closed = false
	c.mu.Unlock()
	client, err := sdk.NewStdio(ctx, sdk.StdioOptions{Executable: c.options.Executable, Args: []string{"app-server"}, Env: c.options.Env, Dir: c.options.Dir, Initialize: types.InitializeParams{ClientInfo: types.ClientInfo{Name: "mass-go", Version: "experiment"}}})
	if err != nil {
		cancel()
		return rt.Health{}, safeError(ctx, "start")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		_ = client.Close()
		return rt.Health{}, rt.ErrUnavailable
	}
	c.client = client
	c.sessions = make(map[string]*session)
	c.mu.Unlock()
	go c.routeRequests(lifetime, client)
	return rt.Health{Configured: rt.Known(true), Connected: rt.Known(true), ProtocolResponsive: rt.Known(true), ProbeScope: "shared_protocol_initialize", ObservedAt: time.Now().UTC()}, nil
}
func (c *driver) routeRequests(ctx context.Context, client *sdk.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case request, ok := <-client.Requests():
			if !ok {
				return
			}
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(request.Params(), &params)
			c.mu.Lock()
			session := c.sessions[params.ThreadID]
			c.mu.Unlock()
			if session == nil {
				continue
			}
			select {
			case session.requests <- request:
			case <-session.done:
			case <-ctx.Done():
				return
			}
		}
	}
}
func (c *driver) RequiresInitialInput() bool { return false }
func (c *driver) CreateSession(ctx context.Context, options rt.SessionOptions) (rt.Session, error) {
	c.mu.Lock()
	client := c.client
	closed := c.closed
	c.mu.Unlock()
	if closed || client == nil {
		return nil, rt.ErrUnavailable
	}
	result, err := client.StartThread(ctx, types.ThreadStartParams{Ephemeral: types.Some(true)})
	if err != nil {
		return nil, safeError(ctx, "create session")
	}
	if result.Thread.ID == "" {
		return nil, rt.ErrInvalidInput
	}
	sub, err := client.Subscribe(context.Background(), result.Thread.ID, types.SubscriptionOptions{})
	if err != nil {
		return nil, safeError(ctx, "subscribe")
	}
	s := &session{conn: c, id: result.Thread.ID, sub: sub, requests: make(chan sdk.Request, 64), done: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = s.Close()
		return nil, rt.ErrUnavailable
	}
	if _, exists := c.sessions[s.id]; exists {
		c.mu.Unlock()
		_ = sub.Abort()
		return nil, rt.ErrConflict
	}
	c.sessions[s.id] = s
	c.mu.Unlock()
	if options.InitialInput != nil {
		if _, err := s.Submit(ctx, *options.InitialInput); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}
func (c *driver) Stop() error {
	c.mu.Lock()
	c.closed = true
	client, cancel := c.client, c.cancel
	sessions := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, s := range sessions {
		_ = s.Close()
	}
	if client != nil {
		if err := client.Close(); err != nil {
			return errors.New("codex runtime stop failed")
		}
	}
	return nil
}
func (s *session) Close() error { s.once.Do(func() { close(s.done); _ = s.sub.Abort() }); return nil }
func (s *session) Key() rt.SessionKey {
	return rt.SessionKey{RuntimeName: s.conn.name, SessionID: s.id}
}
func (s *session) Snapshot() rt.SessionSnapshot {
	state := "attached"
	select {
	case <-s.done:
		state = "closed"
	default:
	}
	return rt.SessionSnapshot{Key: s.Key(), Attachment: state, ObservedAt: time.Now().UTC()}
}

func (s *session) Capabilities() rt.Capabilities {
	return rt.Capabilities{NativeTurnID: true, NativeAcknowledgement: true}
}
func (s *session) Submit(ctx context.Context, text string) (rt.Submission, error) {
	select {
	case <-s.done:
		return rt.Submission{}, rt.ErrUnavailable
	default:
	}
	input := types.UserInput{Text: types.Ptr(types.TextUserInput{Type: types.TextUserInputTypeText, Text: text, TextElements: types.Some([]types.TextElement{})})}
	result, err := s.conn.client.StartTurn(ctx, types.TurnStartParams{ThreadID: s.id, Input: []types.UserInput{input}})
	if err != nil {
		return rt.Submission{}, safeError(ctx, "submit")
	}
	return rt.Submission{TurnID: result.Turn.ID, Evidence: rt.NativeAcknowledged}, nil
}
func (s *session) Cancel(ctx context.Context, turnID string) error {
	select {
	case <-s.done:
		return rt.ErrUnavailable
	default:
	}
	if turnID == "" {
		return errors.New("codex runtime cancellation requires a native turn ID")
	}
	_, err := s.conn.client.InterruptTurn(ctx, types.TurnInterruptParams{ThreadID: s.id, TurnID: turnID})
	if err != nil {
		return safeError(ctx, "interrupt")
	}
	return nil
}
func (s *session) Next(ctx context.Context) (rt.Event, error) {
	s.next.Lock()
	defer s.next.Unlock()
	if err := ctx.Err(); err != nil {
		return rt.Event{}, err
	}
	if !s.ready {
		s.ready = true
		return rt.Event{Kind: rt.SessionReady, RuntimeName: s.conn.name, SessionID: s.id}, nil
	}
	for {
		select {
		case <-s.done:
			return rt.Event{}, io.EOF
		case <-ctx.Done():
			return rt.Event{}, ctx.Err()
		case request, ok := <-s.requests:
			if !ok {
				s.requests = nil
				continue
			}
			event := mapRequest(request, s.id)
			event.RuntimeName = s.conn.name
			return event, nil
		case native, ok := <-s.sub.Events():
			if !ok {
				if s.sub.Err() != nil {
					return rt.Event{}, errors.New("codex runtime event stream terminated")
				}
				return rt.Event{}, io.EOF
			}
			event := mapEvent(native, s.id)
			event.RuntimeName = s.conn.name
			return event, nil
		}
	}
}
func diagnostic(kind string, data []byte) rt.Diagnostic {
	sum := sha256.Sum256(data)
	if len(kind) > 128 {
		kind = kind[:128]
	}
	return rt.Diagnostic{NativeKind: kind, SHA256: hex.EncodeToString(sum[:])}
}
func safeError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("codex runtime %s failed", operation)
}
func mapEvent(native types.Event, id string) rt.Event {
	event := rt.Event{Kind: rt.Unknown, SessionID: id, Diagnostic: diagnostic(native.Method, native.Params)}
	if native.Method == "" {
		event.Diagnostic.NativeKind = fmt.Sprintf("sdk:%T", native.Data)
		return event
	}
	switch native.Method {
	case "turn/started", "turn/completed", "item/agentMessage/delta", "item/started", "item/completed", "error":
	default:
		return event
	}
	var data struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
		Turn     struct{ ID, Status string }
		Item     struct{ ID, Type, Text, Name, Tool, Command, AggregatedOutput string }
		Error    struct{ Message string }
	}
	if err := json.Unmarshal(native.Params, &data); err != nil || native.DecodeError != nil {
		event.Kind = rt.Failure
		event.Status = "decode_error"
		return event
	}
	event.TurnID = data.TurnID
	event.ItemID = data.ItemID
	switch native.Method {
	case "turn/started":
		event.Kind = rt.TurnStarted
		event.TurnID = data.Turn.ID
		event.Status = turnStatus(data.Turn.Status)
	case "turn/completed":
		event.Kind = rt.TurnCompleted
		event.TurnID = data.Turn.ID
		event.Status = turnStatus(data.Turn.Status)
	case "item/agentMessage/delta":
		event.Kind = rt.Message
		event.Text = data.Delta
		event.Status = "delta"
	case "item/started", "item/completed":
		event.ItemID = data.Item.ID
		switch data.Item.Type {
		case "agentMessage":
			event.Kind = rt.Message
			event.Text = data.Item.Text
		case "commandExecution":
			event.Kind = rt.Tool
			event.Tool = data.Item.Command
			event.Text = data.Item.AggregatedOutput
		case "mcpToolCall", "dynamicToolCall":
			event.Kind = rt.Tool
			event.Tool = data.Item.Name
			if event.Tool == "" {
				event.Tool = data.Item.Tool
			}
		}
		event.Status = native.Method
	case "error":
		event.Kind = rt.Failure
		event.Status = "native_error"
	}
	return event
}

func turnStatus(native string) string {
	switch native {
	case "inProgress":
		return "in_progress"
	case "completed", "failed", "interrupted":
		return native
	default:
		return "unknown"
	}
}
