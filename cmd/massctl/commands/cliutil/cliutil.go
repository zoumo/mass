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

// PrintJSON writes result as pretty JSON to w.
func PrintJSON(w io.Writer, result any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// ToAnySlice converts a typed slice to []any for ResourcePrinter.
func ToAnySlice[T any](items []T) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i]
	}
	return out
}

// HandleError prints the error to stderr and exits with code 1.
func HandleError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
