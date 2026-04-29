package create

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/workspace"
)

// workspaceInput matches the API Workspace shape (metadata.name + spec.source)
// so that `massctl ws get -o json` output can be fed back into `workspace create -f`.
type workspaceInput struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Source workspace.Source `json:"source"`
	} `json:"spec"`
}

func addFileFlags(cmd *cobra.Command, getClient cliutil.ClientFn) {
	var (
		file string
		wait bool
	)
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to workspace YAML spec file")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for workspace to become ready (used with -f)")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if file == "" {
			return cmd.Help()
		}

		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("reading workspace spec %q: %w", file, err)
		}
		var input workspaceInput
		if err := sigsyaml.Unmarshal(data, &input); err != nil {
			return fmt.Errorf("parsing workspace spec %q: %w", file, err)
		}
		if input.Metadata.Name == "" {
			return fmt.Errorf("workspace spec must have a non-empty 'metadata.name' field")
		}
		if input.Spec.Source.Type == "" {
			return fmt.Errorf("workspace spec must have a non-empty 'spec.source.type' field")
		}

		client, err := getClient()
		if err != nil {
			return err
		}
		defer client.Close()

		ctx := context.Background()
		ws, err := cliutil.CreateWorkspace(ctx, client, input.Metadata.Name, input.Spec.Source)
		if err != nil {
			return err
		}
		if wait {
			if err := cliutil.WaitWorkspaceReady(ctx, client, input.Metadata.Name); err != nil {
				return err
			}
			if err := client.Get(ctx, pkgariapi.ObjectKey{Name: input.Metadata.Name}, ws); err != nil {
				return err
			}
		}
		return cliutil.PrintJSON(cmd.OutOrStdout(), ws)
	}
}
