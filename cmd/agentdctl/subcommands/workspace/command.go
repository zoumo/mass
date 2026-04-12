// Package workspace provides workspace management commands.
package workspace

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/workspace/create"
	"github.com/open-agent-d/open-agent-d/api/ari"
)

// NewCommand returns the "workspace" cobra command.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace management commands",
	}
	cmd.AddCommand(create.NewCommand(getClient))
	cmd.AddCommand(newListCmd(getClient))
	cmd.AddCommand(newGetCmd(getClient))
	cmd.AddCommand(newDeleteCmd(getClient))
	cmd.AddCommand(newSendCmd(getClient))
	return cmd
}

func newListCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		Args:  cobra.NoArgs,
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

func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get workspace status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.WorkspaceStatusParams{Name: name}
			var result ari.WorkspaceStatusResult
			if err := client.Call("workspace/status", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Call("workspace/delete", ari.WorkspaceDeleteParams{Name: name}, nil); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Workspace %s deleted\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newSendCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		workspace string
		from      string
		to        string
		text      string
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a message from one agent to another within a workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.WorkspaceSendParams{
				Workspace: workspace,
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
	cmd.Flags().StringVarP(&workspace, "name", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&from, "from", "", "Sender agent name (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target agent name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Message text (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}
