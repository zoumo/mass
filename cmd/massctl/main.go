// Command massctl is a CLI for managing the mass daemon.
package main

import (
	"os"

	"github.com/zoumo/mass/cmd/massctl/subcommands"
)

func main() {
	if err := subcommands.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
