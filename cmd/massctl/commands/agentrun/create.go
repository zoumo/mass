package agentrun

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

func newCreateCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws           string
		name         string
		agent        string
		systemPrompt string
		permissions  string
		workflowFile string
		wait         bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new agent run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ar := pkgariapi.AgentRun{
				Metadata: pkgariapi.ObjectMeta{
					Workspace: ws,
					Name:      name,
				},
				Spec: pkgariapi.AgentRunSpec{
					Agent:        agent,
					SystemPrompt: systemPrompt,
					Permissions:  apiruntime.PermissionPolicy(permissions),
					WorkflowFile: workflowFile,
				},
			}
			ctx := context.Background()
			if err := cliutil.CreateAgentRun(ctx, client, &ar); err != nil {
				return err
			}
			if !wait {
				return nil
			}
			if err := cliutil.WaitAgentIdle(ctx, client, ws, name); err != nil {
				return err
			}
			if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: ws, Name: name}, &ar); err != nil {
				return err
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), ar)
		},
	}
	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required)")
	cmd.Flags().StringVar(&name, "name", "", "Agent name within the workspace (required)")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent definition name (required)")
	cmd.Flags().StringVar(&systemPrompt, "system-prompt", "", "System prompt for the agent run")
	cmd.Flags().StringVar(&permissions, "permissions", "", "Permission policy: approve_all, approve_reads, deny_all (default: approve_all)")
	cmd.Flags().StringVar(&workflowFile, "workflow", "", "Path to a workflow definition file (copied to bundle)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the agent run to become idle")
	_ = cmd.MarkFlagRequired("workspace")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}
