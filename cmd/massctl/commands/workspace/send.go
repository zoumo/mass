package workspace

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

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
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Message delivered: %v\n", result.Delivered)
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
