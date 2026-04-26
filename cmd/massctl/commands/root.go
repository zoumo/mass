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
	"github.com/zoumo/mass/cmd/massctl/commands/version"
	"github.com/zoumo/mass/cmd/massctl/commands/workspace"
	"github.com/zoumo/mass/pkg/agentd"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

func init() {
	cobra.AddTemplateFunc("shortWithAliases", func(cmd *cobra.Command) string {
		if len(cmd.Aliases) == 0 {
			return cmd.Short
		}
		return cmd.Short + " (alias: " + strings.Join(cmd.Aliases, ", ") + ")"
	})
}

// usageTemplate is a copy of cobra's default UsageTemplate with one change:
// the subcommand Short line uses shortWithAliases to append alias hints.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{shortWithAliases .}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimRightSpace}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimRightSpace}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

// NewRootCommand returns the massctl root cobra command.
func NewRootCommand() *cobra.Command {
	var socketPath string

	root := &cobra.Command{
		Use:          "massctl",
		Short:        "CLI for mass daemon management",
		SilenceUsage: true,
		Long: `massctl controls workspaces and agent-run lifecycles in the mass daemon.

Typical workflow:
  1. Create workspace    massctl ws create local --name my-ws --path /path/to/code --wait
  2. Create agent run    massctl ar create -w my-ws --name worker --agent claude --wait
  3. Send prompt         massctl ar prompt worker -w my-ws --text "Fix the bug" --wait
  4. Clean up            massctl ar stop worker -w my-ws
                         massctl ar delete worker -w my-ws
                         massctl ws delete my-ws

Quick start (single agent from current directory):
  massctl compose run -w my-ws --agent claude

Declarative multi-agent setup:
  massctl compose apply -f compose.yaml

Use "massctl [command] --help" for full examples and flag reference.`,
	}
	root.SetUsageTemplate(usageTemplate)
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
	return root
}
