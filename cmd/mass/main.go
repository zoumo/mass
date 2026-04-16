// Package main implements the mass daemon entry point.
// mass is the Multi-Agent Supervision System daemon that manages agent runtime via ARI.
package main

import (
	"os"

	"github.com/zoumo/mass/cmd/mass/commands"
)

func main() {
	if err := commands.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
