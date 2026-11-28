package watchdog_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
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
	seedComposeReports(t, coreDir)

	codex, peerLog := peerFixture(t, "gated-invalid")
	descendants := wrapPeer(t, codex)
	peerDir := filepath.Dir(codex)
	const interval = 300 * time.Millisecond
	path, config := writeWatchdogCompose(t, dir, interval)
	pid, endpoint, logs, process := startWatchdogCompose(t, binary, path, peerDir)
	first := awaitComposeTurn(t, peerLog, 1)
	child := fixtureParentPID(t, first.ParentPID)
	require.Equal(t, pid, fixtureParentPID(t, child), "native Codex is owned by the Watchdog child")
	require.Equal(t, core.WatchdogSnapshot{Enabled: true, Running: true}, readWatchdogSnapshot(t, endpoint))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/apis/core/v1/watchdog", http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Origin", "https://foreign.example")
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	// Keep the first round in flight across multiple intervals. The next round
	// must wait for its completion and start after a full interval.
	require.Never(t, func() bool { return len(composeTurns(peerLog)) > 1 }, 2*interval, 15*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "mode"), []byte("suspected_missed_acceptance"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "release"), nil, 0o600))
	second := awaitComposeTurn(t, peerLog, 2)
	require.NotEqual(t, first.ParentPID, second.ParentPID)
	require.NotEqual(t, first.ThreadID, second.ThreadID)
	assertFixtureReleased(t, []int{first.PID, first.ParentPID}, 2)
	var rounds []composeWatchdogLog
	require.Eventually(t, func() bool {
		rounds = composeWatchdogRounds(logs)
		return len(rounds) >= 2
	}, 10*time.Second, 15*time.Millisecond)
	require.Equal(t, "invalid_output", rounds[0].Result.Reason)
	require.Equal(t, "finding", rounds[1].Result.Status, "%+v", rounds[1].Result)
	require.Equal(t, "mass-t1-i9-r1", rounds[1].Result.ReportID)
	require.Equal(t, "evt-9", rounds[1].Result.EventID)
	require.GreaterOrEqual(t, rounds[1].Result.ObservedAt.Sub(rounds[0].At), interval)
	var snapshot core.WatchdogSnapshot
	require.Eventually(t, func() bool {
		snapshot = readWatchdogSnapshot(t, endpoint)
		return snapshot.Latest != nil && snapshot.Latest.Status == "finding"
	}, 2*time.Second, 15*time.Millisecond)
	require.True(t, snapshot.Enabled)
	require.True(t, snapshot.Running)
	require.Equal(t, rounds[1].Result.TopicID, snapshot.Latest.TopicID)
	require.Equal(t, rounds[1].Result.IssueID, snapshot.Latest.IssueID)
	require.Equal(t, "Fixture assessment.", snapshot.Latest.Judgment)
	require.Equal(t, "Review the fixture report.", snapshot.Latest.Recommendation)
	require.Empty(t, rounds[1].Result.Judgment)
	require.Empty(t, rounds[1].Result.Recommendation)
	data, err := os.ReadFile(logs)
	require.NoError(t, err)
	for _, text := range []string{"judgment", "recommendation", snapshot.Latest.Judgment, snapshot.Latest.Recommendation, "Fixture output checked.", "fixture-core-credential", "fixture-native-stderr"} {
		require.NotContains(t, string(data), text)
	}
	// Ensure the next round is in flight before killing Watchdog outright.
	require.NoError(t, os.WriteFile(filepath.Join(peerDir, "mode"), []byte("hang"), 0o600))
	active := awaitComposeTurn(t, peerLog, 3)
	require.NoError(t, syscall.Kill(child, syscall.SIGKILL))
	require.Eventually(t, func() bool {
		logs, _ := os.ReadFile(logs)
		return strings.Contains(string(logs), `"msg":"Watchdog exited"`) && strings.Contains(string(logs), `"releaseUnconfirmed":false`)
	}, 5*time.Second, 15*time.Millisecond)
	stopped := readWatchdogSnapshot(t, endpoint)
	require.True(t, stopped.Enabled)
	require.False(t, stopped.Running)
	require.Equal(t, snapshot.Latest, stopped.Latest)
	assertFixtureReleased(t, []int{child, active.PID, active.ParentPID}, 3)
	assertFixtureReleased(t, descendantPIDs(descendants), 3)
	require.NoDirExists(t, active.Home)
	require.NoError(t, process.stop(t))

	// Keep the original Report in Core. Restart should pick it up in the
	// first round, even though the new interval is much longer.
	count := len(composeTurns(peerLog))
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(config, interval.String(), "1h", 1)), 0o600))
	_, endpoint, logs, process = startWatchdogCompose(t, binary, path, peerDir)
	restarted := awaitComposeTurn(t, peerLog, count+1)
	require.NotEqual(t, active.ParentPID, restarted.ParentPID)
	require.NotEqual(t, active.ThreadID, restarted.ThreadID)
	require.Contains(t, string(restarted.Params), `\"reportId\":\"mass-t1-i9-r1\"`)
	require.Equal(t, core.WatchdogSnapshot{Enabled: true, Running: true}, readWatchdogSnapshot(t, endpoint))
	require.NoError(t, process.stop(t))
	assertFixtureReleased(t, []int{restarted.PID, restarted.ParentPID}, 2)
	assertFixtureReleased(t, descendantPIDs(descendants), 4)
	require.NoDirExists(t, restarted.Home)
	rounds = composeWatchdogRounds(logs)
	require.Len(t, rounds, 1)
	require.Equal(t, "canceled", rounds[0].Result.Reason)
	require.False(t, rounds[0].Result.ReleaseUnconfirmed)
}

func TestWatchdogComposeForcedStop(t *testing.T) {
	binary := superagentBinary(t)
	dir := t.TempDir()
	seedComposeReports(t, filepath.Join(dir, "core-state"))
	path, _ := writeWatchdogCompose(t, dir, time.Hour)
	codex, log := peerFixture(t, "hang")
	descendants := wrapPeer(t, codex)
	_, endpoint, logs, process := startWatchdogCompose(t, binary, path, filepath.Dir(codex))
	active := awaitComposeTurn(t, log, 1)
	child := fixtureParentPID(t, active.ParentPID)
	require.Equal(t, core.WatchdogSnapshot{Enabled: true, Running: true}, readWatchdogSnapshot(t, endpoint))
	// Freeze Watchdog mid-turn so Compose must escalate its stop to SIGKILL.
	require.NoError(t, syscall.Kill(child, syscall.SIGSTOP))
	require.Error(t, process.stop(t))
	data, err := os.ReadFile(logs)
	require.NoError(t, err)
	require.Contains(t, string(data), "watchdog exceeded shutdown timeout")
	require.NotContains(t, string(data), "release unconfirmed")
	assertFixtureReleased(t, []int{child, active.ParentPID, active.PID}, 3)
	assertFixtureReleased(t, descendantPIDs(descendants), 1)
	require.NoDirExists(t, active.Home)
}

func seedComposeReports(t *testing.T, coreDir string) {
	t.Helper()
	store, err := core.OpenStore(core.Config{DataDir: coreDir}, core.TelemetryOptions{})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	db, err := sql.Open("sqlite", filepath.Join(coreDir, "core.sqlite"))
	require.NoError(t, err)
	seedReports(t, db)
	require.NoError(t, db.Close())
}

func writeWatchdogCompose(t *testing.T, dir string, interval time.Duration) (string, string) {
	t.Helper()
	path := filepath.Join(dir, "stack.yaml")
	config := fmt.Sprintf(`core:
  listen: 127.0.0.1:0
  dataDir: core-state
watchdog: {interval: %s}
`, interval)
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	return path, config
}

func readWatchdogSnapshot(t *testing.T, endpoint string) core.WatchdogSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/apis/core/v1/watchdog", http.NoBody)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var snapshot core.WatchdogSnapshot
	require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
	return snapshot
}

func startWatchdogCompose(t *testing.T, binary, path, peerDir string) (int, string, string, *fixtureProcess) {
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
	process := startFixtureProcess(t, cmd)
	var ready struct{ Status, Endpoint string }
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(stdout.Name())
		return json.Unmarshal(data, &ready) == nil && ready.Status == "ready" && ready.Endpoint != ""
	}, 12*time.Second, 15*time.Millisecond)
	return cmd.Process.Pid, ready.Endpoint, stderr.Name(), process
}

func composeTurns(log string) []peerEvent {
	var turns []peerEvent
	for _, event := range fixtureEvents(log) {
		if event.Method == "turn/start" {
			turns = append(turns, event)
		}
	}
	return turns
}

func fixtureEvents(log string) []peerEvent {
	data, _ := os.ReadFile(log)
	var events []peerEvent
	for line := range strings.SplitSeq(string(data), "\n") {
		var event peerEvent
		if json.Unmarshal([]byte(line), &event) == nil {
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
