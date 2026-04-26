package compose

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// newApplyCmd returns the "compose apply" subcommand that creates a workspace
// and all agent runs from a declarative YAML file.
func newApplyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create workspace and agent runs from a declarative config file",
		Long: `apply reads a workspace-compose YAML file and creates the workspace and all agent runs.
It waits for the workspace to be ready and each agent to reach idle state,
then prints the run socket path for each agent.

  massctl compose apply -f compose.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading config %q: %w", file, err)
			}
			cfg, err := parseConfig(data)
			if err != nil {
				return err
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			wsName := cfg.Metadata.Name
			src, err := buildSource(cfg.Spec.Source)
			if err != nil {
				return err
			}
			if _, err := cliutil.CreateWorkspace(ctx, client, wsName, src); err != nil {
				return err
			}
			if err := cliutil.WaitWorkspaceReady(ctx, client, wsName); err != nil {
				return err
			}
			for _, a := range cfg.Spec.Runs {
				if err := createAgentRun(ctx, client, wsName, a); err != nil {
					return err
				}
			}
			for _, a := range cfg.Spec.Runs {
				if err := cliutil.WaitAgentIdle(ctx, client, wsName, a.Name); err != nil {
					return err
				}
			}

			fmt.Println("\nAll agents are ready. Socket info:")
			for _, a := range cfg.Spec.Runs {
				printSocketInfo(ctx, client, wsName, a.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace-compose YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
