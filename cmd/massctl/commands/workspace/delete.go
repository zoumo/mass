package workspace

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete name [name ...]",
		Short: "Delete one or more workspaces",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			for _, name := range args {
				if force {
					if err := forceCleanAgentRuns(ctx, cmd, client, name); err != nil {
						return fmt.Errorf("force-cleaning workspace %q: %w", name, err)
					}
				}
				if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: name}, &pkgariapi.Workspace{}); err != nil {
					return fmt.Errorf("deleting workspace %q: %w", name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "workspace %q deleted\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Stop and delete all agent runs in the workspace before deleting it")
	return cmd
}

// forceCleanAgentRuns stops and then deletes every agent run in the workspace.
func forceCleanAgentRuns(ctx context.Context, cmd *cobra.Command, client pkgariapi.Client, workspace string) error {
	var list pkgariapi.AgentRunList
	if err := client.List(ctx, &list, pkgariapi.InWorkspace(workspace)); err != nil {
		return fmt.Errorf("listing agent runs: %w", err)
	}
	for _, ar := range list.Items {
		key := pkgariapi.ObjectKey{Workspace: workspace, Name: ar.Metadata.Name}
		// Best-effort stop; ignore errors for runs that are already stopped.
		_ = client.AgentRuns().Stop(ctx, key)
		if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
			return fmt.Errorf("deleting agentrun %q: %w", ar.Metadata.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "agentrun %q deleted\n", ar.Metadata.Name)
	}
	return nil
}
