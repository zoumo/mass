// Package agentrun provides agentrun lifecycle management commands.
package agentrun

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
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

			params := ari.AgentRunCreateParams{
				Workspace:     workspace,
				Name:          name,
				Agent:         agent,
				RestartPolicy: restartPolicy,
				SystemPrompt:  systemPrompt,
			}
			var result ari.AgentRunCreateResult
			if err := client.Call("agentrun/create", params, &result); err != nil {
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
	cmd.Flags().StringVar(&restartPolicy, "restart-policy", "", "Restart policy: never, on-failure, always")
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

			params := ari.AgentRunListParams{Workspace: workspace, State: state}
			var result ari.AgentRunListResult
			if err := client.Call("agentrun/list", params, &result); err != nil {
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

			params := ari.AgentRunStatusParams{Workspace: workspace, Name: name}
			var result ari.AgentRunStatusResult
			if err := client.Call("agentrun/status", params, &result); err != nil {
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

			params := ari.AgentRunPromptParams{Workspace: workspace, Name: name, Prompt: text}
			var result ari.AgentRunPromptResult
			if err := client.Call("agentrun/prompt", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)

			if wait && result.Accepted {
				fmt.Println("Waiting for agent run to finish processing...")
				observedRunning := true
				for {
					time.Sleep(500 * time.Millisecond)
					var statusResult ari.AgentRunStatusResult
					if err := client.Call("agentrun/status", ari.AgentRunStatusParams{Workspace: workspace, Name: name}, &statusResult); err != nil {
						fmt.Printf("agentrun/status error: %v\n", err)
						break
					}
					if statusResult.AgentRun.State == "running" {
						observedRunning = true
						continue
					}
					if observedRunning {
						fmt.Printf("Agent run state: %s\n", statusResult.AgentRun.State)
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

			if err := client.Call("agentrun/stop", ari.AgentRunStopParams{Workspace: workspace, Name: name}, nil); err != nil {
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

			if err := client.Call("agentrun/delete", ari.AgentRunDeleteParams{Workspace: workspace, Name: name}, nil); err != nil {
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

			params := ari.AgentRunAttachParams{Workspace: workspace, Name: name}
			var result ari.AgentRunAttachResult
			if err := client.Call("agentrun/attach", params, &result); err != nil {
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

			if err := client.Call("agentrun/cancel", ari.AgentRunCancelParams{Workspace: workspace, Name: name}, nil); err != nil {
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

			params := ari.AgentRunRestartParams{Workspace: workspace, Name: name}
			var result ari.AgentRunRestartResult
			if err := client.Call("agentrun/restart", params, &result); err != nil {
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
