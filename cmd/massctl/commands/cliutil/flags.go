package cliutil

import (
	"github.com/spf13/cobra"
)

// AddOutputFlag registers the -o / --output flag on cmd and returns a pointer
// to the bound OutputFormat value. Default is table.
func AddOutputFlag(cmd *cobra.Command, format *OutputFormat) {
	cmd.Flags().StringVarP((*string)(format), "output", "o", string(FormatTable),
		"Output format: table, wide, json, yaml")
}
