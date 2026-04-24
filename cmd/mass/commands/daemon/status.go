package daemon

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/pkg/agentd"
	"github.com/zoumo/mass/pkg/ari/client"
)

func newStatusCmd(rootPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Check daemon status",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(*rootPath)
		},
	}
}

func runStatus(rootPath string) error {
	opts := agentd.Options{Root: rootPath}

	pid, err := readPidFile(opts.PidFilePath())
	if err != nil {
		fmt.Println("daemon: not running")
		return nil
	}

	// Verify the process is actually alive.
	if err := syscall.Kill(pid, 0); err != nil {
		fmt.Println("daemon: not running (stale pid file)")
		return nil
	}

	// Try connecting to the socket.
	conn, err := net.DialTimeout("unix", opts.SocketPath(), 2*time.Second)
	if err != nil {
		fmt.Printf("daemon: process running (pid: %d) but socket not ready\n", pid)
		return nil
	}
	conn.Close()

	// Call system/info RPC for version info.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := client.Dial(ctx, opts.SocketPath())
	if err != nil {
		fmt.Printf("daemon: running (pid: %d)\n", pid)
		return nil
	}
	defer c.Close()

	info, err := c.System().Info(ctx)
	if err != nil {
		fmt.Printf("daemon: running (pid: %d)\n", pid)
		return nil
	}

	fmt.Printf("daemon: running (pid: %d, version: %s, %s)\n", pid, info.Version, info.GoVersion)
	return nil
}
