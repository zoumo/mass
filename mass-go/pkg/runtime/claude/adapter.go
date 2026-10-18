// Package claude adapts the native Claude SDK to the experimental runtime.
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/zoumo/agent-sdk-go/claude"
	"github.com/zoumo/agent-sdk-go/claude/types"
	"github.com/zoumo/mass/mass-go/internal/managed"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

type driver struct {
	name        string
	options     rt.Config
	mu          sync.Mutex
	closed      bool
	connections []*connection
}

func New(name string, options rt.Config, revision uint64) *managed.Runtime {
	options.Env = append([]string{}, options.Env...)
	return managed.New(name, "claude", revision, &driver{name: name, options: options})
}
func (d *driver) Start(ctx context.Context) (rt.Health, error) {
	if err := ctx.Err(); err != nil {
		return rt.Health{}, err
	}
	info, err := os.Stat(d.options.Executable)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return rt.Health{}, rt.ErrUnavailable
	}
	d.mu.Lock()
	d.closed = false
	d.connections = nil
	d.mu.Unlock()
	return rt.Health{Configured: rt.Known(true), Connected: rt.UnknownObservation(), ProtocolResponsive: rt.UnknownObservation(), ProbeScope: "per_session_process_prepared", ObservedAt: time.Now().UTC()}, nil
}
func (d *driver) CreateSession(ctx context.Context) (rt.Session, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	c := &connection{name: d.name, ctx: lifetime, cancel: cancel, events: make(chan rt.Event), finished: make(chan struct{})}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		cancel()
		return nil, rt.ErrUnavailable
	}
	d.connections = append(d.connections, c)
	d.mu.Unlock()
	openCtx, stopOpen := context.WithCancel(ctx)
	stopLifetime := context.AfterFunc(lifetime, stopOpen)
	defer stopOpen()
	defer stopLifetime()
	if err := c.open(openCtx, d.options); err != nil {
		cancel()
		return nil, err
	}
	return &session{conn: c}, nil
}
func (d *driver) Stop() error {
	d.mu.Lock()
	d.closed = true
	connections := append([]*connection(nil), d.connections...)
	d.mu.Unlock()
	var result error
	for _, c := range connections {
		if err := c.Close(); err != nil {
			result = err
		}
	}
	return result
}

type connection struct {
	client    *sdk.Client
	name      string
	closeOnce sync.Once
	closeErr  error
	opening   sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	events    chan rt.Event
	finished  chan struct{}
	mu        sync.Mutex
	terminal  error
	nativeID  string
}
type session struct {
	conn *connection
	next sync.Mutex
}

var _ rt.Session = (*session)(nil)

func (c *connection) open(ctx context.Context, options rt.Config) error {
	c.opening.Lock()
	defer c.opening.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.ctx.Err() != nil {
		return rt.ErrUnavailable
	}
	client, err := sdk.Open(ctx, sdk.Options{CLIPath: options.Executable, Env: options.Env, WorkDir: options.Dir, PermissionHandler: func(p *sdk.PermissionRequest) {
		select {
		case c.events <- c.permissionEvent(p):
		case <-p.Context().Done():
		case <-c.ctx.Done():
		}
	}})
	if err != nil {
		return safeError(ctx, "open")
	}
	c.client = client
	go c.receive()
	return nil
}
func (c *connection) receive() {
	defer close(c.finished)
	for {
		received, err := c.client.Receive(c.ctx)
		if err != nil {
			c.mu.Lock()
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				c.terminal = errors.New("claude runtime event stream terminated")
			}
			c.mu.Unlock()
			return
		}
		var events []rt.Event
		if native, ok := received.(sdk.Event); ok {
			if (native.System != nil || native.Result != nil) && native.SessionID != "" && (native.AgentID == nil || *native.AgentID == "") && (native.ParentToolUseID == nil || *native.ParentToolUseID == "") {
				c.mu.Lock()
				c.nativeID = native.SessionID
				c.mu.Unlock()
			}
			events = mapEvent(native)
		} else {
			events = []rt.Event{{Kind: rt.Unknown, Diagnostic: diagnostic(fmt.Sprintf("sdk:%T", received), nil)}}
		}
		for _, event := range events {
			select {
			case c.events <- event:
			case <-c.ctx.Done():
				return
			}
		}
	}
}
func (c *connection) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.opening.Lock()
		client := c.client
		c.opening.Unlock()
		if client != nil {
			if err := client.Close(); err != nil {
				c.closeErr = errors.New("claude runtime close failed")
			}
			<-c.finished
		}
	})
	return c.closeErr
}
func (s *session) Close() error { return s.conn.Close() }
func (s *session) Key() rt.SessionKey {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	return rt.SessionKey{RuntimeName: s.conn.name, SessionID: s.conn.nativeID}
}
func (s *session) Snapshot() rt.SessionSnapshot {
	state := "attached"
	if s.Key().SessionID == "" {
		state = "opening"
	}
	select {
	case <-s.conn.ctx.Done():
		state = "closed"
	default:
	}
	return rt.SessionSnapshot{Key: s.Key(), Attachment: state, ObservedAt: time.Now().UTC()}
}
func (s *session) Capabilities() rt.Capabilities { return rt.Capabilities{} }
func (s *session) Submit(ctx context.Context, text string) (rt.Submission, error) {
	if s.conn.ctx.Err() != nil {
		return rt.Submission{}, rt.ErrUnavailable
	}
	if err := s.conn.client.Send(ctx, text); err != nil {
		return rt.Submission{}, safeError(ctx, "submit")
	}
	return rt.Submission{Evidence: rt.SDKWriteSuccess}, nil
}
func (s *session) Cancel(ctx context.Context, turnID string) error {
	if turnID != "" {
		return errors.New("claude runtime has no native turn ID")
	}
	if err := s.conn.client.Interrupt(ctx); err != nil {
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
	select {
	case event := <-s.conn.events:
		event.RuntimeName = s.conn.name
		event.SessionID = s.Key().SessionID
		return event, nil
	case <-ctx.Done():
		return rt.Event{}, ctx.Err()
	case <-s.conn.finished:
		s.conn.mu.Lock()
		defer s.conn.mu.Unlock()
		if s.conn.terminal != nil {
			return rt.Event{}, s.conn.terminal
		}
		return rt.Event{}, io.EOF
	}
}
func safeError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("claude runtime %s failed", operation)
}
func diagnostic(kind string, data []byte) rt.Diagnostic {
	sum := sha256.Sum256(data)
	if len(kind) > 128 {
		kind = kind[:128]
	}
	return rt.Diagnostic{NativeKind: kind, SHA256: hex.EncodeToString(sum[:])}
}
func mapEvent(native types.Event) []rt.Event {
	base := rt.Event{Kind: rt.Unknown, SessionID: native.SessionID, ItemID: native.UUID, Diagnostic: diagnostic(native.Type+"/"+native.Subtype, native.Raw)}
	if (native.AgentID != nil && *native.AgentID != "") || (native.ParentToolUseID != nil && *native.ParentToolUseID != "") {
		return []rt.Event{base}
	}
	if native.Result != nil {
		base.Kind = rt.TurnCompleted
		base.Status = "failed"
		if native.Result.Subtype == "success" && !native.Result.IsError {
			base.Status = "completed"
		}
		return []rt.Event{base}
	}
	if native.System != nil && native.Subtype == "init" && native.SessionID != "" {
		base.Kind = rt.SessionReady
		return []rt.Event{base}
	}
	if native.Error != nil || native.Type == "error" {
		base.Kind = rt.Failure
		base.Status = "native_error"
		return []rt.Event{base}
	}
	if native.Stream != nil && native.Stream.Delta != nil {
		delta := native.Stream.Delta
		if delta.Type == "content_block_delta" {
			var text struct{ Type, Text string }
			if json.Unmarshal(delta.Delta, &text) == nil && text.Type == "text_delta" {
				base.Kind = rt.Message
				base.Text = text.Text
				base.Status = "delta"
			}
		}
		return []rt.Event{base}
	}
	message := native.Assistant
	if message == nil {
		message = native.User
	}
	if message == nil {
		return []rt.Event{base}
	}
	base.ItemID = message.ID
	if message.Content.Text != nil {
		base.Kind = rt.Message
		base.Text = *message.Content.Text
		return []rt.Event{base}
	}
	var events []rt.Event
	for _, block := range message.Content.Blocks {
		event := base
		switch {
		case block.Text != nil:
			event.Kind = rt.Message
			event.Text = block.Text.Text
		case block.ToolUse != nil:
			event.Kind = rt.Tool
			event.ItemID = block.ToolUse.ID
			event.Tool = block.ToolUse.Name
			event.Status = "started"
		case block.ToolResult != nil:
			event.Kind = rt.Tool
			event.ItemID = block.ToolResult.ToolUseID
			event.Status = "completed"
			if block.ToolResult.IsError {
				event.Status = "error"
			}
			text, known := toolResultText(block.ToolResult.Content)
			event.Text = text
			if !known {
				events = append(events, rt.Event{Kind: rt.Unknown, SessionID: native.SessionID, ItemID: block.ToolResult.ToolUseID, Diagnostic: diagnostic("tool_result/content", block.ToolResult.Content)})
			}
		default:
			event.Diagnostic = diagnostic("content/"+block.Type, block.Raw)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return []rt.Event{base}
	}
	return events
}

// Tool output can be a string or an ordered array of content blocks.
func toolResultText(data json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(data, &text) == nil {
		return text, true
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(data, &blocks) != nil {
		return "", false
	}
	var output strings.Builder
	known := true
	for _, block := range blocks {
		if block.Type == "text" {
			output.WriteString(block.Text)
		} else {
			known = false
		}
	}
	return output.String(), known
}
func (c *connection) permissionEvent(p *sdk.PermissionRequest) rt.Event {
	metadata, err := p.Metadata()
	if err != nil || metadata.AgentID != "" {
		return rt.Event{Kind: rt.Unknown, Diagnostic: diagnostic("can_use_tool/unowned", p.Raw())}
	}
	event := mapPermission(p)
	c.mu.Lock()
	event.SessionID = c.nativeID
	c.mu.Unlock()
	return event
}
