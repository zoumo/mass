package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/extensions/cua"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var configPath string
	cmd := &cobra.Command{
		Use:          "cua-mcp",
		Short:        "Serve configured Cua computers over stdio MCP",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cua.Run(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to the Cua MCP YAML configuration")
	if err := cmd.MarkFlagRequired("config"); err != nil {
		panic(err)
	}
	if err := cmd.ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}
