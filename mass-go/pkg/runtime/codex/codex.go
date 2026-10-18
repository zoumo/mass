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

	sdk "github.com/zoumo/agent-sdk-go/codex"
	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type Options struct {
	Executable string
	Env        []string
	Dir        string
}
type connection struct {
	client  *sdk.Client
	mu      sync.Mutex
	created bool
	cancel  context.CancelFunc
	sub     *sdk.Subscription
	closed  bool
}
type session struct {
	conn     *connection
	id       string
	sub      *sdk.Subscription
	ready    bool
	next     sync.Mutex
	requests <-chan sdk.Request
}

var _ rt.Connection = (*connection)(nil)
var _ rt.Session = (*session)(nil)

func Open(ctx context.Context, options Options) (rt.Connection, error) {
	if options.Executable == "" || options.Env == nil {
		return nil, errors.New("codex runtime requires an explicit executable and environment")
	}
	client, err := sdk.NewStdio(ctx, sdk.StdioOptions{Executable: options.Executable, Args: []string{"app-server"}, Env: options.Env, Dir: options.Dir, Initialize: types.InitializeParams{ClientInfo: types.ClientInfo{Name: "mass-go", Version: "experiment"}}})
	if err != nil {
		return nil, safeError(ctx, "open")
	}
	return &connection{client: client}, nil
}
func (c *connection) NewSession(ctx context.Context) (rt.Session, error) {
	c.mu.Lock()
	if c.created || c.closed {
		c.mu.Unlock()
		return nil, errors.New("codex runtime supports one session per connection")
	}
	c.created = true
	c.mu.Unlock()
	result, err := c.client.StartThread(ctx, types.ThreadStartParams{Ephemeral: types.Some(true)})
	if err != nil {
		return nil, safeError(ctx, "create session")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	sub, err := c.client.Subscribe(lifetime, result.Thread.ID, types.SubscriptionOptions{})
	if err != nil {
		cancel()
		return nil, safeError(ctx, "subscribe")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		_ = sub.Abort()
		return nil, errors.New("codex runtime is closed")
	}
	c.cancel = cancel
	c.sub = sub
	c.mu.Unlock()
	return &session{conn: c, id: result.Thread.ID, sub: sub, requests: c.client.Requests()}, nil
}
func (c *connection) Close() error {
	c.mu.Lock()
	c.closed = true
	cancel, sub := c.cancel, c.sub
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sub != nil {
		_ = sub.Abort()
	}
	if err := c.client.Close(); err != nil {
		return errors.New("codex runtime close failed")
	}
	return nil
}
func (s *session) Close() error { _ = s.sub.Close(); return s.conn.Close() }

// Identity uses the native thread ID as the runtime session unit; it is not
// the SDK Thread.SessionID shared across a thread tree.
func (s *session) Identity() rt.Identity { return rt.Identity{Provider: "codex", SessionID: s.id} }
func (s *session) Capabilities() rt.Capabilities {
	return rt.Capabilities{NativeTurnID: true, NativeAcknowledgement: true}
}
func (s *session) Submit(ctx context.Context, text string) (rt.Submission, error) {
	input := types.UserInput{Text: types.Ptr(types.TextUserInput{Type: types.TextUserInputTypeText, Text: text, TextElements: types.Some([]types.TextElement{})})}
	result, err := s.conn.client.StartTurn(ctx, types.TurnStartParams{ThreadID: s.id, Input: []types.UserInput{input}})
	if err != nil {
		return rt.Submission{}, safeError(ctx, "submit")
	}
	return rt.Submission{TurnID: result.Turn.ID, Evidence: rt.NativeAcknowledged}, nil
}
func (s *session) Cancel(ctx context.Context, turnID string) error {
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
		return rt.Event{Kind: rt.SessionReady, SessionID: s.id}, nil
	}
	for {
		select {
		case <-ctx.Done():
			return rt.Event{}, ctx.Err()
		case request, ok := <-s.requests:
			if !ok {
				s.requests = nil
				continue
			}
			return mapRequest(request, s.id), nil
		case native, ok := <-s.sub.Events():
			if !ok {
				if s.sub.Err() != nil {
					return rt.Event{}, errors.New("codex runtime event stream terminated")
				}
				return rt.Event{}, io.EOF
			}
			return mapEvent(native, s.id), nil
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
	if data.ThreadID != "" {
		event.SessionID = data.ThreadID
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
