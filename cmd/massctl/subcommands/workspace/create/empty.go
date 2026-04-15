package create

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/cmd/massctl/subcommands/cliutil"
	"github.com/zoumo/mass/pkg/workspace"
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

			ws := pkgariapi.Workspace{
				Metadata: pkgariapi.ObjectMeta{Name: name},
				Spec:     pkgariapi.WorkspaceSpec{Source: srcJSON},
			}
			if err := client.Create(context.Background(), &ws); err != nil {
				cliutil.HandleError(err)
				return nil
			}
			cliutil.OutputJSON(ws)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Workspace name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
