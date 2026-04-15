// Package shim provides commands for direct communication with a running
// agent-shim over its Unix socket JSON-RPC interface.
package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/pkg/jsonrpc"
	shimapi "github.com/zoumo/mass/pkg/shim/api"
	shimclient "github.com/zoumo/mass/pkg/shim/client"
)

// dialShim connects to a shim Unix socket and returns a typed ShimClient.
func dialShim(ctx context.Context, socketPath string, opts ...jsonrpc.ClientOption) (*shimclient.ShimClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	return shimclient.NewShimClient(c), nil
}

// ── Notification printing ──────────────────────────────────────────────────

// parseNotification parses a jsonrpc.NotificationMsg into a ShimEvent.
// Returns nil if the notification is not a shim event or cannot be parsed.
func parseNotification(msg jsonrpc.NotificationMsg) *shimapi.ShimEvent {
	if msg.Method != shimapi.MethodShimEvent {
		return nil
	}
	var ev shimapi.ShimEvent
	if err := json.Unmarshal(msg.Params, &ev); err != nil {
		return nil
	}
	return &ev
}

func printNotification(ev shimapi.ShimEvent) {
	if sc, ok := ev.Payload.(shimapi.StateChangeEvent); ok {
		fmt.Fprintf(os.Stderr, "\033[2m[stateChange seq=%d] %s → %s pid=%d reason=%q\033[0m\n",
			ev.Seq, sc.PreviousStatus, sc.Status, sc.PID, sc.Reason)
		return
	}
	printShimEvent(ev)
}

func startNotificationPrinter(ctx context.Context, notifs <-chan jsonrpc.NotificationMsg) <-chan struct{} {
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
			case msg, ok := <-notifs:
				if !ok {
					return
				}
				ev := parseNotification(msg)
				if ev == nil {
					continue
				}
				printNotification(*ev)
				if ev.Type == shimapi.EventTypeTurnEnd {
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
	switch pl := ev.Payload.(type) {
	case shimapi.ContentEvent:
		switch ev.Type {
		case shimapi.EventTypeAgentMessage:
			fmt.Print(contentBlockText(pl.Content))
		case shimapi.EventTypeAgentThinking:
			fmt.Fprintf(os.Stderr, "\033[2m[thinking seq=%d] %s\033[0m\n", ev.Seq, contentBlockText(pl.Content))
		}
	case shimapi.ToolCallEvent:
		content, _ := json.Marshal(pl)
		fmt.Fprintf(os.Stderr, "\033[33m[tool_call seq=%d] %s\033[0m\n", ev.Seq, content)
	case shimapi.ToolResultEvent:
		content, _ := json.Marshal(pl)
		fmt.Fprintf(os.Stderr, "\033[2m[tool_result seq=%d] %s\033[0m\n", ev.Seq, content)
	case shimapi.TurnEndEvent:
		fmt.Println()
	default:
		content, _ := json.Marshal(ev.Payload)
		fmt.Fprintf(os.Stderr, "[%s seq=%d] %s\n", ev.Type, ev.Seq, content)
	}
}

// ── Prompt / chat helpers ──────────────────────────────────────────────────

func runPrompt(sock, text string) error {
	notifs := make(chan jsonrpc.NotificationMsg, 1024)
	ctx := context.Background()

	sc, err := dialShim(ctx, sock, jsonrpc.WithNotificationChannel(notifs))
	if err != nil {
		return err
	}
	defer sc.Close()

	if _, err := sc.Subscribe(ctx, nil); err != nil {
		return fmt.Errorf("session/subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	turnEnd := startNotificationPrinter(ctx, notifs)
	drainTurnEnd(turnEnd)

	result, err := sc.Prompt(ctx, &shimapi.SessionPromptParams{Prompt: text})
	if err != nil {
		return fmt.Errorf("session/prompt: %w", err)
	}
	<-turnEnd

	if result.StopReason != "" {
		fmt.Fprintf(os.Stderr, "\n[stop: %s]\n", result.StopReason)
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
			sc, err := dialShim(cmd.Context(), socket)
			if err != nil {
				return err
			}
			defer sc.Close()
			result, err := sc.Status(cmd.Context())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	})

	var fromSeq int
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Print replayable event history (runtime/history)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc, err := dialShim(cmd.Context(), socket)
			if err != nil {
				return err
			}
			defer sc.Close()
			params := &shimapi.RuntimeHistoryParams{}
			if cmd.Flags().Changed("from-seq") {
				params.FromSeq = &fromSeq
			}
			result, err := sc.History(cmd.Context(), params)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
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
			sc, err := dialShim(cmd.Context(), socket)
			if err != nil {
				return err
			}
			defer sc.Close()
			err = sc.Stop(cmd.Context())
			if err == nil {
				fmt.Println("stop sent")
			}
			return err
		},
	})

	return cmd
}
