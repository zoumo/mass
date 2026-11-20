package main

import (
	"os"

	"github.com/zoumo/mass/superagent/cmd/superagent/subcommand"
)

func main() { os.Exit(subcommand.Execute()) }
