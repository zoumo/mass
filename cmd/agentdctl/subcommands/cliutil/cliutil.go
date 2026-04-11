// Package cliutil provides shared helpers for agentdctl subcommands.
package cliutil

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// ClientFn is a factory for ARI clients, injected by the root command.
type ClientFn func() (*ari.Client, error)

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

// ParseAgentKey splits a "workspace/name" argument into (workspace, name).
func ParseAgentKey(arg string) (workspace, name string, err error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("agent key must be in 'workspace/name' format, got %q", arg)
	}
	return parts[0], parts[1], nil
}
