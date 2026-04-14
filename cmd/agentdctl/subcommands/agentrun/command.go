// Package agentrun provides agentrun lifecycle management commands.
package agentrun

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pkgariapi "github.com/zoumo/oar/pkg/ari/api"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
)

// NewCommand returns the "agentrun" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentrun",
		Short: "Agent-run lifecycle management",
	}

	cmd.AddCommand(newCreateCmd(getClient))
	cmd.AddCommand(newListCmd(getClient))
	cmd.AddCommand(newStatusCmd(getClient))
	cmd.AddCommand(newPromptCmd(getClient))
	cmd.AddCommand(newStopCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newAttachCmd(getClient))
	cmd.AddCommand(newCancelCmd(getClient))
	cmd.AddCommand(newRestartCmd(getClient))
	return cmd
}

func newCreateCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace     string
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

			params := pkgariapi.AgentRunCreateParams{
				Workspace:     workspace,
				Name:          name,
				Agent:         agent,
				RestartPolicy: restartPolicy,
				SystemPrompt:  systemPrompt,
			}
			var result pkgariapi.AgentRunCreateResult
			if err := client.Call(pkgariapi.MethodAgentRunCreate, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
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
		workspace string
		state     string
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

			params := pkgariapi.AgentRunListParams{Workspace: workspace, State: state}
			var result pkgariapi.AgentRunListResult
			if err := client.Call(pkgariapi.MethodAgentRunList, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Filter by workspace name")
	cmd.Flags().StringVar(&state, "state", "", "Filter by state")
	return cmd
}

func newStatusCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Get agent run status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := pkgariapi.AgentRunStatusParams{Workspace: workspace, Name: name}
			var result pkgariapi.AgentRunStatusResult
			if err := client.Call(pkgariapi.MethodAgentRunStatus, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newPromptCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
		text      string
		wait      bool
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

			params := pkgariapi.AgentRunPromptParams{Workspace: workspace, Name: name, Prompt: text}
			var result pkgariapi.AgentRunPromptResult
			if err := client.Call(pkgariapi.MethodAgentRunPrompt, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)

			if wait && result.Accepted {
				fmt.Println("Waiting for agent run to finish processing...")
				observedRunning := true
				for {
					time.Sleep(500 * time.Millisecond)
					var statusResult pkgariapi.AgentRunStatusResult
					if err := client.Call(pkgariapi.MethodAgentRunStatus, pkgariapi.AgentRunStatusParams{Workspace: workspace, Name: name}, &statusResult); err != nil {
						fmt.Printf("agentrun/status error: %v\n", err)
						break
					}
					if statusResult.AgentRun.Status.State == "running" {
						observedRunning = true
						continue
					}
					if observedRunning {
						fmt.Printf("Agent run state: %s\n", statusResult.AgentRun.Status.State)
						break
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Prompt text (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Poll agentrun/status until state is no longer 'running'")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}

func newStopCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
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

			if err := client.Call(pkgariapi.MethodAgentRunStop, pkgariapi.AgentRunStopParams{Workspace: workspace, Name: name}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s stopped\n", fmt.Sprintf("%s/%s", workspace, name))
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
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

			if err := client.Call(pkgariapi.MethodAgentRunDelete, pkgariapi.AgentRunDeleteParams{Workspace: workspace, Name: name}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s deleted\n", fmt.Sprintf("%s/%s", workspace, name))
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newAttachCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
	)
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Get shim socket path for attaching",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := pkgariapi.AgentRunAttachParams{Workspace: workspace, Name: name}
			var result pkgariapi.AgentRunAttachResult
			if err := client.Call(pkgariapi.MethodAgentRunAttach, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newCancelCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
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

			if err := client.Call(pkgariapi.MethodAgentRunCancel, pkgariapi.AgentRunCancelParams{Workspace: workspace, Name: name}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Agent run %s cancel requested\n", fmt.Sprintf("%s/%s", workspace, name))
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newRestartCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		name      string
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

			params := pkgariapi.AgentRunRestartParams{Workspace: workspace, Name: name}
			var result pkgariapi.AgentRunRestartResult
			if err := client.Call(pkgariapi.MethodAgentRunRestart, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
