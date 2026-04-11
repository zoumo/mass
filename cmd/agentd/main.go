// Package main implements the agentd daemon entry point.
// agentd is the agent daemon that manages agent runtime via ARI.
package main

import (
	"os"

	"github.com/open-agent-d/open-agent-d/cmd/agentd/subcommands"
)

func main() {
	if err := subcommands.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
