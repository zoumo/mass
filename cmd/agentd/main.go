// Package main implements the agentd daemon entry point.
// agentd is the agent daemon that manages agent runtime via ARI.
package main

import (
	"os"

	"github.com/zoumo/oar/cmd/agentd/subcommands"
)

func main() {
	if err := subcommands.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
