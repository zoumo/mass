package agent

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func newDeleteCmd(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "delete name [name ...]",
		Short: "Delete one or more agent definitions",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			for _, name := range args {
				if err := client.Delete(ctx, pkgariapi.ObjectKey{Name: name}, &pkgariapi.Agent{}); err != nil {
					return fmt.Errorf("deleting agent %q: %w", name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "agent %q deleted\n", name)
			}
			return nil
		},
	}
}
