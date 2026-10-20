// Package subcommand owns the terminal interface to the MASS HTTP service.
package subcommand

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Version is set by the binary build when release metadata is available.
var Version = "dev"

type failure struct {
	value *runtime.OperationError
	exit  int
}

func (e *failure) Error() string { return e.value.Error() }
func invalid(message string) error {
	return &failure{&runtime.OperationError{Code: "invalid_argument", Reason: "invalid_arguments", Message: message}, 2}
}

func fileError() error {
	return &failure{&runtime.OperationError{Code: "invalid_argument", Reason: "input_file_error", Message: "Cannot read input file as UTF-8 text"}, 1}
}

type options struct {
	url, output string
	timeout     time.Duration
	client      client.Client
}

// Run executes one command. It writes results to stdout and diagnostics to stderr.
// A canceled context releases only this invocation, never Agent work.
func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) int {
	o := &options{}
	root := o.command()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	exit := 2
	v := &runtime.OperationError{Code: "invalid_argument", Reason: "invalid_arguments", Message: "Invalid command arguments; use --help"}
	var f *failure
	if errors.As(err, &f) {
		v = f.value
		exit = f.exit
	}
	if ctx.Err() != nil {
		if v.Code != "outcome_unknown" {
			v = &runtime.OperationError{Code: "canceled", Reason: "interrupted", Message: "Request wait interrupted"}
		}
		exit = 130
	}
	if o.output == "json" {
		if writeErr := json.NewEncoder(stderr).Encode(v); writeErr != nil {
			return exit
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %s\n", v.Code, v.Reason, v.Message)
	}
	return exit
}

func (o *options) command() *cobra.Command {
	root := &cobra.Command{Use: "massctl", Short: "Manage MASS resources over HTTP", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true}}
	root.Example = "  massctl runtime list\n  massctl session get review -n demo\n  massctl -o json session history SESSION_ID"
	root.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	root.PersistentFlags().StringVar(&o.url, "url", envURL(), "MASS service URL (MASS_URL)")
	root.PersistentFlags().StringVarP(&o.output, "output", "o", "human", "Output: human or json")
	root.PersistentFlags().DurationVar(&o.timeout, "timeout", 30*time.Second, "Request timeout; 0 disables (watch: through ready only)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if o.output != "human" && o.output != "json" {
			return invalid("Output must be human or json")
		}
		if o.timeout < 0 {
			return invalid("Timeout must be nonnegative")
		}
		o.client = client.Client{URL: o.url, Token: os.Getenv("MASS_TOKEN")}
		return nil
	}
	root.AddCommand(o.runtimeCommands(), o.sessionCommands(), &cobra.Command{Use: "version", Example: "  massctl version", Short: "Print client version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.output == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"version": Version})
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "massctl", Version)
		return err
	}})
	// Flag values are per invocation; aliases share the same counter.
	var strict func(*cobra.Command)
	strict = func(cmd *cobra.Command) {
		if run := cmd.RunE; run != nil {
			cmd.RunE = func(c *cobra.Command, args []string) error {
				err := run(c, args)
				if err == nil {
					return nil
				}
				var f *failure
				if errors.As(err, &f) {
					return err
				}
				return &failure{&runtime.OperationError{Code: "internal", Reason: "output_failed", Message: "Cannot write command output"}, 1}
			}
		}

		for _, fs := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				if f.Name != "additional-dir" {
					f.Value = &singleValue{Value: f.Value}
				}
			})
		}
		for _, child := range cmd.Commands() {
			strict(child)
		}
	}
	strict(root)
	return root
}

func envURL() string {
	if v := os.Getenv("MASS_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:24368"
}

type singleValue struct {
	pflag.Value
	set bool
}

func (v *singleValue) Set(s string) error {
	if v.set {
		return errors.New("single-value flag repeated")
	}
	v.set = true
	return v.Value.Set(s)
}

func (o *options) request(cmd *cobra.Command, op string, call func(context.Context, any) error, out any) error {
	ctx := cmd.Context()
	cancel := func() {}
	if o.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
	}
	defer cancel()
	if err := call(ctx, out); err != nil {
		return requestError(err, op)
	}
	return nil
}

func requestError(err error, op string) error {
	var v *runtime.OperationError
	if errors.As(err, &v) {
		return &failure{v, 1}
	}
	code, reason, message := "unavailable", "request_failed", "Request failed"
	switch op {
	case "runtime.get", "runtime.list", "session.get", "session.list", "session.history", "watch":
	default:
		code, reason, message = "outcome_unknown", "response_unconfirmed", "No verifiable response; operation may have executed"
	}
	return &failure{&runtime.OperationError{Code: code, Reason: reason, Message: message}, 1}
}

func group(name, short string) *cobra.Command {
	c := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs}
	c.Example = "  massctl " + name + " list\n  massctl " + name + " create --help"
	c.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	return c
}

func (o *options) result(cmd *cobra.Command, op, name string, call func(context.Context, any) error) error {
	var raw json.RawMessage
	if err := o.request(cmd, op, call, &raw); err != nil {
		return err
	}
	if op == "runtime.delete" {
		if o.output == "human" {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted Runtime %s\n", name)
			return err
		}
		return nil
	}
	return o.print(cmd.OutOrStdout(), raw)
}
