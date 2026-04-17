// Package workspace provides workspace management commands.
package workspace

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace/create"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
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

			var list pkgariapi.WorkspaceList
			if err := client.List(context.Background(), &list); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(list)
			return nil
		},
	}
}

func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get workspace details",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			var ws pkgariapi.Workspace
			if err := client.Get(context.Background(), pkgariapi.ObjectKey{Name: name}, &ws); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(ws)
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

			if err := client.Delete(context.Background(), pkgariapi.ObjectKey{Name: name}, &pkgariapi.Workspace{}); err != nil {
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
		ws   string
		from string
		to   string
		text string
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

			result, err := client.Workspaces().Send(context.Background(), &pkgariapi.WorkspaceSendParams{
				Workspace: ws,
				From:      from,
				To:        to,
				Message:   []pkgariapi.ContentBlock{pkgariapi.TextBlock(text)},
			})
			if err != nil {
				cliutil.HandleError(err)
				return nil
			}
			fmt.Printf("Message delivered: %v\n", result.Delivered)
			return nil
		},
	}
	cmd.Flags().StringVarP(&ws, "name", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&from, "from", "", "Sender agent name (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target agent name (required)")
	cmd.Flags().StringVar(&text, "text", "", "Message text (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}
