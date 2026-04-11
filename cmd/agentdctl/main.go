// Command agentdctl is a CLI for managing the agentd daemon.
package main

import (
	"os"

	"github.com/open-agent-d/open-agent-d/cmd/agentdctl/subcommands"
)

func main() {
	if err := subcommands.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
