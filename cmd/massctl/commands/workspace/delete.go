package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
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

// isExited reports whether the agent run state is stopped or error.
func isExited(state apiruntime.Status) bool {
	return state == apiruntime.StatusStopped || state == apiruntime.StatusError
}

// forceCleanAgentRuns stops and then deletes every agent run in the workspace.
func forceCleanAgentRuns(ctx context.Context, cmd *cobra.Command, client pkgariapi.Client, workspace string) error {
	var list pkgariapi.AgentRunList
	if err := client.List(ctx, &list, pkgariapi.InWorkspace(workspace)); err != nil {
		return fmt.Errorf("listing agent runs: %w", err)
	}
	for _, ar := range list.Items {
		key := pkgariapi.ObjectKey{Workspace: workspace, Name: ar.Metadata.Name}

		if !isExited(ar.Status.Status) {
			if err := client.AgentRuns().Stop(ctx, key); err != nil {
				return fmt.Errorf("stopping agentrun %q: %w", ar.Metadata.Name, err)
			}
			if err := waitForExited(ctx, client, key); err != nil {
				return fmt.Errorf("waiting for agentrun %q to stop: %w", ar.Metadata.Name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agentrun %q stopped\n", ar.Metadata.Name)
		}

		if err := client.Delete(ctx, key, &pkgariapi.AgentRun{}); err != nil {
			return fmt.Errorf("deleting agentrun %q: %w", ar.Metadata.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "agentrun %q deleted\n", ar.Metadata.Name)
	}
	return nil
}

// waitForExited polls until the agent run reaches a terminal state (stopped/error).
func waitForExited(ctx context.Context, client pkgariapi.Client, key pkgariapi.ObjectKey) error {
	timeout := time.After(30 * time.Second)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timed out waiting for terminal state")
		case <-tick.C:
			var ar pkgariapi.AgentRun
			if err := client.Get(ctx, key, &ar); err != nil {
				return err
			}
			if isExited(ar.Status.Status) {
				return nil
			}
		}
	}
}
