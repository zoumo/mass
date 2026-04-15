// Package shim provides commands for direct communication with a running
// agent-shim over its Unix socket JSON-RPC interface.
package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"sync"

	"github.com/spf13/cobra"

	shimapi "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/ndjson"
)

// ── JSON-RPC wire types ────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Client ────────────────────────────────────────────────────────────────

type client struct {
	conn net.Conn
	dec  *ndjson.Reader
	enc  *json.Encoder
	mu   sync.Mutex

	nextID    int
	pending   map[int]chan rpcResponse
	pendingMu sync.Mutex

	notifs chan rpcResponse
}

func dial(socketPath string) (*client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	c := &client{
		conn:    conn,
		dec:     ndjson.NewReader(conn),
		enc:     json.NewEncoder(conn),
		pending: make(map[int]chan rpcResponse),
		notifs:  make(chan rpcResponse, 1024),
	}
	go c.readLoop()
	return c, nil
}

func (c *client) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\n[readLoop] PANIC: %v\n%s\n", r, debug.Stack())
		}
		close(c.notifs)
	}()
	for {
		var msg rpcResponse
		err := c.dec.Decode(&msg)
		if errors.Is(err, ndjson.ErrInvalidJSON) {
			fmt.Fprintf(os.Stderr, "\n[readLoop] skipping non-JSON line: %v\n", err)
			continue
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "\n[readLoop] read error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "\n[readLoop] EOF — server closed connection\n")
			}
			break
		}
		if msg.ID == nil && msg.Method != "" {
			if len(c.notifs) == cap(c.notifs) {
				fmt.Fprintf(os.Stderr, "\n[readLoop] notifs channel full (%d), dropping message: %s\n", cap(c.notifs), msg.Method)
			}
			c.notifs <- msg
		} else if msg.ID != nil {
			c.pendingMu.Lock()
			ch, ok := c.pending[*msg.ID]
			c.pendingMu.Unlock()
			if ok {
				ch <- msg
			}
		}
	}
}

func (c *client) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	ch := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	c.mu.Lock()
	err := c.enc.Encode(req)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	resp := <-ch
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// send sends a request without waiting for a response. Safe for concurrent use.
func (c *client) send(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	req := rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	return c.enc.Encode(req)
}

func (c *client) close() { _ = c.conn.Close() }

// ── Notification printing ──────────────────────────────────────────────────

func printNotification(msg rpcResponse) {
	switch msg.Method {
	case shimapi.MethodShimEvent:
		var ev shimapi.ShimEvent
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "[shim/event parse error: %v]\n", err)
			return
		}
		if sc, ok := ev.Content.(shimapi.StateChangeEvent); ok {
			fmt.Fprintf(os.Stderr, "\033[2m[stateChange seq=%d] %s → %s pid=%d reason=%q\033[0m\n",
				ev.Seq, sc.PreviousStatus, sc.Status, sc.PID, sc.Reason)
			return
		}
		printShimEvent(ev)

	default:
		fmt.Fprintf(os.Stderr, "[unknown notification: %s] %s\n", msg.Method, string(msg.Params))
	}
}

func isTurnEndNotification(msg rpcResponse) bool {
	if msg.Method != shimapi.MethodShimEvent {
		return false
	}
	var ev shimapi.ShimEvent
	if err := json.Unmarshal(msg.Params, &ev); err != nil {
		return false
	}
	return ev.Type == shimapi.EventTypeTurnEnd
}

func startNotificationPrinter(ctx context.Context, c *client) <-chan struct{} {
	turnEnd := make(chan struct{}, 16)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\n[notification printer] PANIC: %v\n%s\n", r, debug.Stack())
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.notifs:
				if !ok {
					return
				}
				printNotification(msg)
				if isTurnEndNotification(msg) {
					turnEnd <- struct{}{}
				}
			}
		}
	}()
	return turnEnd
}

func drainTurnEnd(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func printShimEvent(ev shimapi.ShimEvent) {
	switch pl := ev.Content.(type) {
	case shimapi.TextEvent:
		fmt.Print(pl.Text)
	case shimapi.ThinkingEvent:
		fmt.Fprintf(os.Stderr, "\033[2m[thinking seq=%d] %s\033[0m\n", ev.Seq, pl.Text)
	case shimapi.ToolCallEvent:
		content, _ := json.Marshal(pl)
		fmt.Fprintf(os.Stderr, "\033[33m[tool_call seq=%d] %s\033[0m\n", ev.Seq, content)
	case shimapi.ToolResultEvent:
		content, _ := json.Marshal(pl)
		fmt.Fprintf(os.Stderr, "\033[2m[tool_result seq=%d] %s\033[0m\n", ev.Seq, content)
	case shimapi.TurnEndEvent:
		fmt.Println()
	default:
		content, _ := json.Marshal(ev.Content)
		fmt.Fprintf(os.Stderr, "[%s seq=%d] %s\n", ev.Type, ev.Seq, content)
	}
}

// ── Prompt / chat helpers ──────────────────────────────────────────────────

func runPrompt(sock, text string) error {
	c, err := dial(sock)
	if err != nil {
		return err
	}
	defer c.close()

	if _, err := c.call(shimapi.MethodSessionSubscribe, nil); err != nil {
		return fmt.Errorf("session/subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turnEnd := startNotificationPrinter(ctx, c)
	drainTurnEnd(turnEnd)

	result, err := c.call(shimapi.MethodSessionPrompt, map[string]string{"prompt": text})
	if err != nil {
		return fmt.Errorf("session/prompt: %w", err)
	}
	<-turnEnd

	var pr struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(result, &pr)
	if pr.StopReason != "" {
		fmt.Fprintf(os.Stderr, "\n[stop: %s]\n", pr.StopReason)
	}
	return nil
}

func runChat(sock string) error {
	return runChatTUI(sock)
}

// ── NewCommand ─────────────────────────────────────────────────────────────

// NewCommand returns the "shim" cobra command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shim",
		Short: "Direct communication with a running agent-shim over its Unix socket",
	}

	var socket string
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Unix socket path for the shim (required)")
	_ = cmd.MarkPersistentFlagRequired("socket")

	cmd.AddCommand(&cobra.Command{
		Use:   "state",
		Short: "Print agent state and recovery metadata (runtime/status)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socket)
			if err != nil {
				return err
			}
			defer c.close()
			result, err := c.call(shimapi.MethodRuntimeStatus, nil)
			if err != nil {
				return err
			}
			var pretty any
			_ = json.Unmarshal(result, &pretty)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(pretty)
		},
	})

	var fromSeq int
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Print replayable event history (runtime/history)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socket)
			if err != nil {
				return err
			}
			defer c.close()
			params := map[string]any{}
			if cmd.Flags().Changed("from-seq") {
				params["fromSeq"] = fromSeq
			}
			result, err := c.call(shimapi.MethodRuntimeHistory, params)
			if err != nil {
				return err
			}
			var pretty any
			_ = json.Unmarshal(result, &pretty)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(pretty)
		},
	}
	historyCmd.Flags().IntVar(&fromSeq, "from-seq", 0, "Return history from this sequence number")
	cmd.AddCommand(historyCmd)

	var promptText string
	promptCmd := &cobra.Command{
		Use:   "prompt",
		Short: "Send a prompt and stream the response (session/prompt)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if promptText == "" {
				return fmt.Errorf("--prompt is required")
			}
			return runPrompt(socket, promptText)
		},
	}
	promptCmd.Flags().StringVar(&promptText, "prompt", "", "Text to send")
	cmd.AddCommand(promptCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "chat",
		Short: "Interactive chat REPL (type 'exit' to quit)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChat(socket)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Gracefully shut down the agent (runtime/stop)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socket)
			if err != nil {
				return err
			}
			defer c.close()
			_, err = c.call(shimapi.MethodRuntimeStop, nil)
			if err == nil {
				fmt.Println("stop sent")
			}
			return err
		},
	})

	return cmd
}
