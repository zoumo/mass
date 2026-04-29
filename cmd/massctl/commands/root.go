// Package commands assembles the massctl cobra command tree.
package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/agent"
	"github.com/zoumo/mass/cmd/massctl/commands/agentrun"
	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/cmd/massctl/commands/compose"
	"github.com/zoumo/mass/cmd/massctl/commands/ext"
	"github.com/zoumo/mass/cmd/massctl/commands/version"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace"
	"github.com/zoumo/mass/pkg/agentd"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

func init() {
	cobra.AddTemplateFunc("nameWithAliases", func(cmd *cobra.Command) string {
		if len(cmd.Aliases) == 0 {
			return cmd.Name()
		}
		return cmd.Name() + " (" + strings.Join(cmd.Aliases, ", ") + ")"
	})
	cobra.AddTemplateFunc("cmdAliasNamePad", func(parent *cobra.Command) int {
		maxLen := 11
		for _, c := range parent.Commands() {
			if !c.IsAvailableCommand() && c.Name() != "help" {
				continue
			}
			n := c.Name()
			if len(c.Aliases) > 0 {
				n = n + " (" + strings.Join(c.Aliases, ", ") + ")"
			}
			if len(n) > maxLen {
				maxLen = len(n)
			}
		}
		return maxLen + 1
	})
}

// usageTemplate renders help output in four sections:
//  1. description (Long or Short)
//  2. Examples
//  3. Available Commands  (aliases shown inline)
//  4. Options / Global Options
const usageTemplate = `{{if .Long}}{{.Long | trimRightSpace}}{{else}}{{.Short | trimRightSpace}}{{end}}
{{if not .HasAvailableSubCommands}}
Usage:
  {{.UseLine}}{{end}}{{if .HasExample}}
Examples:
{{.Example | trimRightSpace}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{$pad := cmdAliasNamePad .}}{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad (nameWithAliases .) $pad}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Options:
{{.LocalFlags.FlagUsages | trimRightSpace}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Options:
{{.InheritedFlags.FlagUsages | trimRightSpace}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// NewRootCommand returns the massctl root cobra command.
func NewRootCommand() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:          "massctl",
		Short:        "CLI for mass daemon management",
		SilenceUsage: true,
		Long:         `massctl controls workspaces and agent-run lifecycles in the mass daemon.`,
		Example: `  # Typical end-to-end workflow
  massctl ws create local --name my-ws --path /path/to/code --wait
  massctl ar create -w my-ws --name worker --agent claude --wait
  massctl ar prompt worker -w my-ws --text "Fix the bug" --wait
  massctl ar stop worker -w my-ws
  massctl ar delete worker -w my-ws
  massctl ws delete my-ws

  # Quick start: single agent from current directory
  massctl compose run -w my-ws --agent claude

  # Declarative multi-agent setup
  massctl compose apply -f compose.yaml`,
	}
	root.SetUsageTemplate(usageTemplate)
	root.SetHelpTemplate("{{.UsageString}}\n")
	root.PersistentFlags().StringVar(&socketPath, "socket", filepath.Join(agentd.DefaultRoot(), "mass.sock"), "ARI socket path")

	getClient := func() (ariclient.Client, error) {
		if socketPath == "" {
			return nil, os.ErrInvalid
		}
		return ariclient.Dial(context.Background(), socketPath)
	}

	root.AddCommand(agentrun.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(agent.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(workspace.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(compose.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(version.NewCommand(cliutil.ClientFn(getClient)))
	root.AddCommand(ext.NewCommand())
	return root
}
