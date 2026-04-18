package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newApplyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply (create or update) an agent definition from a YAML file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading agent file %q: %w", file, err)
			}
			var ag pkgariapi.Agent
			if err := yaml.Unmarshal(data, &ag); err != nil {
				return fmt.Errorf("parsing agent YAML %q: %w", file, err)
			}
			if ag.Metadata.Name == "" {
				return fmt.Errorf("agent YAML must have a non-empty 'metadata.name' field")
			}
			if ag.Spec.Command == "" {
				return fmt.Errorf("agent YAML must have a non-empty 'spec.command' field")
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()

			// Try create first; if it already exists, update instead.
			if err := client.Create(ctx, &ag); err != nil {
				if err := client.Update(ctx, &ag); err != nil {
					return err
				}
			}
			return cliutil.PrintJSON(cmd.OutOrStdout(), ag)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to agent YAML file (required)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
