package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/tui/chat"
)

func newChatCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws   string
		name string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Interactive chat with a running agent (resolves run socket via ARI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}

			var ar pkgariapi.AgentRun
			if err := client.Get(context.Background(), pkgariapi.ObjectKey{Workspace: ws, Name: name}, &ar); err != nil {
				client.Close()
				return fmt.Errorf("agentrun/get %s/%s: %w", ws, name, err)
			}
			client.Close()

			if ar.Status.Run == nil || ar.Status.Run.SocketPath == "" {
				return fmt.Errorf("agent run %s/%s has no run socket (state: %s)", ws, name, ar.Status.State)
			}

			return chat.RunChatTUI(ar.Status.Run.SocketPath)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent run name (required)")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
