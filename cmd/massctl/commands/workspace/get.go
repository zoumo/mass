package workspace

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// newGetCmd returns the "get" subcommand.
// Without arguments it lists all workspaces; with positional names it gets
// those specific workspaces (kubectl-style).
func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var format cliutil.OutputFormat

	cmd := &cobra.Command{
		Use:   "get [name ...]",
		Short: "List or get workspaces",
		Long: `Without arguments, lists all workspaces.
With one or more names, gets those specific workspaces.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getClient()
			if err != nil {
				return err
			}
			defer client.Close()

			ctx := context.Background()
			printer := &cliutil.ResourcePrinter{Format: format, Columns: columns()}

			if len(args) == 0 {
				return listWorkspaces(ctx, client, printer, cmd)
			}
			return getWorkspaces(ctx, client, printer, cmd, args)
		},
	}

	cliutil.AddOutputFlag(cmd, &format)
	return cmd
}

func listWorkspaces(ctx context.Context, client pkgariapi.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command) error {
	var list pkgariapi.WorkspaceList
	if err := client.List(ctx, &list); err != nil {
		return err
	}
	items := make([]any, len(list.Items))
	for i := range list.Items {
		items[i] = list.Items[i]
	}
	return printer.Print(cmd.OutOrStdout(), items)
}

func getWorkspaces(ctx context.Context, client pkgariapi.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command, names []string) error {
	var items []any
	for _, name := range names {
		var ws pkgariapi.Workspace
		if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, &ws); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		items = append(items, ws)
	}
	return printer.Print(cmd.OutOrStdout(), items)
}
