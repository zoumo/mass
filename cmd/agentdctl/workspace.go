// Package main provides workspace management commands for the agentdctl CLI.
// Workspace commands allow preparing, listing, and cleaning up workspaces.
package main

import (
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

// workspacePrepareCmd prepares a workspace from the specified source.
var workspacePrepareCmd = &cobra.Command{
	Use:   "prepare",
	Short: "Prepare a workspace",
	RunE:  runWorkspacePrepare,
}

// workspaceListCmd lists all workspaces in the registry.
var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	RunE:  runWorkspaceList,
}

// workspaceCleanupCmd cleans up a workspace.
var workspaceCleanupCmd = &cobra.Command{
	Use:   "cleanup <workspace-id>",
	Short: "Clean up a workspace",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkspaceCleanup,
}

// Flags for workspace prepare command.
var (
	wsPrepareName string
	wsPrepareType string // git, emptyDir, local

	// Git source flags
	wsPrepareGitURL   string
	wsPrepareGitRef   string
	wsPrepareGitDepth int

	// Local source flags
	wsPrepareLocalPath string
)

func init() {
	// Add flags to workspacePrepareCmd
	workspacePrepareCmd.Flags().StringVar(&wsPrepareName, "name", "", "Workspace name (required)")
	workspacePrepareCmd.Flags().StringVar(&wsPrepareType, "type", "emptyDir", "Source type: git, emptyDir, or local")
	workspacePrepareCmd.Flags().StringVar(&wsPrepareGitURL, "url", "", "Git repository URL (required for git type)")
	workspacePrepareCmd.Flags().StringVar(&wsPrepareGitRef, "ref", "", "Git reference (branch, tag, or commit)")
	workspacePrepareCmd.Flags().IntVar(&wsPrepareGitDepth, "depth", 0, "Git shallow clone depth (0 = full clone)")
	workspacePrepareCmd.Flags().StringVar(&wsPrepareLocalPath, "path", "", "Local directory path (required for local type)")
	_ = workspacePrepareCmd.MarkFlagRequired("name")

	// Add subcommands to workspaceCmd
	workspaceCmd.AddCommand(workspacePrepareCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceCleanupCmd)
}

// runWorkspacePrepare prepares a workspace via the ARI workspace/prepare method.
func runWorkspacePrepare(cmd *cobra.Command, args []string) error {
	// Validate type-specific required flags BEFORE connecting to client
	switch wsPrepareType {
	case "git":
		if wsPrepareGitURL == "" {
			return fmt.Errorf("--url is required for git source type")
		}
	case "emptyDir":
		// No additional flags required
	case "local":
		if wsPrepareLocalPath == "" {
			return fmt.Errorf("--path is required for local source type")
		}
	default:
		return fmt.Errorf("unknown source type %q (valid: git, emptyDir, local)", wsPrepareType)
	}

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	// Build the WorkspaceSpec from flags
	spec := workspace.WorkspaceSpec{
		OarVersion: "0.1.0",
		Metadata: workspace.WorkspaceMetadata{
			Name: wsPrepareName,
		},
	}

	// Set source based on type
	switch wsPrepareType {
	case "git":
		spec.Source = workspace.Source{
			Type: workspace.SourceTypeGit,
			Git: workspace.GitSource{
				URL:   wsPrepareGitURL,
				Ref:   wsPrepareGitRef,
				Depth: wsPrepareGitDepth,
			},
		}
	case "emptyDir":
		spec.Source = workspace.Source{
			Type: workspace.SourceTypeEmptyDir,
		}
	case "local":
		spec.Source = workspace.Source{
			Type: workspace.SourceTypeLocal,
			Local: workspace.LocalSource{
				Path: wsPrepareLocalPath,
			},
		}
	}

	params := ari.WorkspacePrepareParams{
		Spec: spec,
	}

	var result ari.WorkspacePrepareResult
	if err := client.Call("workspace/prepare", params, &result); err != nil {
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

// runWorkspaceCleanup cleans up a workspace via the ARI workspace/cleanup method.
func runWorkspaceCleanup(cmd *cobra.Command, args []string) error {
	workspaceId := args[0]

	client, err := getClient()
	if err != nil {
		return err
	}
	defer client.Close()

	params := ari.WorkspaceCleanupParams{WorkspaceId: workspaceId}
	if err := client.Call("workspace/cleanup", params, nil); err != nil {
		handleError(err)
		return nil
	}

	fmt.Printf("Workspace %s cleaned up\n", workspaceId)
	return nil
}
