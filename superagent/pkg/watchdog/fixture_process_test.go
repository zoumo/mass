package watchdog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fixtureProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error // Published by closing done.
}

func startFixtureProcess(t *testing.T, cmd *exec.Cmd) *fixtureProcess {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	process := &fixtureProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()
	t.Cleanup(func() { _ = process.stop(t) })
	return process
}

var errFixtureWait = errors.New("fixture process wait timed out")

func (p *fixtureProcess) wait(timeout time.Duration) error {
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(timeout):
		return errFixtureWait
	}
}

func (p *fixtureProcess) stop(t *testing.T) error {
	t.Helper()
	select {
	case <-p.done:
		return p.waitErr
	default:
	}
	pids, err := fixtureProcessTree(p.cmd.Process.Pid)
	if err != nil {
		t.Errorf("cannot identify fixture descendants: %v", err)
	}
	defer releaseFixturePIDs(t, pids)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("cannot stop fixture process: %v", err)
	}
	if err := p.wait(20 * time.Second); !errors.Is(err, errFixtureWait) {
		return err
	}
	_ = p.cmd.Process.Kill()
	err = p.wait(5 * time.Second)
	t.Errorf("fixture process exceeded shutdown timeout: %v", err)
	return err
}

func fixtureParentPID(t *testing.T, pid int) int {
	t.Helper()
	output, err := exec.CommandContext(t.Context(), "/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	require.NoError(t, err)
	parent, err := strconv.Atoi(strings.TrimSpace(string(output)))
	require.NoError(t, err)
	require.Positive(t, parent)
	return parent
}

func fixtureProcessTree(root int) ([]int, error) {
	// Cleanup can inspect descendants after the test context is canceled.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int)
	for line := range strings.SplitSeq(string(output), "\n") {
		var pid, parent int
		if _, err := fmt.Sscan(line, &pid, &parent); err == nil && pid > 0 && pid != parent {
			children[parent] = append(children[parent], pid)
		}
	}
	pids := []int{root}
	for i := 0; i < len(pids); i++ {
		pids = append(pids, children[pids[i]]...)
	}
	return pids, nil
}

func releaseFixturePIDs(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if !fixturePIDReleased(pid) {
			t.Errorf("fixture process %d remained after shutdown", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	for _, pid := range pids {
		if !fixturePIDReleased(pid) {
			t.Errorf("could not confirm release of fixture process %d", pid)
		}
	}
}

func assertFixtureReleased(t *testing.T, pids []int, count int) {
	t.Helper()
	require.Len(t, pids, count)
	for _, pid := range pids {
		require.True(t, fixturePIDReleased(pid), "fixture process %d remained", pid)
	}
}

func fixturePIDReleased(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
