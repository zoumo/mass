package agentrun

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// newGetCmd returns the "get" subcommand.
// Without arguments it lists all agent runs; with positional names it gets
// those specific agent runs (requires -w workspace).
func newGetCmd(getClient cliutil.ClientFn) *cobra.Command {
	var (
		ws     string
		state  string
		format cliutil.OutputFormat
	)

	cmd := &cobra.Command{
		Use:   "get [name ...] [-w workspace]",
		Short: "List or get agent runs",
		Long: `Without arguments, lists all agent runs (optionally filtered by -w workspace).
With one or more names, gets those specific agent runs (-w required).`,
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
				return listAgentRuns(ctx, client, printer, cmd, ws, state)
			}
			if ws == "" {
				return fmt.Errorf("--workspace/-w is required when getting agent runs by name")
			}
			return getAgentRuns(ctx, client, printer, cmd, ws, args)
		},
	}

	cmd.Flags().StringVarP(&ws, "workspace", "w", "", "Workspace name (required for get, optional filter for list)")
	cmd.Flags().StringVar(&state, "phase", "", "Filter by lifecycle phase (list only)")
	cliutil.AddOutputFlag(cmd, &format)
	return cmd
}

func listAgentRuns(ctx context.Context, client ariclient.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command, ws, state string) error {
	var opts []pkgariapi.ListOption
	if ws != "" {
		opts = append(opts, pkgariapi.InWorkspace(ws))
	}
	if state != "" {
		opts = append(opts, pkgariapi.WithPhase(state))
	}
	var list pkgariapi.AgentRunList
	if err := client.List(ctx, &list, opts...); err != nil {
		return err
	}
	list.Kind = pkgariapi.KindList
	items := make([]any, len(list.Items))
	for i := range list.Items {
		items[i] = list.Items[i]
	}
	return printer.PrintList(cmd.OutOrStdout(), items, list)
}

func getAgentRuns(ctx context.Context, client ariclient.Client, printer *cliutil.ResourcePrinter, cmd *cobra.Command, ws string, names []string) error {
	var list pkgariapi.AgentRunList
	for _, name := range names {
		var ar pkgariapi.AgentRun
		if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: ws, Name: name}, &ar); err != nil {
			return fmt.Errorf("agentrun %s/%s: %w", ws, name, err)
		}
		list.Items = append(list.Items, ar)
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
