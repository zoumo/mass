// Package cliutil provides shared helpers for massctl commands.
package cliutil

import (
	"encoding/json"
	"fmt"
	"os"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// ClientFn is a factory for ARI clients, injected by the root command.
type ClientFn func() (pkgariapi.Client, error)

// OutputJSON pretty-prints the result as JSON to stdout.
func OutputJSON(result any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// HandleError prints the error to stderr and exits with code 1.
func HandleError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
