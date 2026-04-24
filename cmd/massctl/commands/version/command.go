package version

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/internal/version"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// NewCommand returns the version subcommand.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Print client version
			fmt.Println("massctl version:", version.Full())
			if version.BuildTime != "" {
				fmt.Println("build time:", version.BuildTime)
			}

			// Try to get daemon version
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			client, err := getClient()
			if err != nil {
				fmt.Println("\ndaemon: not reachable")
				return nil
			}
			defer client.Close()

			info, err := client.System().Info(ctx)
			if err != nil {
				fmt.Println("\ndaemon: not reachable")
				return nil
			}

			fmt.Println("\nmass daemon version:", formatVersion(info))
			fmt.Println("go version:", info.GoVersion)
			if info.BuildTime != "" {
				fmt.Println("build time:", info.BuildTime)
			}
			return nil
		},
	}
}

func formatVersion(info *pkgariapi.SystemInfoResult) string {
	if info.GitCommit != "" && info.GitCommit != "unknown" {
		return info.Version + " (" + info.GitCommit + ")"
	}
	return info.Version
}
