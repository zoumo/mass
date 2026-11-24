package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-go/codex"
)

const (
	AgentGuardCommand = "watchdog-agent-guard"
	AgentGuardTimeout = 3 * time.Second
)

// AgentGuardEvent travels on the private Compose results pipe. The guard keeps
// its end open until it exits, even when the Watchdog process is killed.
type AgentGuardEvent struct {
	GuardPID int  `json:"guardPid"`
	Released bool `json:"released,omitempty"`
}

func startAgentGuard(executable, cwd string, env []string, progress *os.File) (codex.StreamTransport, error) {
	self, err := os.Executable()
	if err != nil {
		return codex.StreamTransport{}, err
	}
	if progress == nil {
		progress, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return codex.StreamTransport{}, err
		}
		defer progress.Close()
	}
	lease, release, err := os.Pipe()
	if err != nil {
		return codex.StreamTransport{}, err
	}
	defer lease.Close()
	stdout, writer, err := os.Pipe()
	if err != nil {
		_ = release.Close()
		return codex.StreamTransport{}, err
	}
	defer writer.Close()
	// The lease controls the guard's lifetime so it can clean up the app-server group.
	cmd := exec.CommandContext(context.Background(), self, AgentGuardCommand, executable)
	cmd.Dir, cmd.Env, cmd.Stdout = cwd, env, writer
	// A terminal interrupt is handled by Watchdog. The guard survives to
	// close the native group when the Watchdog releases its lease.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{lease, progress}
	stdin, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		_ = stdout.Close()
		_ = release.Close()
		if stdin != nil {
			_ = stdin.Close()
		}
		return codex.StreamTransport{}, err
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	return codex.StreamTransport{
		Reader: stdout, Writer: stdin,
		Wait: func(ctx context.Context) error {
			_ = release.Close()
			select {
			case <-done:
				return waitErr
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, nil
}

// RunAgentGuard owns the app-server process group. A separate lease detects
// Watchdog death independently of SDK shutdown or stdio inherited by Codex.
func RunAgentGuard(executable string) error {
	lease := os.NewFile(3, "watchdog lease")
	defer lease.Close()
	progress := os.NewFile(4, "watchdog progress")
	// Keep progress open until process exit so Compose can wait for the guard.
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	event := AgentGuardEvent{GuardPID: os.Getpid()}
	output := json.NewEncoder(progress)
	if err := output.Encode(event); err != nil {
		return err
	}
	if err := ReapOrphans(); err != nil {
		// Fail the SDK initialization by closing its streams. No agent started.
		event.Released = true
		return output.Encode(event)
	}
	// The guard stops and waits for the whole app-server group.
	cmd := exec.CommandContext(context.Background(), executable, "app-server")
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		// Starting failed before Codex could create descendants.
		event.Released = true
		return output.Encode(event)
	}
	_ = os.Stdin.Close()
	_ = os.Stdout.Close()
	leaseEnded := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, lease)
		close(leaseEnded)
	}()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-leaseEnded:
		// The SDK closes app-server stdin before giving up the lease.
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
		}
	}

	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := awaitProcessExit(ctx, -pgid); err != nil {
		return err
	}
	event.Released = true
	return output.Encode(event)
}

// ConfirmAgentGuardExit also reaps the guard if Compose adopted it after the
// Watchdog was forced to exit. Normal rounds are reaped by the Watchdog.
func ConfirmAgentGuardExit(ctx context.Context, pid int) error {
	return awaitProcessExit(ctx, pid)
}

func awaitProcessExit(ctx context.Context, id int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Wait only on the owned PID or group. Other children have exec.Cmd waiters.
		for {
			pid, err := syscall.Wait4(id, nil, syscall.WNOHANG, nil)
			if pid <= 0 && !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		err := syscall.Kill(id, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// Darwin may return EPERM for a group of exiting processes until
		// the kernel removes their entries. Require ESRCH to confirm release.
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
