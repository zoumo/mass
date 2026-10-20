package subcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type configInput struct{ executable, file, inline string }

func (c *configInput) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.executable, "executable", "", "Agent executable path")
	cmd.Flags().StringVar(&c.file, "config-file", "", "Complete RuntimeConfig JSON file (- for stdin)")
	cmd.Flags().StringVar(&c.inline, "config-json", "", "Complete RuntimeConfig JSON")
	cmd.MarkFlagsMutuallyExclusive("executable", "config-file", "config-json")
}

func (c *configInput) provided(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("executable") || cmd.Flags().Changed("config-file") || cmd.Flags().Changed("config-json")
}

func (c *configInput) read(cmd *cobra.Command, base runtime.Config) (runtime.Config, error) {
	if cmd.Flags().Changed("executable") {
		if base == nil {
			base = runtime.Config{}
		}
		base["executable"] = c.executable
		return base, nil
	}
	if !cmd.Flags().Changed("config-file") && !cmd.Flags().Changed("config-json") {
		return runtime.Config{}, nil
	}
	b := []byte(c.inline)
	if cmd.Flags().Changed("config-file") {
		var err error
		b, err = readFile(cmd, c.file)
		if err != nil {
			return nil, err
		}
	}
	if !utf8.Valid(b) || transport.ValidateJSON(b) != nil {
		return nil, invalid("Configuration must be one valid JSON value")
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, invalid("Invalid configuration JSON")
	}
	if value == nil {
		return nil, nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("Configuration must be an object or null")
	}
	return runtime.Config(m), nil
}

func readFile(cmd *cobra.Command, path string) ([]byte, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(cmd.InOrStdin())
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil || !utf8.Valid(b) {
		return nil, fileError()
	}
	return b, nil
}

func (o *options) runtimeCommands() *cobra.Command {
	root := group("runtime", "Manage Runtime resources")
	for _, action := range []string{"create", "get", "list", "update", "start", "stop", "delete"} {
		cmd := &cobra.Command{Use: action + " NAME", Short: action + " Runtime", Args: cobra.ExactArgs(1)}
		cmd.Example = "  massctl runtime " + action + " agent"
		var input configInput
		var kind string
		if action == "list" {
			cmd.Example = "  massctl runtime list"
			cmd.Use = "list"
			cmd.Args = cobra.NoArgs
		}
		if action == "create" || action == "update" {
			input.flags(cmd)
			cmd.Long = "Configuration inputs --executable, --config-file and --config-json are mutually exclusive. Files contain RuntimeConfig; '-' reads stdin."
			if action == "create" {
				cmd.Long += " NAME and --type are required. Omitted configuration submits {}. Creation does not start the Runtime."
				cmd.Example = "  massctl runtime create agent --type codex --executable /absolute/path/to/codex"
			} else {
				cmd.Long += " At least one configuration input is required. Preserve the current type. --executable preserves other fields; complete JSON replaces config. Updating does not restart the Runtime."
				cmd.Example = "  massctl runtime update agent --executable /absolute/path/to/codex\n  massctl runtime update agent --config-file runtime-config.json"
			}
		}
		if action == "create" {
			cmd.Flags().StringVar(&kind, "type", "", "Runtime type (required)")
			_ = cmd.MarkFlagRequired("type")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			name := ""
			var call func(context.Context, any) error
			if len(args) > 0 {
				name = args[0]
				if name == "" {
					return invalid("Runtime name is required")
				}
			}
			switch action {
			case "create":
				if kind == "" {
					return invalid("Runtime type is required")
				}
				config, err := input.read(cmd, nil)
				if err != nil {
					return err
				}
				v := resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: resource.RuntimeSpec{Type: kind, Config: config}}
				call = func(ctx context.Context, out any) error { return o.client.CreateRuntime(ctx, v, out) }
			case "update":
				if !input.provided(cmd) {
					return invalid("Runtime update requires configuration input")
				}
				// Parse complete config before any network request; flags apply after GET.
				config, err := input.read(cmd, nil)
				if err != nil {
					return err
				}
				var current resource.Runtime
				if err := o.request(cmd, "runtime.get", func(ctx context.Context, out any) error { return o.client.GetRuntime(ctx, name, out) }, &current); err != nil {
					return err
				}
				if cmd.Flags().Changed("executable") {
					config, err = input.read(cmd, current.Spec.Config)
					if err != nil {
						return err
					}
				}
				spec := resource.RuntimeSpec{Type: current.Spec.Type, Config: config}
				call = func(ctx context.Context, out any) error { return o.client.UpdateRuntime(ctx, name, spec, out) }
			case "get":
				call = func(ctx context.Context, out any) error { return o.client.GetRuntime(ctx, name, out) }
			case "list":
				call = func(ctx context.Context, out any) error {
					return o.client.ListRuntimes(ctx, resource.RuntimeQuery{}, out)
				}
			case "start":
				call = func(ctx context.Context, out any) error { return o.client.StartRuntime(ctx, name, out) }
			case "stop":
				call = func(ctx context.Context, out any) error { return o.client.StopRuntime(ctx, name, out) }
			case "delete":
				call = func(ctx context.Context, _ any) error { return o.client.DeleteRuntime(ctx, name) }
			}
			return o.result(cmd, "runtime."+action, name, call)
		}
		root.AddCommand(cmd)
	}
	return root
}
