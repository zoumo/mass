package agent

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// newGetCmd returns the "get" subcommand.
// Without arguments it lists all agents; with positional names it gets those
// specific agents (kubectl-style).
func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var format cliutil.OutputFormat

	cmd := &cobra.Command{
		Use:   "get [name ...]",
		Short: "List or get agent definitions",
		Long: `Without arguments, lists all agent definitions.
With one or more names, gets those specific agents.`,
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
				return listAgents(ctx, client, printer, cmd)
			}
			return getAgents(ctx, client, printer, cmd, args)
		},
	}

	cliutil.AddOutputFlag(cmd, &format)
	return cmd
}

func listAgents(ctx context.Context, client pkgariapi.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command) error {
	var list pkgariapi.AgentList
	if err := client.List(ctx, &list); err != nil {
		return err
	}
	list.Kind = pkgariapi.KindList
	items := make([]any, len(list.Items))
	for i := range list.Items {
		items[i] = list.Items[i]
	}
	return printer.PrintList(cmd.OutOrStdout(), items, list)
}

func getAgents(ctx context.Context, client pkgariapi.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command, names []string) error {
	var list pkgariapi.AgentList
	for _, name := range names {
		var ag pkgariapi.Agent
		if err := client.Get(ctx, pkgariapi.ObjectKey{Name: name}, &ag); err != nil {
			return fmt.Errorf("agent %q: %w", name, err)
		}
		list.Items = append(list.Items, ag)
	}
	items := make([]any, len(list.Items))
	for i := range list.Items {
		items[i] = list.Items[i]
	}
	if len(items) == 1 {
		return printer.Print(cmd.OutOrStdout(), items)
	}
	list.Kind = pkgariapi.KindList
	return printer.PrintList(cmd.OutOrStdout(), items, list)
}
