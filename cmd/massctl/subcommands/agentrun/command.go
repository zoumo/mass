// Package agentrun provides agentrun lifecycle management commands.
package agentrun

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/cmd/massctl/subcommands/cliutil"
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
			result, err := client.AgentRuns().Prompt(ctx, key, text)
			if err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)

			if wait && result.Accepted {
				fmt.Println("Waiting for agent run to finish processing...")
				observedRunning := true
				for {
					time.Sleep(500 * time.Millisecond)
					var ar pkgariapi.AgentRun
					if err := client.Get(ctx, key, &ar); err != nil {
						fmt.Printf("agentrun/get error: %v\n", err)
						break
					}
					if ar.Status.State == "running" {
						observedRunning = true
						continue
					}
					if observedRunning {
						fmt.Printf("Agent run state: %s\n", ar.Status.State)
						break
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Prompt text (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Poll agentrun/get until state is no longer 'running'")
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
