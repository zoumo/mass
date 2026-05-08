// Command massctl is a CLI for managing the mass daemon.
package main

import (
	"os"

	"github.com/zoumo/mass/cmd/massctl/commands"
	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

func main() {
	if err := commands.NewRootCommand().Execute(); err != nil {
		os.Exit(cliutil.ExitCode(err))
	}
}
