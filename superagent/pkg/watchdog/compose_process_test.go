package watchdog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/pkg/watchdog"
)

func TestWatchdogCompose(t *testing.T) {
	binary := superagentBinary(t)
	dir := t.TempDir()
	coreDir := filepath.Join(dir, "core-state")
	store, err := core.Open(core.Config{DataDir: coreDir})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	db, err := sql.Open("sqlite", filepath.Join(coreDir, "core.sqlite"))
	require.NoError(t, err)
	seedReports(t, db)
	require.NoError(t, db.Close())

	codex, peerLog := peerFixture(t, "gated-invalid")
	peerDir := filepath.Dir(codex)
	const interval = 300 * time.Millisecond
	path := filepath.Join(dir, "stack.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "source-token"), []byte(strings.Repeat("x", 40)), 0o600))
	config := fmt.Sprintf(`core:
  listen: 127.0.0.1:0
  dataDir: core-state
  sources:
    - {source: fixture, accountId: fixture, tokenFile: source-token}
watchdog: {interval: %s}
`, interval)
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))

	pid, logs, stop := startWatchdogCompose(t, binary, path, peerDir)
	first := awaitComposeTurn(t, peerLog, 1)
	require.NoError(t, syscall.Kill(first.ParentPID, 0))
	require.NotEqual(t, pid, first.ParentPID, "native Codex is owned by the Watchdog child")
	// Keep the first round in flight across multiple intervals. The next round
	// must wait for its completion and start after a full interval.
	require.Never(t, func() bool { return len(composeTurns(peerLog)) > 1 }, 2*interval, 15*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "mode"), []byte("suspected_missed_acceptance"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "release"), nil, 0o600))
	second := awaitComposeTurn(t, peerLog, 2)
	require.Equal(t, first.ParentPID, second.ParentPID)
	require.NotEqual(t, first.ThreadID, second.ThreadID)
	require.ErrorIs(t, syscall.Kill(first.PID, 0), syscall.ESRCH)
	var rounds []composeWatchdogLog
	require.Eventually(t, func() bool {
		rounds = composeWatchdogRounds(logs)
		return len(rounds) >= 2
	}, 10*time.Second, 15*time.Millisecond)
	require.Equal(t, "invalid_output", rounds[0].Result.Reason)
	require.Equal(t, "finding", rounds[1].Result.Status)
	require.Equal(t, "mass-t1-i9-r1", rounds[1].Result.ReportID)
	require.GreaterOrEqual(t, rounds[1].Result.ObservedAt.Sub(rounds[0].At), interval)
	stop()
	require.ErrorIs(t, syscall.Kill(second.PID, 0), syscall.ESRCH)
	require.ErrorIs(t, syscall.Kill(first.ParentPID, 0), syscall.ESRCH)

	// Keep the original Report in Core. Restart should pick it up in the
	// first round, even though the new interval is much longer.
	count := len(composeTurns(peerLog))
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(config, interval.String(), "1h", 1)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "mode"), []byte("hang"), 0o600))
	_, logs, stop = startWatchdogCompose(t, binary, path, peerDir)
	restarted := awaitComposeTurn(t, peerLog, count+1)
	require.NotEqual(t, first.ParentPID, restarted.ParentPID)
	require.NotEqual(t, second.ThreadID, restarted.ThreadID)
	require.Contains(t, string(restarted.Params), `\"reportId\":\"mass-t1-i9-r1\"`)
	stop()
	require.ErrorIs(t, syscall.Kill(restarted.PID, 0), syscall.ESRCH)
	require.ErrorIs(t, syscall.Kill(restarted.ParentPID, 0), syscall.ESRCH)
	rounds = composeWatchdogRounds(logs)
	require.Len(t, rounds, 1)
	require.Equal(t, "canceled", rounds[0].Result.Reason)
	require.False(t, rounds[0].Result.ReleaseUnconfirmed)
}

func startWatchdogCompose(t *testing.T, binary, path, peerDir string) (int, string, func()) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "startup-")
	require.NoError(t, err)
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "logs-")
	require.NoError(t, err)
	defer stderr.Close()
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), binary, "compose", "--config", path)
	cmd.Env = append(os.Environ(), "PATH="+peerDir, "GORACE=atexit_sleep_ms=0", "OTEL_TRACES_EXPORTER=none", "OTEL_METRICS_EXPORTER=none")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			logs, _ := os.ReadFile(stderr.Name())
			require.NoError(t, err, "%s", logs)
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Fatal("Compose did not stop")
		}
	}
	t.Cleanup(stop)
	require.Eventually(t, func() bool {
		var ready struct{ Status string }
		data, _ := os.ReadFile(stdout.Name())
		return json.Unmarshal(data, &ready) == nil && ready.Status == "ready"
	}, 12*time.Second, 15*time.Millisecond)
	return cmd.Process.Pid, stderr.Name(), stop
}

func composeTurns(log string) []peerEvent {
	data, _ := os.ReadFile(log)
	var events []peerEvent
	for line := range strings.SplitSeq(string(data), "\n") {
		var event peerEvent
		if json.Unmarshal([]byte(line), &event) == nil && event.Method == "turn/start" {
			events = append(events, event)
		}
	}
	return events
}

func awaitComposeTurn(t *testing.T, log string, count int) peerEvent {
	t.Helper()
	var turns []peerEvent
	require.Eventually(t, func() bool {
		turns = composeTurns(log)
		return len(turns) >= count
	}, 12*time.Second, 15*time.Millisecond)
	return turns[count-1]
}

type composeWatchdogLog struct {
	At        time.Time       `json:"time"`
	Component string          `json:"component"`
	Result    watchdog.Result `json:"result"`
}

func composeWatchdogRounds(log string) []composeWatchdogLog {
	data, _ := os.ReadFile(log)
	var rounds []composeWatchdogLog
	for line := range strings.SplitSeq(string(data), "\n") {
		var entry composeWatchdogLog
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Component == "watchdog" && entry.Result.Status != "" {
			rounds = append(rounds, entry)
		}
	}
	return rounds
}
