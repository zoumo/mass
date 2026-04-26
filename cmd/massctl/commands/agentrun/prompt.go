package agentrun

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/watch"
)

func newPromptCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		text string
		wait bool
	)
	cmd := &cobra.Command{
		Use:   "prompt name",
		Short: "Send prompt to agent run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			name := args[0]
			ctx := context.Background()
			key := pkgariapi.ObjectKey{Workspace: ws, Name: name}

			if !wait {
				result, err := client.AgentRuns().Prompt(ctx, key, []runapi.ContentBlock{runapi.TextBlock(text)})
				if err != nil {
					return err
				}
				return cliutil.PrintJSON(cmd.OutOrStdout(), result)
			}

			// --wait mode: get socket path, connect directly, watch events,
			// send prompt, collect agent_message until turn_end.

			// Resolve the agent-run socket path via ARI.
			var ar pkgariapi.AgentRun
			if err := client.Get(ctx, key, &ar); err != nil {
				return fmt.Errorf("agentrun/get: %w", err)
			}
			if ar.Status.SocketPath == "" {
				return fmt.Errorf("agent run %s/%s has no run socket (state: %s)", ws, name, ar.Status.Phase)
			}

			// Connect to the agent-run process directly.
			runClient, err := runclient.Dial(ctx, ar.Status.SocketPath)
			if err != nil {
				return fmt.Errorf("dial agent-run: %w", err)
			}
			defer runClient.Close()

			// Start watching events before sending prompt to avoid missing any.
			watcher := watch.NewRetryWatcher(
				ctx,
				runclient.NewWatchFunc(ar.Status.SocketPath),
				-1,
				func(ev runapi.AgentRunEvent) int { return ev.Seq },
				1024,
			)
			defer watcher.Stop()

			// Send prompt (fire-and-forget).
			if err := runClient.SendPrompt(ctx, &runapi.SessionPromptParams{
				Prompt: []runapi.ContentBlock{runapi.TextBlock(text)},
			}); err != nil {
				return fmt.Errorf("send_prompt: %w", err)
			}

			// Collect agent_message text until turn_end.
			var parts []string
			timeout := time.After(5 * time.Minute)
			for {
				select {
				case ev, ok := <-watcher.ResultChan():
					if !ok {
						return fmt.Errorf("event stream closed before turn_end")
					}
					if ev.Type == runapi.EventTypeTurnEnd {
						fmt.Fprintln(cmd.OutOrStdout(), strings.Join(parts, ""))
						return nil
					}
					if ev.Type == runapi.EventTypeAgentMessage {
						if ce, ok := ev.Payload.(runapi.ContentEvent); ok && ce.Content.Text != nil {
							parts = append(parts, ce.Content.Text.Text)
						}
					}
				case <-timeout:
					return fmt.Errorf("timeout waiting for turn_end")
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Prompt text (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for turn to complete and print agent response")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}
