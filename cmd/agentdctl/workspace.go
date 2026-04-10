// Package main provides workspace management commands for the agentdctl CLI.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// workspaceCmd is the root command for workspace management operations.
var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Workspace management commands",
}

// workspaceCreateCmd creates a workspace from the specified source.
var workspaceCreateCmd = &cobra.Command{
	Use:   "create <type> <name>",
	Short: "Create a workspace from a source type",
	Long: `Create a workspace of the given source type with the given name.

Source types:
  git       Clone a remote git repository
  emptyDir  Create an empty directory workspace
  local     Mount a local directory as a workspace

Examples:
  agentdctl workspace create emptyDir myws
  agentdctl workspace create local myws --path /tmp/mydir
  agentdctl workspace create git myws --url https://github.com/org/repo --ref main`,
	Args: cobra.ExactArgs(2),
	RunE: runWorkspaceCreate,
}

// workspaceListCmd lists all workspaces in the registry.
var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	RunE:  runWorkspaceList,
}

// workspaceDeleteCmd deletes a workspace.
var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceDelete,
}

// workspaceSendCmd sends a message from one agent to another within a workspace.
var workspaceSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message from one agent to another within a workspace",
	RunE:  runWorkspaceSend,
}

// Flags for workspace create command.
var (
	wsCreateGitURL    string
	wsCreateGitRef    string
	wsCreateGitDepth  int
	wsCreateLocalPath string
)

// Flags for workspace send command.
var (
	wsSendWorkspace string
	wsSendFrom      string
	wsSendTo        string
	wsSendText      string
)

func init() {
	workspaceCreateCmd.Flags().StringVar(&wsCreateGitURL, "url", "", "Git repository URL (required for git type)")
	workspaceCreateCmd.Flags().StringVar(&wsCreateGitRef, "ref", "", "Git reference (branch, tag, or commit)")
	workspaceCreateCmd.Flags().IntVar(&wsCreateGitDepth, "depth", 0, "Git shallow clone depth (0 = full clone)")
	workspaceCreateCmd.Flags().StringVar(&wsCreateLocalPath, "path", "", "Local directory path (required for local type)")

	workspaceSendCmd.Flags().StringVar(&wsSendWorkspace, "workspace", "", "Workspace name (required)")
	workspaceSendCmd.Flags().StringVar(&wsSendFrom, "from", "", "Sender agent name (required)")
	workspaceSendCmd.Flags().StringVar(&wsSendTo, "to", "", "Target agent name (required)")
	workspaceSendCmd.Flags().StringVar(&wsSendText, "text", "", "Message text (required)")
	_ = workspaceSendCmd.MarkFlagRequired("workspace")
	_ = workspaceSendCmd.MarkFlagRequired("from")
	_ = workspaceSendCmd.MarkFlagRequired("to")
	_ = workspaceSendCmd.MarkFlagRequired("text")

	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceDeleteCmd)
	workspaceCmd.AddCommand(workspaceSendCmd)
}

// runWorkspaceCreate creates a workspace via the ARI workspace/create method.
// args[0] is the source type (git, emptyDir, local); args[1] is the workspace name.
func runWorkspaceCreate(cmd *cobra.Command, args []string) error {
	wsType := args[0]
	wsName := args[1]

	switch wsType {
	case "git":
		if wsCreateGitURL == "" {
			return fmt.Errorf("--url is required for git source type")
		}
	case "emptyDir":
		// No additional flags required.
	case "local":
		if wsCreateLocalPath == "" {
			return fmt.Errorf("--path is required for local source type")
		}
	default:
		return fmt.Errorf("unknown source type %q (valid: git, emptyDir, local)", wsType)
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	// Build Source from flags.
	var src workspace.Source
	switch wsType {
	case "git":
		src = workspace.Source{
			Type: workspace.SourceTypeGit,
			Git:  workspace.GitSource{URL: wsCreateGitURL, Ref: wsCreateGitRef, Depth: wsCreateGitDepth},
		}
	case "emptyDir":
		src = workspace.Source{Type: workspace.SourceTypeEmptyDir}
	case "local":
		src = workspace.Source{Type: workspace.SourceTypeLocal, Local: workspace.LocalSource{Path: wsCreateLocalPath}}
	}

	srcJSON, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("marshal source: %w", err)
	}

	params := ari.WorkspaceCreateParams{
		Name:   wsName,
		Source: srcJSON,
	}

	var result ari.WorkspaceCreateResult
	if err := client.Call("workspace/create", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runWorkspaceList lists all workspaces via the ARI workspace/list method.
func runWorkspaceList(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.WorkspaceListParams{}
	var result ari.WorkspaceListResult
	if err := client.Call("workspace/list", params, &result); err != nil {
		handleError(err)
		return nil
	}

	outputJSON(result)
	return nil
}

// runWorkspaceDelete deletes a workspace via the ARI workspace/delete method.
func runWorkspaceDelete(cmd *cobra.Command, args []string) error {
	name := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.WorkspaceDeleteParams{Name: name}
	if err := client.Call("workspace/delete", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Workspace %s deleted\n", name)
	return nil
}

// runWorkspaceSend sends a message from one agent to another within a workspace.
func runWorkspaceSend(cmd *cobra.Command, args []string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.WorkspaceSendParams{
		Workspace: wsSendWorkspace,
		From:      wsSendFrom,
		To:        wsSendTo,
		Message:   wsSendText,
	}

	var result ari.WorkspaceSendResult
	if err := client.Call("workspace/send", params, &result); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Message delivered: %v\n", result.Delivered)
	return nil
}
