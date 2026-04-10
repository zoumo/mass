// Package main provides agent management commands for the agentdctl CLI.
// Agents are identified by workspace/name pairs (e.g. "my-workspace/my-agent").
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// agentCmd is the root command for agent management operations.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Agent management commands",
}

var agentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent",
	RunE:  runAgentCreate,
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents",
	RunE:  runAgentList,
}

var agentStatusCmd = &cobra.Command{
	Use:   "status <workspace/name>",
	Short: "Get agent status",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentStatus,
}

var agentPromptCmd = &cobra.Command{
	Use:   "prompt <workspace/name>",
	Short: "Send prompt to agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentPrompt,
}

var agentStopCmd = &cobra.Command{
	Use:   "stop <workspace/name>",
	Short: "Stop an agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentStop,
}

var agentDeleteCmd = &cobra.Command{
	Use:   "delete <workspace/name>",
	Short: "Delete an agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentDelete,
}

var agentAttachCmd = &cobra.Command{
	Use:   "attach <workspace/name>",
	Short: "Get shim socket path for attaching",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentAttach,
}

var agentCancelCmd = &cobra.Command{
	Use:   "cancel <workspace/name>",
	Short: "Cancel current agent prompt",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentCancel,
}

var agentRestartCmd = &cobra.Command{
	Use:   "restart <workspace/name>",
	Short: "Restart a stopped agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentRestart,
}

// Flags for agent create command.
var (
	agentCreateWorkspace     string
	agentCreateName          string
	agentCreateRuntimeClass  string
	agentCreateRestartPolicy string
	agentCreateSystemPrompt  string
)

// Flags for agent list command.
var (
	agentListWorkspace string
	agentListState     string
)

// Flags for agent prompt command.
var (
	agentPromptText string
	agentPromptWait bool
)

// parseAgentKey splits a "workspace/name" argument into (workspace, name).
func parseAgentKey(arg string) (workspace, name string, err error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("agent key must be in 'workspace/name' format, got %q", arg)
	}
	return parts[0], parts[1], nil
}

func init() {
	agentCreateCmd.Flags().StringVar(&agentCreateWorkspace, "workspace", "", "Workspace name (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateName, "name", "", "Agent name within the workspace (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateRuntimeClass, "runtime-class", "", "Runtime class (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateRestartPolicy, "restart-policy", "", "Restart policy: never, on-failure, always")
	agentCreateCmd.Flags().StringVar(&agentCreateSystemPrompt, "system-prompt", "", "System prompt for the agent")
	_ = agentCreateCmd.MarkFlagRequired("workspace")
	_ = agentCreateCmd.MarkFlagRequired("name")
	_ = agentCreateCmd.MarkFlagRequired("runtime-class")

	agentListCmd.Flags().StringVar(&agentListWorkspace, "workspace", "", "Filter agents by workspace name")
	agentListCmd.Flags().StringVar(&agentListState, "state", "", "Filter agents by state")

	agentPromptCmd.Flags().StringVar(&agentPromptText, "text", "", "Prompt text (required)")
	_ = agentPromptCmd.MarkFlagRequired("text")
	agentPromptCmd.Flags().BoolVar(&agentPromptWait, "wait", false, "Poll agent/status until state is no longer 'running' after dispatch")

	agentCmd.AddCommand(agentCreateCmd)
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentPromptCmd)
	agentCmd.AddCommand(agentStopCmd)
	agentCmd.AddCommand(agentDeleteCmd)
	agentCmd.AddCommand(agentAttachCmd)
	agentCmd.AddCommand(agentCancelCmd)
	agentCmd.AddCommand(agentRestartCmd)
}

func runAgentCreate(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunCreateParams{
		Workspace:     agentCreateWorkspace,
		Name:          agentCreateName,
		RuntimeClass:  agentCreateRuntimeClass,
		RestartPolicy: agentCreateRestartPolicy,
		SystemPrompt:  agentCreateSystemPrompt,
	}

	var result ari.AgentRunCreateResult
	if err := client.Call("agentrun/create", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

func runAgentList(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunListParams{
		Workspace: agentListWorkspace,
		State:     agentListState,
	}
	var result ari.AgentRunListResult
	if err := client.Call("agentrun/list", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

func runAgentStatus(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunStatusParams{Workspace: ws, Name: name}
	var result ari.AgentRunStatusResult
	if err := client.Call("agentrun/status", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

func runAgentPrompt(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunPromptParams{
		Workspace: ws,
		Name:      name,
		Prompt:    agentPromptText,
	}
	var result ari.AgentRunPromptResult
	if err := client.Call("agentrun/prompt", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)

	if agentPromptWait && result.Accepted {
		fmt.Println("Waiting for agent to finish processing...")
		for {
			time.Sleep(500 * time.Millisecond)
			var statusResult ari.AgentRunStatusResult
			statusParams := ari.AgentRunStatusParams{Workspace: ws, Name: name}
			if err := client.Call("agentrun/status", statusParams, &statusResult); err != nil {
				fmt.Printf("agent/status error: %v\n", err)
				break
			}
			if statusResult.Agent.State != "running" {
				fmt.Printf("Agent state: %s\n", statusResult.Agent.State)
				break
			}
		}
	}

	return nil
}

func runAgentStop(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunStopParams{Workspace: ws, Name: name}
	if err := client.Call("agentrun/stop", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s stopped\n", args[0])
	return nil
}

func runAgentDelete(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunDeleteParams{Workspace: ws, Name: name}
	if err := client.Call("agentrun/delete", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s deleted\n", args[0])
	return nil
}

func runAgentAttach(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunAttachParams{Workspace: ws, Name: name}
	var result ari.AgentRunAttachResult
	if err := client.Call("agentrun/attach", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

func runAgentCancel(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunCancelParams{Workspace: ws, Name: name}
	if err := client.Call("agentrun/cancel", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s cancel requested\n", args[0])
	return nil
}

func runAgentRestart(cmd *cobra.Command, args []string) error {
	ws, name, err := parseAgentKey(args[0])
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRunRestartParams{Workspace: ws, Name: name}
	var result ari.AgentRunRestartResult
	if err := client.Call("agentrun/restart", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}
