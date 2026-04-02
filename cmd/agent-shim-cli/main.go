// Command agent-shim-cli is a minimal interactive CLI for talking to a running
// agent-shim over its Unix socket JSON-RPC interface.
//
// Usage:
//
//	agent-shim-cli --socket /tmp/gsd-test.sock
//	agent-shim-cli --socket /tmp/gsd-test.sock --prompt "hello"
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

	// events receives $/event notifications
	events chan rpcResponse
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
		events:  make(chan rpcResponse, 64),
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
			case c.events <- msg:
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
	// Scanner stopped (connection closed). Signal event consumers.
	close(c.events)
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

func (c *client) notify(method string, params any) error {
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	return c.enc.Encode(req)
}

func (c *client) close() { c.conn.Close() }

// ── Event printing ─────────────────────────────────────────────────────────

type eventNotification struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type textPayload struct {
	Text string `json:"text"`
}

func printEvent(raw json.RawMessage) {
	var n eventNotification
	if err := json.Unmarshal(raw, &n); err != nil {
		fmt.Fprintf(os.Stderr, "[event parse error: %v]\n", err)
		return
	}
	switch n.Type {
	case "text":
		var p textPayload
		_ = json.Unmarshal(n.Payload, &p)
		fmt.Print(p.Text)
	case "thinking":
		var p textPayload
		_ = json.Unmarshal(n.Payload, &p)
		fmt.Fprintf(os.Stderr, "\033[2m[thinking] %s\033[0m\n", p.Text)
	case "tool_call":
		fmt.Fprintf(os.Stderr, "\033[33m[tool_call] %s\033[0m\n", string(n.Payload))
	case "tool_result":
		fmt.Fprintf(os.Stderr, "\033[2m[tool_result] %s\033[0m\n", string(n.Payload))
	case "turn_end":
		fmt.Println()
	default:
		fmt.Fprintf(os.Stderr, "[%s] %s\n", n.Type, string(n.Payload))
	}
}

// ── Commands ───────────────────────────────────────────────────────────────

func main() {
	var socketPath string
	var promptText string

	root := &cobra.Command{
		Use:   "agent-shim-cli",
		Short: "Interactive client for agent-shim",
	}
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "Unix socket path (required)")
	_ = root.MarkPersistentFlagRequired("socket")

	// state sub-command
	root.AddCommand(&cobra.Command{
		Use:   "state",
		Short: "Print agent state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socketPath)
			if err != nil {
				return err
			}
			defer c.close()
			result, err := c.call("GetState", nil)
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

	// prompt sub-command
	promptCmd := &cobra.Command{
		Use:   "prompt",
		Short: "Send a prompt and stream the response",
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

	// shutdown sub-command
	root.AddCommand(&cobra.Command{
		Use:   "shutdown",
		Short: "Gracefully shut down the agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dial(socketPath)
			if err != nil {
				return err
			}
			defer c.close()
			_, err = c.call("Shutdown", nil)
			if err == nil {
				fmt.Println("shutdown sent")
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

	// Subscribe first so we don't miss early events.
	if _, err := c.call("Subscribe", nil); err != nil {
		c.close()
		return fmt.Errorf("subscribe: %w", err)
	}

	// Drain events in background; exits when c.events is closed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range c.events {
			printEvent(ev.Params)
		}
	}()

	// Send prompt (blocks until agent turn completes).
	result, err := c.call("Prompt", map[string]string{"text": text})
	// Close connection now — readLoop will exit and close c.events,
	// which unblocks the events goroutine above.
	c.close()
	<-done

	if err != nil {
		return fmt.Errorf("prompt: %w", err)
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
	if _, err := c.call("Subscribe", nil); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stream events in background
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-c.events:
				if !ok {
					return
				}
				printEvent(ev.Params)
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

		result, err := c.call("Prompt", map[string]string{"text": line})
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
