package main

import (
	"os"

	"github.com/zoumo/mass/cmd/superagent/subcommand"
)

func main() { os.Exit(subcommand.Execute()) }
