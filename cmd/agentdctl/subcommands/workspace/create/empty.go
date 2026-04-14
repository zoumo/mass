package create

import (
	"encoding/json"

	"github.com/spf13/cobra"

	pkgariapi "github.com/zoumo/oar/pkg/ari/api"
	"github.com/zoumo/oar/cmd/agentdctl/subcommands/cliutil"
	"github.com/zoumo/oar/pkg/workspace"
)

func newEmptyCmd(getClient cliutil.ClientFn) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "empty",
		Short: "Create an empty directory workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			src := workspace.Source{Type: workspace.SourceTypeEmptyDir}
			srcJSON, err := json.Marshal(src)
			if err != nil {
				return nil
			}
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			params := pkgariapi.WorkspaceCreateParams{Name: name, Source: srcJSON}
			var result pkgariapi.WorkspaceCreateResult
			if err := client.Call(pkgariapi.MethodWorkspaceCreate, params, &result); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
