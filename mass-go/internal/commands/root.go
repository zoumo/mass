// Package commands assembles massctl following agentrun's agentdctl command UX.
package commands

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/zoumo/mass/mass-go/internal/client"
)

type options struct {
	endpoint string
	timeout  time.Duration
	output   string
	json     bool
}

func NewRootCommand() *cobra.Command {
	o := &options{}
	root := &cobra.Command{Use: "massctl", Short: "Interact with the MASS server", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, RunE: showHelp,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if o.timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if o.output != "table" && o.output != "json" {
				return errors.New("--output must be table or json")
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&o.endpoint, "endpoint", "http://127.0.0.1:24368", "MASS base URL or JSON-RPC URL")
	root.PersistentFlags().DurationVar(&o.timeout, "timeout", 60*time.Second, "unary request and watch connection timeout")
	root.PersistentFlags().StringVarP(&o.output, "output", "o", "table", "output format: table or json (events are JSONL)")
	root.PersistentFlags().BoolVar(&o.json, "json", false, "emit compact JSON; shorthand for --output json")
	root.AddCommand(o.unary("status", "Get server status", "agentd/status", 0, nil))
	root.AddCommand(o.runtime(), o.session())
	return root
}

func showHelp(cmd *cobra.Command, _ []string) error { return cmd.Help() }

type paramsFunc func(*cobra.Command, []string) (any, error)

func (o *options) unary(use, short, method string, argc int, params paramsFunc) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(argc), RunE: func(cmd *cobra.Command, args []string) error {
		var p any = map[string]any{}
		if params != nil {
			var err error
			p, err = params(cmd, args)
			if err != nil {
				return err
			}
		}
		c, err := client.New(o.endpoint, o.timeout)
		if err != nil {
			return err
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(cmd.Context(), o.timeout)
		defer cancel()
		result, err := c.Call(ctx, method, p)
		if err != nil {
			return err
		}
		page, _ := cmd.Flags().GetBool("page")
		return o.print(cmd.OutOrStdout(), method, result, page)
	}}
}

func selector(_ *cobra.Command, args []string) (any, error) {
	if args[0] == "" {
		return nil, errors.New("session name must not be empty")
	}
	return map[string]string{"sessionName": args[0]}, nil
}

func (o *options) runtime() *cobra.Command {
	cmd := &cobra.Command{Use: "runtime", Aliases: []string{"rt"}, Short: "Inspect configured runtimes (registration is server-file managed)", Args: cobra.NoArgs, RunE: showHelp}
	cmd.AddCommand(o.unary("types", "List runtime types", "agentd/runtime/types", 0, nil), o.unary("list", "List runtimes", "agentd/runtime/list", 0, nil))
	status := o.unary("status RUNTIME_NAME", "Get runtime status", "agentd/runtime/status", 1, func(_ *cobra.Command, args []string) (any, error) { return map[string]string{"name": args[0]}, nil })
	status.Aliases = []string{"get"}
	cmd.AddCommand(status)
	return cmd
}
