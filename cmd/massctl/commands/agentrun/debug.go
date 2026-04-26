package agentrun

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	runclient "github.com/zoumo/mass/pkg/agentrun/client"
)

// newDebugCmd returns the "debug" subcommand for direct agent-run socket communication.
func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Direct communication with a running agent-run over its Unix socket",
	}

	var socket string
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Unix socket path for the agent-run (required)")
	_ = cmd.MarkPersistentFlagRequired("socket")

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Print agent runtime status and recovery metadata (runtime/status)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc, err := runclient.Dial(cmd.Context(), socket)
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
			sc, err := runclient.Dial(cmd.Context(), socket)
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
