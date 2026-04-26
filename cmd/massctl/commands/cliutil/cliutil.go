// Package cliutil provides shared helpers for massctl commands.
package cliutil

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// ClientFn is a factory for ARI clients, injected by the root command.
type ClientFn func() (ariclient.Client, error)

// OutputJSON pretty-prints the result as JSON to stdout.
//
// Deprecated: prefer PrintJSON or ResourcePrinter for new commands.
func OutputJSON(result any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// PrintJSON writes result as pretty JSON to w.
func PrintJSON(w io.Writer, result any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// HandleError prints the error to stderr and exits with code 1.
func HandleError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
