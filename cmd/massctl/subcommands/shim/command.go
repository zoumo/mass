// Package shim provides commands for direct communication with a running
// agent-shim over its Unix socket JSON-RPC interface.
package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/pkg/jsonrpc"
	shimclient "github.com/zoumo/mass/pkg/shim/client"
)

// dialShim connects to a shim Unix socket and returns a typed ShimClient.
func dialShim(ctx context.Context, socketPath string, opts ...jsonrpc.ClientOption) (*shimclient.ShimClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}
	return shimclient.NewShimClient(c), nil
}

// NewCommand returns the "shim" cobra command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shim",
		Short: "Direct communication with a running agent-shim over its Unix socket",
	}

	var socket string
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Unix socket path for the shim (required)")
	_ = cmd.MarkPersistentFlagRequired("socket")

	cmd.AddCommand(&cobra.Command{
		Use:   "state",
		Short: "Print agent state and recovery metadata (runtime/status)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc, err := dialShim(cmd.Context(), socket)
			if err != nil {
				return err
			}
			defer sc.Close()
			result, err := sc.Status(cmd.Context())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Gracefully shut down the agent (runtime/stop)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc, err := dialShim(cmd.Context(), socket)
			if err != nil {
				return err
			}
			defer sc.Close()
			err = sc.Stop(cmd.Context())
			if err == nil {
				fmt.Println("stop sent")
			}
			return err
		},
	})

	return cmd
}
