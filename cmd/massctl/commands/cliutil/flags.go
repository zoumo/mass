package cliutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// AddOutputFlag registers the -o / --output flag on cmd and returns a pointer
// to the bound OutputFormat value. Default is table.
func AddOutputFlag(cmd *cobra.Command, format *OutputFormat) {
	cmd.Flags().StringVarP((*string)(format), "output", "o", string(FormatTable),
		"Output format: table, wide, json, yaml")
}

// ResolveFilePath validates that path exists and is a regular file, then
// returns its absolute path.
func ResolveFilePath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", path)
	}
	return filepath.Abs(path)
}
