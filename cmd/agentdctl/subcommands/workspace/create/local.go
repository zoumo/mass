package create

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/oar/api"
	"github.com/zoumo/oar/api/ari"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
	"github.com/zoumo/oar/pkg/workspace"
)

func newLocalCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		name string
		path string
	)
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Create a workspace from a local directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" {
				return fmt.Errorf("--path is required")
			}
			src := workspace.Source{
				Type:  workspace.SourceTypeLocal,
				Local: workspace.LocalSource{Path: path},
			}
			srcJSON, err := json.Marshal(src)
			if err != nil {
				return fmt.Errorf("marshal source: %w", err)
			}
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := ari.WorkspaceCreateParams{Name: name, Source: srcJSON}
			var result ari.WorkspaceCreateResult
			if err := client.Call(api.MethodWorkspaceCreate, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	cmd.Flags().StringVar(&path, "path", "", "Local directory path (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}
