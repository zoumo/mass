package version

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	"github.com/zoumo/mass/internal/version"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// NewCommand returns the version subcommand.
func NewCommand(getClient cliutil.ClientFn) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOutput {
				return printJSON(getClient)
			}
			return printText(getClient)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func printText(getClient cliutil.ClientFn) error {
	// Print client version
	fmt.Println("client:")
	fmt.Println("  version:    ", version.Version)
	fmt.Println("  git commit: ", version.GitCommit)
	fmt.Println("  go version: ", version.GoVersion)
	if version.BuildTime != "" {
		fmt.Println("  build time: ", version.BuildTime)
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

	fmt.Println("\ndaemon:")
	fmt.Println("  version:    ", info.Version)
	fmt.Println("  git commit: ", info.GitCommit)
	fmt.Println("  go version: ", info.GoVersion)
	if info.BuildTime != "" {
		fmt.Println("  build time: ", info.BuildTime)
	}
	return nil
}

func printJSON(getClient cliutil.ClientFn) error {
	result := struct {
		Client pkgariapi.SystemInfoResult `json:"client"`
		Daemon pkgariapi.SystemInfoResult `json:"daemon,omitempty"`
	}{
		Client: pkgariapi.SystemInfoResult{
			Version:   version.Version,
			GitCommit: version.GitCommit,
			GoVersion: version.GoVersion,
			BuildTime: version.BuildTime,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := getClient()
	if err == nil {
		defer client.Close()
		info, err := client.System().Info(ctx)
		if err == nil {
			result.Daemon = *info
		}
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
