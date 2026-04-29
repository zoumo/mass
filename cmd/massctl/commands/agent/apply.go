package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

func newApplyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		file     string
		disabled bool
	)

	cmd := &cobra.Command{
		Use:   "apply [-f file | name --disabled[=bool]]",
		Short: "Apply (create or update) an agent definition",
		Long: `Apply an agent definition from a YAML file, or update a single field inline.

  # From file (create or update):
  massctl agent apply -f agent.yaml

  # Inline disable/enable:
  massctl agent apply gsd-pi --disabled
  massctl agent apply gsd-pi --disabled=false`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hasFile := cmd.Flags().Changed("file")
			hasName := len(args) == 1

			if hasFile && hasName {
				return fmt.Errorf("-f and positional name argument are mutually exclusive")
			}
			if !hasFile && !hasName {
				return fmt.Errorf("provide -f <file> or <name> with flags")
			}

			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()

			if hasFile {
				return applyFromFile(ctx, cmd, client, file)
			}
			return applyInline(ctx, cmd, client, args[0], disabled)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to agent YAML file")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Set agent disabled state")
	return cmd
}

func applyFromFile(ctx context.Context, cmd *cobra.Command, client ariclient.Client, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading agent file %q: %w", file, err)
	}
	var ag pkgariapi.Agent
	if err := sigsyaml.Unmarshal(data, &ag); err != nil {
		return fmt.Errorf("parsing agent YAML %q: %w", file, err)
	}
	if ag.Metadata.Name == "" {
		return fmt.Errorf("agent YAML must have a non-empty 'metadata.name' field")
	}
	if ag.Spec.Command == "" {
		return fmt.Errorf("agent YAML must have a non-empty 'spec.command' field")
	}

	// Try create first; if it already exists, update instead.
	if err := client.Create(ctx, &ag); err != nil {
		if err := client.Update(ctx, &ag); err != nil {
			return err
		}
	}
	return cliutil.PrintJSON(cmd.OutOrStdout(), ag)
}

func applyInline(ctx context.Context, cmd *cobra.Command, client ariclient.Client, name string, disabled bool) error {
	var ag pkgariapi.Agent
	if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, &ag); err != nil {
		return fmt.Errorf("agent %q: %w", name, err)
	}

	ag.Spec.Disabled = &disabled

	if err := client.Update(ctx, &ag); err != nil {
		return err
	}
	return cliutil.PrintJSON(cmd.OutOrStdout(), ag)
}
