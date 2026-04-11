// Package workspace provides workspace management commands.
// S03 (M010) will reshape the command UX; this file is the S02 behavior-preserving port.
package workspace

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// NewCommand returns the "workspace" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace management commands",
	}
	cmd.AddCommand(newCreateCmd(getClient))
	cmd.AddCommand(newListCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newSendCmd(getClient))
	return cmd
}

func newCreateCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		gitURL    string
		gitRef    string
		gitDepth  int
		localPath string
	)
	cmd := &cobra.Command{
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
		RunE: func(cmd *cobra.Command, args []string) error {
			wsType := args[0]
			wsName := args[1]

			switch wsType {
			case "git":
				if gitURL == "" {
					return fmt.Errorf("--url is required for git source type")
				}
			case "emptyDir":
				// no additional flags
			case "local":
				if localPath == "" {
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

			var src workspace.Source
			switch wsType {
			case "git":
				src = workspace.Source{
					Type: workspace.SourceTypeGit,
					Git:  workspace.GitSource{URL: gitURL, Ref: gitRef, Depth: gitDepth},
				}
			case "emptyDir":
				src = workspace.Source{Type: workspace.SourceTypeEmptyDir}
			case "local":
				src = workspace.Source{Type: workspace.SourceTypeLocal, Local: workspace.LocalSource{Path: localPath}}
			}

			srcJSON, err := json.Marshal(src)
			if err != nil {
				return fmt.Errorf("marshal source: %w", err)
			}

			params := ari.WorkspaceCreateParams{Name: wsName, Source: srcJSON}
			var result ari.WorkspaceCreateResult
			if err := client.Call("workspace/create", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&gitURL, "url", "", "Git repository URL (required for git type)")
	cmd.Flags().StringVar(&gitRef, "ref", "", "Git reference (branch, tag, or commit)")
	cmd.Flags().IntVar(&gitDepth, "depth", 0, "Git shallow clone depth (0 = full clone)")
	cmd.Flags().StringVar(&localPath, "path", "", "Local directory path (required for local type)")
	return cmd
}

func newListCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var result ari.WorkspaceListResult
			if err := client.Call("workspace/list", ari.WorkspaceListParams{}, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Call("workspace/delete", ari.WorkspaceDeleteParams{Name: args[0]}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Workspace %s deleted\n", args[0])
			return nil
		},
	}
}

func newSendCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		wsWorkspace string
		from        string
		to          string
		text        string
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a message from one agent to another within a workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.WorkspaceSendParams{
				Workspace: wsWorkspace,
				From:      from,
				To:        to,
				Message:   text,
			}
			var result ari.WorkspaceSendResult
			if err := client.Call("workspace/send", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Message delivered: %v\n", result.Delivered)
			return nil
		},
	}
	cmd.Flags().StringVar(&wsWorkspace, "workspace", "", "Workspace name (required)")
	cmd.Flags().StringVar(&from, "from", "", "Sender agent name (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target agent name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Message text (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}
