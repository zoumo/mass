// Package main provides agent management commands for the agentdctl CLI.
// Agent commands allow creating, listing, prompting, stopping, and deleting agents.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/spf13/cobra"
)

// agentCmd is the root command for agent management operations.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Agent management commands",
}

// agentCreateCmd creates a new agent in a room.
var agentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent",
	RunE:  runAgentCreate,
}

// agentListCmd lists agents, optionally filtered by room or state.
var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents",
	RunE:  runAgentList,
}

// agentStatusCmd gets detailed status for a specific agent.
var agentStatusCmd = &cobra.Command{
	Use:   "status <agent-id>",
	Short: "Get agent status",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentStatus,
}

// agentPromptCmd sends a prompt to a running agent.
var agentPromptCmd = &cobra.Command{
	Use:   "prompt <agent-id>",
	Short: "Send prompt to agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentPrompt,
}

// agentStopCmd stops a running agent.
var agentStopCmd = &cobra.Command{
	Use:   "stop <agent-id>",
	Short: "Stop an agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentStop,
}

// agentDeleteCmd deletes an agent from the registry.
var agentDeleteCmd = &cobra.Command{
	Use:   "delete <agent-id>",
	Short: "Delete an agent",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentDelete,
}

// agentAttachCmd gets the shim socket path for attaching to an agent.
var agentAttachCmd = &cobra.Command{
	Use:   "attach <agent-id>",
	Short: "Get shim socket path for attaching",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentAttach,
}

// agentCancelCmd cancels the current prompt of a running agent.
var agentCancelCmd = &cobra.Command{
	Use:   "cancel <agent-id>",
	Short: "Cancel current agent prompt",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentCancel,
}

// agentRestartCmd restarts a stopped or errored agent.
var agentRestartCmd = &cobra.Command{
	Use:   "restart <agent-id>",
	Short: "Restart a stopped agent",
	Long: `Restart a stopped (or errored) agent.

The agent transitions immediately to "creating" state while its session is
re-bootstrapped in the background. Poll "agentdctl agent status <agent-id>"
until state is "created" (or "error") before sending prompts.`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentRestart,
}

// Flags for agent create command.
var (
	agentCreateRoom          string
	agentCreateName          string
	agentCreateWorkspaceId   string
	agentCreateRuntimeClass  string
	agentCreateDescription   string
	agentCreateSystemPrompt  string
)

// Flags for agent list command.
var (
	agentListRoom  string
	agentListState string
)

// Flags for agent prompt command.
var (
	agentPromptText string
	agentPromptWait bool
)

func init() {
	// Add flags to agentCreateCmd
	agentCreateCmd.Flags().StringVar(&agentCreateRoom, "room", "", "Room name for multi-agent coordination (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateName, "name", "", "Agent name within the room (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateWorkspaceId, "workspace-id", "", "Workspace ID (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateRuntimeClass, "runtime-class", "", "Runtime class (required)")
	agentCreateCmd.Flags().StringVar(&agentCreateDescription, "description", "", "Agent description")
	agentCreateCmd.Flags().StringVar(&agentCreateSystemPrompt, "system-prompt", "", "System prompt for the agent")
	_ = agentCreateCmd.MarkFlagRequired("room")
	_ = agentCreateCmd.MarkFlagRequired("name")
	_ = agentCreateCmd.MarkFlagRequired("workspace-id")
	_ = agentCreateCmd.MarkFlagRequired("runtime-class")

	// Add flags to agentListCmd
	agentListCmd.Flags().StringVar(&agentListRoom, "room", "", "Filter agents by room name")
	agentListCmd.Flags().StringVar(&agentListState, "state", "", "Filter agents by state")

	// Add flags to agentPromptCmd
	agentPromptCmd.Flags().StringVar(&agentPromptText, "text", "", "Prompt text (required)")
	_ = agentPromptCmd.MarkFlagRequired("text")
	agentPromptCmd.Flags().BoolVar(&agentPromptWait, "wait", false, "Poll agent/status until state is no longer 'running' after dispatch")

	// Add subcommands to agentCmd
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

// runAgentCreate creates a new agent via the ARI agent/create method.
func runAgentCreate(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentCreateParams{
		Room:         agentCreateRoom,
		Name:         agentCreateName,
		WorkspaceId:  agentCreateWorkspaceId,
		RuntimeClass: agentCreateRuntimeClass,
		Description:  agentCreateDescription,
		SystemPrompt: agentCreateSystemPrompt,
	}

	var result ari.AgentCreateResult
	if err := client.Call("agent/create", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runAgentList lists agents via the ARI agent/list method.
func runAgentList(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentListParams{
		Room:  agentListRoom,
		State: agentListState,
	}
	var result ari.AgentListResult
	if err := client.Call("agent/list", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runAgentStatus gets status for a specific agent.
func runAgentStatus(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentStatusParams{AgentId: agentId}
	var result ari.AgentStatusResult
	if err := client.Call("agent/status", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runAgentPrompt sends a prompt to an agent asynchronously.
// Returns immediately with {accepted: true} once the prompt is dispatched.
// With --wait, polls agent/status until the agent state is no longer "running".
func runAgentPrompt(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentPromptParams{
		AgentId: agentId,
		Prompt:  agentPromptText,
	}
	var result ari.AgentPromptResult
	if err := client.Call("agent/prompt", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)

	if agentPromptWait && result.Accepted {
		fmt.Println("Waiting for agent to finish processing...")
		ctx := context.Background()
		for {
			time.Sleep(500 * time.Millisecond)
			var statusResult ari.AgentStatusResult
			if err := client.Call("agent/status", ari.AgentStatusParams{AgentId: agentId}, &statusResult); err != nil {
				fmt.Printf("agent/status error: %v\n", err)
				break
			}
			if statusResult.Agent.State != "running" {
				fmt.Printf("Agent state: %s\n", statusResult.Agent.State)
				break
			}
		}
		_ = ctx
	}

	return nil
}

// runAgentStop stops a running agent.
func runAgentStop(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentStopParams{AgentId: agentId}
	if err := client.Call("agent/stop", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s stopped\n", agentId)
	return nil
}

// runAgentDelete deletes an agent from the registry.
func runAgentDelete(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentDeleteParams{AgentId: agentId}
	if err := client.Call("agent/delete", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s deleted\n", agentId)
	return nil
}

// runAgentAttach gets the shim socket path for attaching to an agent.
func runAgentAttach(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentAttachParams{AgentId: agentId}
	var result ari.AgentAttachResult
	if err := client.Call("agent/attach", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runAgentCancel cancels the current prompt of a running agent.
func runAgentCancel(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentCancelParams{AgentId: agentId}
	if err := client.Call("agent/cancel", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Agent %s cancel requested\n", agentId)
	return nil
}

// runAgentRestart restarts a stopped or errored agent via the ARI agent/restart method.
// The agent immediately transitions to "creating" state while re-bootstrapping in the
// background. Use "agentdctl agent status <agent-id>" to poll until state is "created".
func runAgentRestart(cmd *cobra.Command, args []string) error {
	agentId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.AgentRestartParams{AgentId: agentId}
	var result ari.AgentRestartResult
	if err := client.Call("agent/restart", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}
