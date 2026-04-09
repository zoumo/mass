// Command agent-shim-cli is a minimal interactive CLI for talking to a running
// agent-shim over its Unix socket JSON-RPC interface.
//
// It uses the clean-break shim surface:
//
//	session/prompt    — send a prompt and stream the response
//	session/cancel    — cancel the current turn
//	session/subscribe — register for live notifications
//	runtime/status    — print current state and recovery metadata
//	runtime/history   — print replayable event history
//	runtime/stop      — gracefully shut down the agent
//
// Usage:
//
//	agent-shim-cli --socket /path/to/shim.sock state
//	agent-shim-cli --socket /path/to/shim.sock history
//	agent-shim-cli --socket /path/to/shim.sock prompt --prompt "hello"
//	agent-shim-cli --socket /path/to/shim.sock chat
//	agent-shim-cli --socket /path/to/shim.sock stop
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
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
	Method  string          `json:"method,omitempty"` // for notifications
	Params  json.RawMessage `json:"params,omitempty"` // for notifications
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Client ────────────────────────────────────────────────────────────────

type client struct {
	conn    net.Conn
	scanner *bufio.Scanner
	enc     *json.Encoder
	mu      sync.Mutex
	nextID  int

	// pending maps request ID → reply channel
	pending   map[int]chan rpcResponse
	pendingMu sync.Mutex

	// notifs receives session/update and runtime/stateChange notifications
	notifs chan rpcResponse
}

func dial(socketPath string) (*client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	c := &client{
		conn:    conn,
		scanner: bufio.NewScanner(conn),
		enc:     json.NewEncoder(conn),
		pending: make(map[int]chan rpcResponse),
		notifs:  make(chan rpcResponse, 64),
	}
	go c.readLoop()
	return c, nil
}

func (c *client) readLoop() {
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		var msg rpcResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		// Notification (no ID, has Method)
		if msg.ID == nil && msg.Method != "" {
			select {
			case c.notifs <- msg:
			default:
			}
			continue
		}
		// Reply
		if msg.ID != nil {
			c.pendingMu.Lock()
			ch, ok := c.pending[*msg.ID]
			c.pendingMu.Unlock()
			if ok {
				ch <- msg
			}
		}
	}
	// Scanner stopped (connection closed). Signal notification consumers.
	close(c.notifs)
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
	if err := c.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	resp := <-ch
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func (c *client) close() { _ = c.conn.Close() }

// ── Notification printing ──────────────────────────────────────────────────

// sessionUpdateParams mirrors events.SessionUpdateParams for printing.
type sessionUpdateParams struct {
	SessionID string       `json:"sessionId"`
	Seq       int          `json:"seq"`
	Timestamp string       `json:"timestamp"`
	Event     sessionEvent `json:"event"`
}

type sessionEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type textPayload struct {
	Text string `json:"text"`
}

// runtimeStateChangeParams mirrors events.RuntimeStateChangeParams for printing.
type runtimeStateChangeParams struct {
	SessionID      string `json:"sessionId"`
	Seq            int    `json:"seq"`
	Timestamp      string `json:"timestamp"`
	PreviousStatus string `json:"previousStatus"`
	Status         string `json:"status"`
	PID            int    `json:"pid,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func printNotification(msg rpcResponse) {
	switch msg.Method {
	case "session/update":
		var p sessionUpdateParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			fmt.Fprintf(os.Stderr, "[session/update parse error: %v]\n", err)
			return
		}
		printSessionEvent(p.Seq, p.Event)

	case "runtime/stateChange":
		var p runtimeStateChangeParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			fmt.Fprintf(os.Stderr, "[runtime/stateChange parse error: %v]\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "\033[2m[stateChange seq=%d] %s → %s pid=%d reason=%q\033[0m\n",
			p.Seq, p.PreviousStatus, p.Status, p.PID, p.Reason)

	default:
		fmt.Fprintf(os.Stderr, "[unknown notification: %s] %s\n", msg.Method, string(msg.Params))
	}
}

func printSessionEvent(seq int, ev sessionEvent) {
	switch ev.Type {
	case "text":
		var p textPayload
		_ = json.Unmarshal(ev.Payload, &p)
		fmt.Print(p.Text)
	case "thinking":
		var p textPayload
		_ = json.Unmarshal(ev.Payload, &p)
		fmt.Fprintf(os.Stderr, "\033[2m[thinking seq=%d] %s\033[0m\n", seq, p.Text)
	case "tool_call":
		fmt.Fprintf(os.Stderr, "\033[33m[tool_call seq=%d] %s\033[0m\n", seq, string(ev.Payload))
	case "tool_result":
		fmt.Fprintf(os.Stderr, "\033[2m[tool_result seq=%d] %s\033[0m\n", seq, string(ev.Payload))
	case "turn_end":
		fmt.Println()
	default:
		fmt.Fprintf(os.Stderr, "[%s seq=%d] %s\n", ev.Type, seq, string(ev.Payload))
	}
}

// ── Commands ───────────────────────────────────────────────────────────────

func main() {
	var socketPath string
	var promptText string

	root := &cobra.Command{
		Use:   "agent-shim-cli",
		Short: "Interactive client for agent-shim (clean-break session/runtime surface)",
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "Unix socket path (required)")
	_ = root.MarkPersistentFlagRequired("socket")

	// state sub-command — calls runtime/status
	root.AddCommand(&cobra.Command{
		Use:   "state",
		Short: "Print agent state and recovery metadata (runtime/status)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socketPath)
			if err != nil {
				return err
			}
			defer c.close()
			result, err := c.call("runtime/status", nil)
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

	// history sub-command — calls runtime/history
	var fromSeq int
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Print replayable event history (runtime/history)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socketPath)
			if err != nil {
				return err
			}
			defer c.close()
			params := map[string]any{}
			if cmd.Flags().Changed("from-seq") {
				params["fromSeq"] = fromSeq
			}
			result, err := c.call("runtime/history", params)
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
	root.AddCommand(historyCmd)

	// prompt sub-command — session/subscribe then session/prompt
	promptCmd := &cobra.Command{
		Use:   "prompt",
		Short: "Send a prompt and stream the response (session/prompt)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if promptText == "" {
				return fmt.Errorf("--prompt is required")
			}
			return runPrompt(socketPath, promptText)
		},
	}
	promptCmd.Flags().StringVar(&promptText, "prompt", "", "Text to send")
	root.AddCommand(promptCmd)

	// chat sub-command (interactive REPL)
	root.AddCommand(&cobra.Command{
		Use:   "chat",
		Short: "Interactive chat REPL (type 'exit' to quit)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChat(socketPath)
		},
	})

	// stop sub-command — runtime/stop
	root.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Gracefully shut down the agent (runtime/stop)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socketPath)
			if err != nil {
				return err
			}
			defer c.close()
			_, err = c.call("runtime/stop", nil)
			if err == nil {
				fmt.Println("stop sent")
			}
			return err
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runPrompt(socketPath, text string) error {
	c, err := dial(socketPath)
	if err != nil {
		return err
	}

	// Subscribe first (no afterSeq — fresh connection) so we don't miss early events.
	subResult, err := c.call("session/subscribe", nil)
	if err != nil {
		c.close()
		return fmt.Errorf("session/subscribe: %w", err)
	}
	var sub struct {
		NextSeq int `json:"nextSeq"`
	}
	_ = json.Unmarshal(subResult, &sub)

	// Drain notifications in background; exits when c.notifs is closed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range c.notifs {
			printNotification(msg)
		}
	}()

	// Send prompt (blocks until agent turn completes).
	result, err := c.call("session/prompt", map[string]string{"prompt": text})
	// Close connection now — readLoop will exit and close c.notifs,
	// which unblocks the notifications goroutine above.
	c.close()
	<-done

	if err != nil {
		return fmt.Errorf("session/prompt: %w", err)
	}

	var pr struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(result, &pr)
	if pr.StopReason != "" {
		fmt.Fprintf(os.Stderr, "\n[stop: %s]\n", pr.StopReason)
	}
	return nil
}

func runChat(socketPath string) error {
	c, err := dial(socketPath)
	if err != nil {
		return err
	}
	defer c.close()

	// Subscribe once for the whole session
	if _, err := c.call("session/subscribe", nil); err != nil {
		return fmt.Errorf("session/subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stream notifications in background
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.notifs:
				if !ok {
					return
				}
				printNotification(msg)
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("agent-shim-cli chat — type your message, 'exit' to quit")
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}

		result, err := c.call("session/prompt", map[string]string{"prompt": line})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(result, &pr)
		if pr.StopReason != "" {
			fmt.Fprintf(os.Stderr, "[stop: %s]", pr.StopReason)
		}
	}
	return nil
}
