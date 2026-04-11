package create

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands/cliutil"
	"github.com/open-agent-d/open-agent-d/pkg/ari"
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

func newEmptyCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "empty <name>",
		Short: "Create an empty directory workspace",
		Args:  cobra.ExactArgs(1),
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
}
