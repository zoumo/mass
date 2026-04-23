package daemon

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/mass/pkg/agentd"
)

func newRestartCmd(rootPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "restart",
		Short:        "Restart the running daemon",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestart(*rootPath)
		},
	}
}

func runRestart(rootPath string) error {
	opts := agentd.Options{Root: rootPath}

	pid, err := readPidFile(opts.PidFilePath())
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	// Open a connection to the old daemon so we can detect when it shuts down.
	oldConn, err := net.DialTimeout("unix", opts.SocketPath(), 2*time.Second)
	if err != nil {
		return fmt.Errorf("daemon not reachable on socket: %w", err)
	}

	// Send SIGHUP to trigger re-exec.
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		oldConn.Close()
		return fmt.Errorf("send SIGHUP to pid %d: %w", pid, err)
	}
	fmt.Printf("daemon: sent SIGHUP to pid %d, waiting for restart...\n", pid)

	// Wait for old connection to break (daemon shutting down).
	deadline := time.Now().Add(30 * time.Second)
	_ = oldConn.SetReadDeadline(deadline)
	buf := make([]byte, 1)
	_, _ = oldConn.Read(buf) // blocks until EOF or error — means old daemon is gone
	oldConn.Close()

	// Poll until new daemon socket is connectable.
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)

		conn, err := net.DialTimeout("unix", opts.SocketPath(), 500*time.Millisecond)
		if err != nil {
			continue
		}
		conn.Close()

		fmt.Printf("daemon: restarted (pid: %d)\n", pid)
		return nil
	}

	return fmt.Errorf("timeout waiting for daemon restart (pid: %d)", pid)
}

func readPidFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}
