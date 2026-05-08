// Package cliutil provides shared helpers for massctl commands.
package cliutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// ClientFn is a factory for ARI clients, injected by the root command.
type ClientFn func() (ariclient.Client, error)

// ExitError carries a command-specific process exit code without forcing the
// command to call os.Exit before deferred cleanup can run.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

// ExitCode returns err's requested process exit code, or 1 for ordinary errors.
func ExitCode(err error) int {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

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
