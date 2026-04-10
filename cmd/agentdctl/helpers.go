// Package main provides shared helper utilities for the agentdctl CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-agent-d/open-agent-d/pkg/ari"
)

// getClient creates an ARI client connected to the socket path.
// The socketPath is set by the root command's persistent flag.
func getClient() (*ari.Client, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("socket path not set")
	}
	return ari.NewClient(socketPath)
}

// outputJSON pretty-prints the result as JSON to stdout.
func outputJSON(result any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// handleError prints the error to stderr and exits with code 1.
func handleError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
