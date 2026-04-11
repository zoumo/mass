package create

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

func newLocalCmd(getClient cliutil.ClientFn) *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "local <name> --path <path>",
		Short: "Create a workspace from a local directory",
		Args:  cobra.ExactArgs(1),
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

			params := ari.WorkspaceCreateParams{Name: args[0], Source: srcJSON}
			var result ari.WorkspaceCreateResult
			if err := client.Call("workspace/create", params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Local directory path (required)")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}
