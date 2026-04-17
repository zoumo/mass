// Package agentrun provides agentrun lifecycle management commands.
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
)

// NewCommand returns the "agentrun" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentrun",
		Short: "Agent-run lifecycle management",
	}

	cmd.AddCommand(newCreateCmd(getClient))
	cmd.AddCommand(newListCmd(getClient))
	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(newPromptCmd(getClient))
	cmd.AddCommand(newStopCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newCancelCmd(getClient))
	cmd.AddCommand(newRestartCmd(getClient))
	cmd.AddCommand(newChatCmd(getClient))
	cmd.AddCommand(newDebugCmd())
	return cmd
}

func newCreateCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws            string
		name          string
		agent         string
		restartPolicy string
		systemPrompt  string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ar := pkgariapi.AgentRun{
				Metadata: pkgariapi.ObjectMeta{
					Workspace: ws,
					Name:      name,
				},
				Spec: pkgariapi.AgentRunSpec{
					Agent:         agent,
					RestartPolicy: restartPolicy,
					SystemPrompt:  systemPrompt,
				},
			}
			if err := client.Create(context.Background(), &ar); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(ar)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name within the workspace (required)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent definition name (required)")
	cmd.Flags().StringVar(&restartPolicy, "restart-policy", "", "Restart policy: try_reload, always_new (default: always_new)")
	cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt for the agent run")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func newListCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws    string
		state string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var opts []pkgariapi.ListOption
			if ws != "" {
				opts = append(opts, pkgariapi.InWorkspace(ws))
			}
			if state != "" {
				opts = append(opts, pkgariapi.WithState(state))
			}
			var list pkgariapi.AgentRunList
			if err := client.List(context.Background(), &list, opts...); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(list)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Filter by workspace name")
	cmd.Flags().StringVar(&state, "state", "", "Filter by state")
	return cmd
}

func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get agent run details",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var ar pkgariapi.AgentRun
			if err := client.Get(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}, &ar); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(ar)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newPromptCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
		text string
		wait bool
	)
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Send prompt to agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			key := pkgariapi.ObjectKey{Workspace: ws, Name: name}

			if !wait {
				result, err := client.AgentRuns().Prompt(ctx, key, []pkgariapi.ContentBlock{pkgariapi.TextBlock(text)})
				if err != nil {
					cliutil.HandleError(err)
					return nil
				}
				cliutil.OutputJSON(result)
				return nil
			}

			// --wait mode: get socket path, connect directly, watch events,
			// send prompt, collect agent_message until turn_end.

			// Resolve the agent-run socket path via ARI.
			var ar pkgariapi.AgentRun
			if err := client.Get(ctx, key, &ar); err != nil {
				return fmt.Errorf("agentrun/get: %w", err)
			}
			if ar.Status.Run == nil || ar.Status.Run.SocketPath == "" {
				return fmt.Errorf("agent run %s/%s has no run socket (state: %s)", ws, name, ar.Status.State)
			}

			// Connect to the agent-run process directly.
			runClient, err := runclient.Dial(ctx, ar.Status.Run.SocketPath)
			if err != nil {
				return fmt.Errorf("dial agent-run: %w", err)
			}
			defer runClient.Close()

			// Start watching events before sending prompt to avoid missing any.
			watcher, err := runClient.WatchEvent(ctx, nil)
			if err != nil {
				return fmt.Errorf("watch_event: %w", err)
			}
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
						fmt.Println(strings.Join(parts, ""))
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
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Prompt text (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for turn to complete and print agent response")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}

func newStopCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop an agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.AgentRuns().Stop(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s/%s stopped\n", ws, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Delete(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}, &pkgariapi.AgentRun{}); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s/%s deleted\n", ws, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newCancelCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel current agent run prompt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.AgentRuns().Cancel(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s/%s cancel requested\n", ws, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newRestartCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart a stopped agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			result, err := client.AgentRuns().Restart(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name})
			if err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
