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
	conn      *driver
	id        string
	sub       *sdk.Subscription
	position  uint64
	ready     bool
	next      sync.Mutex
	requests  chan sdk.Request
	done      chan struct{}
	once      sync.Once
	handles   map[string]rt.ResponseHandle
	handlesMu sync.Mutex
}

var _ managed.NativeSession = (*session)(nil)

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
	published := managed.NewSession(s)
	if options.InitialInput != nil {
		if _, err := published.Submit(ctx, rt.TextInput(*options.InitialInput)); err != nil {
			_ = published.Close()
			return nil, err
		}
	}
	return published, nil
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
		receipt := rt.UnknownSubmission()
		receipt.HandedToSDK = rt.Known(false)
		receipt.WriteCompleted = rt.Known(false)
		return receipt, &rt.SafeError{Category: "unavailable", Scope: "operation", Message: "attachment is closed"}
	default:
	}
	input := types.UserInput{Text: types.Ptr(types.TextUserInput{Type: types.TextUserInputTypeText, Text: text, TextElements: types.Some([]types.TextElement{})})}
	result, err := s.conn.client.StartTurn(ctx, types.TurnStartParams{ThreadID: s.id, Input: []types.UserInput{input}})
	if err != nil {
		return submissionError(err)
	}
	receipt := rt.UnknownSubmission()
	receipt.WorkRef = result.Turn.ID
	receipt.HandedToSDK = rt.Known(true)
	receipt.NativeAcknowledged = rt.Known(true)
	receipt.Outcome = "admitted"
	return receipt, nil
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
		_, failure := submissionError(err)
		return failure
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
		return rt.Event{Kind: rt.SessionReady, RuntimeName: s.conn.name, SessionID: s.id, Origin: rt.EventOrigin{Kind: "runtime", Attribution: "root", Completeness: "complete"}}, nil
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
			event, handle := mapRequest(request, s.id)
			if handle != nil {
				s.handlesMu.Lock()
				if s.handles == nil {
					s.handles = map[string]rt.ResponseHandle{}
				}
				s.handles[event.Interaction.Ref] = handle
				s.handlesMu.Unlock()
			}
			event.RuntimeName = s.conn.name
			s.position++
			event.SourcePosition = s.position
			event.ObservedAt = time.Now().UTC()
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
			s.position++
			event.SourcePosition = s.position
			event.ObservedAt = time.Now().UTC()
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
		event.Diagnostic.NativeKind = "sdk_derived/unsupported"
		event.Origin = rt.EventOrigin{Kind: "sdk_derived", Attribution: "unresolved", Completeness: "unknown"}
		return event
	}
	if native.Method == "item/commandExecution/outputDelta" || native.Method == "item/fileChange/outputDelta" || native.Method == "item/mcpToolCall/progress" {
		return toolUpdate(native, id)
	}
	if converted, ok := messageEvent(native, id); ok {
		return converted
	}
	var data struct {
		TurnID string
		Turn   struct{ ID, Status string }
	}
	switch native.Method {
	case "turn/started", "turn/completed", "item/started", "item/completed", "error":
	default:
		return event
	}
	if json.Unmarshal(native.Params, &data) != nil || native.DecodeError != nil {
		event.Kind = rt.Failure
		event.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return event
	}
	event.WorkRef = data.TurnID
	switch native.Method {
	case "turn/started", "turn/completed":
		event.Kind = rt.TurnStarted
		if native.Method == "turn/completed" {
			event.Kind = rt.TurnCompleted
		}
		event.WorkRef = data.Turn.ID
		event.Work = &rt.WorkPayload{Status: turnStatus(data.Turn.Status)}
	case "item/started", "item/completed":
		return toolEvent(native, id)
	case "error":
		event.Kind = rt.Failure
		event.Error = &rt.SafeError{Category: "native_error", Scope: "session"}
	}
	return event
}

func (s *session) ResponseHandle(ref string) rt.ResponseHandle {
	s.handlesMu.Lock()
	defer s.handlesMu.Unlock()
	return s.handles[ref]
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
