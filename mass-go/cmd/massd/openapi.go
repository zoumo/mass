package main

import (
	"github.com/spf13/cobra"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func newOpenAPICommand() *cobra.Command {
	return &cobra.Command{Use: "openapi", Short: "Export OpenAPI JSON without starting the daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		document, err := transport.OpenAPI()
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(document, '\n'))
		return err
	}}
}
