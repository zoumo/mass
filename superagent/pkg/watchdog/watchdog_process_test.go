package watchdog_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
	"github.com/zoumo/mass/superagent/pkg/watchdog"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "app-server" {
		if err := codexPeer(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestWatchdogProcess(t *testing.T) {
	binary := superagentBinary(t)
	endpoint, db := runCore(t)
	require.Equal(t, core.WatchdogSnapshot{}, readWatchdogSnapshot(t, endpoint))
	seedReports(t, db)
	codex, log := peerFixture(t, "suspected_missed_acceptance")
	descendants := wrapPeer(t, codex)
	first := runCLI(t, binary, endpoint, codex)
	require.Equal(t, "finding", first.Status, "%+v", first)
	require.Equal(t, "suspected_missed_acceptance", first.Reason)
	require.Equal(t, "mass-t1-i9-r1", first.ReportID, "the oldest eligible notification is on the second page")
	require.Equal(t, "mass-t1-i9", first.IssueID)
	require.Equal(t, "mass-t1", first.TopicID)
	require.Equal(t, 1, first.GoalRevision)
	require.Equal(t, "evt-9", first.EventID)
	require.WithinDuration(t, first.ObservedAt.Add(-2*time.Hour), first.SentAt, 10*time.Second)
	require.NotEmpty(t, first.Recommendation)

	second := runCLI(t, binary, endpoint, codex)
	require.Equal(t, first.ReportID, second.ReportID)
	events := readPeerEvents(t, log)
	require.Len(t, events, 6)
	for i := 0; i < len(events); i += 3 {
		require.Equal(t, []string{"initialize", "thread/start", "turn/start"}, []string{events[i].Method, events[i+1].Method, events[i+2].Method})
		require.False(t, events[i].HasCoreCredentials)
		require.True(t, events[i].HasAuthFile)
		require.False(t, events[i].HasUserConfig)
		require.True(t, events[i].HasProxy)
		require.NoFileExists(t, filepath.Join(events[i].Home, ".codex", "auth.json"))
		require.ErrorIs(t, syscall.Kill(events[i].PID, 0), syscall.ESRCH)
		require.Contains(t, string(events[i+1].Params), `"ephemeral":true`)
		require.Contains(t, string(events[i+1].Params), `"sandbox":"read-only"`)
		require.Contains(t, string(events[i+2].Params), `"outputSchema":`)
		require.Contains(t, string(events[i+2].Params), `\"notification\":`)
		require.Contains(t, string(events[i+2].Params), `\"recentTopicActivity\":`)
		require.Contains(t, string(events[i+2].Params), `\"reportId\":\"mass-t1-i9-r1\"`)
	}
	require.NotEqual(t, events[2].ThreadID, events[5].ThreadID)
	require.NotEqual(t, events[0].Home, events[3].Home)
	assertFixtureReleased(t, descendantPIDs(descendants), 2)
}

func TestWatchdogOutcomes(t *testing.T) {
	binary := superagentBinary(t)
	endpoint, db := runCore(t)
	codex, log := peerFixture(t, "unexpected")
	result := runCLI(t, binary, endpoint, codex)
	require.Equal(t, "no_finding", result.Status)
	require.Equal(t, "no_candidate", result.Reason)
	require.NoFileExists(t, log)

	seedReports(t, db)
	_, err := db.ExecContext(t.Context(), `UPDATE internal_events SET status='pending' WHERE event_id IN ('evt-8', 'evt-9')`)
	require.NoError(t, err)
	result = runCLI(t, binary, endpoint, codex)
	require.Equal(t, "no_candidate", result.Reason, "old goal, blocker, accepted, canceled, too recent, pending and wrong Topic deliveries are ineligible")
	require.NoFileExists(t, log)

	_, err = db.ExecContext(t.Context(), `UPDATE internal_events SET status='sent' WHERE event_id='evt-8'`)
	require.NoError(t, err)
	for _, scenario := range []struct {
		outcome, status, reason string
	}{
		{"normal_waiting", "no_finding", "normal_waiting"},
		{"insufficient_evidence", "incomplete", "insufficient_evidence"},
		{"invalid", "incomplete", "invalid_output"},
	} {
		t.Run(scenario.outcome, func(t *testing.T) {
			peer, trace := peerFixture(t, scenario.outcome)
			assessment := runCLI(t, binary, endpoint, peer)
			require.Equal(t, scenario.status, assessment.Status, "%+v", assessment)
			require.Equal(t, scenario.reason, assessment.Reason)
			require.False(t, assessment.ReleaseUnconfirmed)
			events := readPeerEvents(t, trace)
			require.Len(t, events, 3)
			require.ErrorIs(t, syscall.Kill(events[0].PID, 0), syscall.ESRCH)
		})
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadEndpoint := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	result = runCLI(t, binary, deadEndpoint, codex)
	require.Equal(t, "incomplete", result.Status)
	require.Equal(t, "core_read_failed", result.Reason)
	require.NoFileExists(t, log)
}

func TestWatchdogStopsCodex(t *testing.T) {
	binary := superagentBinary(t)
	endpoint, db := runCore(t)
	seedReports(t, db)
	for _, reason := range []string{"timed_out", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			codex, log := peerFixture(t, "hang")
			descendants := wrapPeer(t, codex)
			var extra []string
			if reason == "timed_out" {
				// The deadline includes starting the race-instrumented processes.
				extra = []string{"--timeout=10s"}
			}
			cmd := watchdogCommand(t, binary, endpoint, codex, extra...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			process := startFixtureProcess(t, cmd)
			awaitComposeTurn(t, log, 1)
			if reason == "canceled" {
				require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
			}
			require.NoError(t, process.wait(15*time.Second))
			result := decodeResult(t, stdout.Bytes())
			require.Equal(t, "incomplete", result.Status)
			require.Equal(t, reason, result.Reason)
			require.False(t, result.ReleaseUnconfirmed)
			events := readPeerEvents(t, log)
			require.Len(t, events, 3)
			assertFixtureReleased(t, []int{events[0].PID, events[0].ParentPID}, 2)
			assertFixtureReleased(t, descendantPIDs(descendants), 1)
			require.NoDirExists(t, events[0].Home)
		})
	}
}

func TestWatchdogContinuesAfterCandidateChanges(t *testing.T) {
	binary := superagentBinary(t)
	for _, scenario := range []string{"stale", "read_failed"} {
		t.Run(scenario, func(t *testing.T) {
			endpoint, db := runCore(t)
			seedReports(t, db)
			target, err := url.Parse(endpoint)
			require.NoError(t, err)
			proxy := &httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) { request.SetURL(target) }}
			var activityRead atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/core/topics/mass-t1/activity" && activityRead.CompareAndSwap(false, true) && scenario == "stale" {
					if _, err := db.ExecContext(r.Context(), `UPDATE issues SET body=json_set(body,'$.status','completed') WHERE id='mass-t1-i9'`); err != nil {
						http.Error(w, "fixture update failed", http.StatusInternalServerError)
						return
					}
				}
				if r.URL.Path == "/v1/core/issues/mass-t1-i9" && activityRead.Load() && scenario == "read_failed" {
					http.Error(w, "fixture read failed", http.StatusServiceUnavailable)
					return
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			codex, log := peerFixture(t, "suspected_missed_acceptance")
			result := runCLI(t, binary, server.URL, codex)
			require.True(t, activityRead.Load())
			if scenario == "read_failed" {
				require.Equal(t, "core_read_failed", result.Reason)
				require.NoFileExists(t, log)
				return
			}
			require.Equal(t, "finding", result.Status)
			require.Equal(t, "mass-t1-i8-r1", result.ReportID)
			require.Len(t, readPeerEvents(t, log), 3, "only the backup gets an Agent session")
		})
	}
}

func TestWatchdogReadsIssueWithLargeReportHistory(t *testing.T) {
	binary := superagentBinary(t)
	endpoint, db := runCore(t)
	seedReports(t, db)
	for i := range 11 {
		report := core.Report{
			ID: fmt.Sprintf("historical-%d", i), IssueID: "mass-t1-i9", TopicID: "mass-t1",
			Sequence: int64(61 + i), Revision: 0, Kind: "ready", Historical: true,
			CreatedAt: time.Now().Add(-3 * time.Hour).Format(time.RFC3339Nano),
			Details:   strings.Repeat("x", 850<<10),
		}
		body, err := json.Marshal(report)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `UPDATE issues SET body=json_insert(body,'$.reports[#]',json(?)) WHERE id='mass-t1-i9'`, string(body))
		require.NoError(t, err)
	}
	for _, view := range []struct {
		query  string
		method string
		status int
	}{
		{"", http.MethodGet, http.StatusOK},
		{"?view=summary", http.MethodGet, http.StatusOK},
		{"?view=summary", http.MethodHead, http.StatusOK},
		{"?view=unknown", http.MethodGet, http.StatusBadRequest},
	} {
		t.Run(view.method+view.query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, view.method, endpoint+"/v1/core/issues/mass-t1-i9"+view.query, http.NoBody)
			require.NoError(t, err)
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, response.Body.Close())
			require.NoError(t, err)
			require.Equal(t, view.status, response.StatusCode)
			if view.query == "" {
				require.Greater(t, len(body), 8<<20)
			} else if view.method == http.MethodHead {
				require.Empty(t, body)
			} else if view.status == http.StatusOK {
				require.Less(t, len(body), 2000)
				var summary core.IssueSummary
				require.NoError(t, json.Unmarshal(body, &summary))
				require.Equal(t, "mass-t1-i9", summary.ID)
				require.Equal(t, "Confirm the fixture output is healthy.", summary.Goal)
				require.NotContains(t, string(body), `"reports"`)
			}
		})
	}
	codex, _ := peerFixture(t, "suspected_missed_acceptance")
	result := runCLI(t, binary, endpoint, codex)
	require.Equal(t, "finding", result.Status, "%+v", result)
	require.Equal(t, "mass-t1-i9-r1", result.ReportID)
}

// Opt in to a real model turn using the caller's existing Codex authentication.
func TestWatchdogRealCodex(t *testing.T) {
	codex := os.Getenv("SUPERAGENT_WATCHDOG_REAL_CODEX")
	if codex == "" {
		t.Skip("set SUPERAGENT_WATCHDOG_REAL_CODEX to the real Codex executable")
	}
	binary := superagentBinary(t)
	endpoint, db := runCore(t)
	seedReports(t, db)
	result := runCLI(t, binary, endpoint, codex)
	require.False(t, result.ReleaseUnconfirmed)
	require.Contains(t, []string{"suspected_missed_acceptance", "normal_waiting", "insufficient_evidence"}, result.Reason)
	require.Equal(t, "evt-9", result.EventID)
	t.Logf("real Codex produced %s (%s) for %s", result.Status, result.Reason, result.ReportID)
}

func superagentBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("SUPERAGENT_CORE"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "superagent")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../../cmd/superagent")
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOWORK=off")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	return binary
}

func runCore(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	server, err := core.StartWithContext(t.Context(), core.Config{DataDir: dir, Listen: "127.0.0.1:0"}, core.TelemetryOptions{})
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Shutdown(ctx))
		require.NoError(t, <-served)
	})
	db, err := sql.Open("sqlite", filepath.Join(dir, "core.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return server.Endpoint(), db
}

func seedReports(t *testing.T, db *sql.DB) {
	t.Helper()
	at := time.Now().UTC().Add(-3 * time.Hour)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO topics(created_at,body,creation_key,creation_digest)
 VALUES(?1,'{"title":"Fixture Topic","status":"active"}','fixture','')`, at.Format(time.RFC3339Nano))
	require.NoError(t, err)
	for n := 1; n <= 9; n++ {
		id := fmt.Sprintf("mass-t1-i%d", n)
		binding, event := "binding-"+id, fmt.Sprintf("evt-%d", n)
		issue := core.Issue{
			ID: id, TopicID: "mass-t1", Status: "active", CurrentSessionBindingID: binding,
			Requirements: "Confirm the fixture output is healthy.", RequirementsRevision: 1,
			CreatedAt: at.Format(time.RFC3339Nano), UpdatedAt: at.Format(time.RFC3339Nano),
			Reports: []core.Report{{
				ID: id + "-r1", TopicID: "mass-t1", IssueID: id, SessionBindingID: binding,
				Revision: 1, Kind: "ready", Summary: "Fixture output checked.",
				Details:   "The fixture's results are ready for acceptance.",
				CreatedAt: at.Add(time.Duration(n) * time.Second).Format(time.RFC3339Nano), Sequence: int64(n), EventID: event,
			}},
		}
		sent, status, recipient := at, "sent", "mass-t1"
		switch n {
		case 1:
			issue.Status = "completed"
			issue.Completion = &core.Completion{Reason: "Accepted fixture output."}
		case 2:
			issue.RequirementsRevision++
		case 3:
			status = "pending"
		case 4:
			issue.Blockers = []core.Blocker{{ID: "wait", BlockerInput: core.BlockerInput{Kind: "human", Reason: "Waiting for manual verification."}}}
		case 5:
			sent = at.Add(3 * time.Hour)
		case 6:
			issue.Status = "canceled"
		case 7:
			recipient = "unrelated-topic"
		case 8:
			sent = at.Add(90 * time.Minute)
			for seq := 10; seq < 60; seq++ {
				issue.Reports = append(issue.Reports, core.Report{
					ID: fmt.Sprintf("%s-extra-%d", id, seq), Sequence: int64(seq), Kind: "progress",
					CreatedAt: at.Add(8 * time.Second).Format(time.RFC3339Nano),
				})
			}
		case 9:
			sent = at.Add(time.Hour)
			issue.Reports[0].Sequence = 60
		}
		body, err := json.Marshal(issue)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO issues(topic_id,number,created_at,body,creation_key,creation_digest)
 VALUES('mass-t1',?1,?2,?3,?4,'')`, n, issue.CreatedAt, string(body), id)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO internal_events(event_id,kind,sender,recipient,refs,created_at,status,sent_at)
 VALUES(?1,'report_submitted',json_object('kind','issue','session_binding_id',?2),json_object('kind','topic','topic_id',?3),
 json_array(json_object('kind','report','id',?4,'issue_id',?5,'goal_revision',1)),?6,?7,?8)`,
			event, binding, recipient, issue.Reports[0].ID, id, issue.CreatedAt, status, sent.Format(time.RFC3339Nano))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func watchdogCommand(t *testing.T, binary, endpoint, codex string, extra ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 150*time.Second)
	t.Cleanup(cancel)
	args := append([]string{"watchdog", "--endpoint", endpoint, "--codex-bin", codex}, extra...)
	return exec.CommandContext(ctx, binary, args...)
}

func runCLI(t *testing.T, binary, endpoint, codex string, extra ...string) watchdog.Result {
	t.Helper()
	cmd := watchdogCommand(t, binary, endpoint, codex, extra...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	process := startFixtureProcess(t, cmd)
	require.NoError(t, process.wait(150*time.Second))
	require.Empty(t, stderr.String())
	return decodeResult(t, stdout.Bytes())
}

func decodeResult(t *testing.T, output []byte) watchdog.Result {
	t.Helper()
	require.Equal(t, 1, bytes.Count(output, []byte{'\n'}), "%s", output)
	var result watchdog.Result
	require.NoError(t, json.Unmarshal(output, &result), "%s", output)
	return result
}

type peerEvent struct {
	Method             string          `json:"method"`
	Params             json.RawMessage `json:"params"`
	PID                int             `json:"pid"`
	ParentPID          int             `json:"parentPid"`
	ThreadID           string          `json:"threadId"`
	Home               string          `json:"home"`
	HasCoreCredentials bool            `json:"hasCoreCredentials"`
	HasAuthFile        bool            `json:"hasAuthFile"`
	HasUserConfig      bool            `json:"hasUserConfig"`
	HasProxy           bool            `json:"hasProxy"`
}

func peerFixture(t *testing.T, outcome string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	current, err := os.Executable()
	require.NoError(t, err)
	executable := filepath.Join(dir, "codex")
	require.NoError(t, os.Symlink(current, executable))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mode"), []byte(outcome), 0o600))
	auth := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(auth, "auth.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(auth, "config.toml"), []byte("fixture"), 0o600))
	t.Setenv("CODEX_HOME", auth)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("MASS_TOKEN", "fixture-core-credential")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("no_proxy", "fixture.local")
	log := filepath.Join(dir, "peer.ndjson")
	t.Cleanup(func() {
		pids := make(map[int]struct{})
		for _, event := range fixtureEvents(log) {
			if event.PID > 0 && event.ParentPID > 0 {
				pids[event.PID] = struct{}{}
				pids[event.ParentPID] = struct{}{}
			}
		}
		owned := make([]int, 0, len(pids))
		for pid := range pids {
			owned = append(owned, pid)
		}
		releaseFixturePIDs(t, owned)
	})
	return executable, log
}

func wrapPeer(t *testing.T, codex string) string {
	t.Helper()
	dir := filepath.Dir(codex)
	log := filepath.Join(dir, "descendants")
	require.NoError(t, os.Rename(codex, filepath.Join(dir, "peer")))
	require.NoError(t, os.WriteFile(codex, []byte(`#!/bin/sh
/bin/sleep 300 </dev/null >/dev/null 2>&1 &
printf '%s\n' "$!" >> "${0%/*}/descendants"
exec "${0%/*}/peer" "$@"
`), 0o700))
	t.Cleanup(func() { releaseFixturePIDs(t, descendantPIDs(log)) })
	return log
}

func descendantPIDs(log string) []int {
	data, _ := os.ReadFile(log)
	var pids []int
	for raw := range strings.FieldsSeq(string(data)) {
		if pid, err := strconv.Atoi(raw); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func readPeerEvents(t *testing.T, log string) []peerEvent {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(data))
	var events []peerEvent
	for {
		var event peerEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			return events
		}
		require.NoError(t, err)
		events = append(events, event)
	}
}

func codexPeer() error {
	dir := filepath.Dir(os.Args[0])
	mode, err := os.ReadFile(filepath.Join(dir, "mode"))
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "peer.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stderr, "fixture-native-stderr")
	pid := os.Getpid()
	threadID := "fixture-thread-" + strconv.Itoa(pid)
	turn := map[string]any{"id": "fixture-turn", "status": "inProgress", "items": []any{}}
	_, auth := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"))
	_, config := os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"))
	in, out, trace := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout), json.NewEncoder(log)
	in.Buffer(make([]byte, 4096), 1<<20)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			return err
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"userAgent": "fixture", "codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "unix", "platformOs": "linux"}
		case "initialized":
			continue
		case "thread/start":
			result = map[string]any{
				"thread": map[string]any{
					"id": threadID, "ephemeral": true, "preview": "", "modelProvider": "fixture", "createdAt": 1,
					"updatedAt": 1, "status": map[string]any{"type": "idle"}, "cwd": cwd, "cliVersion": "0",
					"source": "cli", "turns": []any{}, "sessionId": threadID, "projectId": nil,
				},
				"model": "fixture", "modelProvider": "fixture", "cwd": cwd, "approvalPolicy": "never",
				"approvalsReviewer": "user", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false},
			}
		case "turn/start":
			result = map[string]any{"turn": turn}
		default:
			return fmt.Errorf("unexpected request %q", req.Method)
		}
		if err := trace.Encode(peerEvent{
			Method: req.Method, Params: req.Params, PID: pid, ParentPID: os.Getppid(),
			ThreadID: threadID, Home: os.Getenv("HOME"),
			HasCoreCredentials: os.Getenv("MASS_TOKEN") != "", HasAuthFile: auth == nil, HasUserConfig: config == nil,
			HasProxy: os.Getenv("HTTPS_PROXY") == "http://127.0.0.1:1" && os.Getenv("no_proxy") == "fixture.local",
		}); err != nil {
			return err
		}
		if err := out.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			return err
		}
		if req.Method == "turn/start" && string(mode) != "hang" {
			outcome := string(mode)
			if outcome == "gated-invalid" {
				outcome = "invalid"
				// Observe the release marker, even if the fixture is slow to advance.
				for deadline := time.Now().Add(time.Minute); ; {
					_, err := os.Stat(filepath.Join(dir, "release"))
					if err == nil {
						break
					}
					if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
						return err
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if err := completePeerTurn(out, threadID, outcome); err != nil {
				return err
			}
		}
	}
	if string(mode) == "hang" {
		// Simulate an uncooperative app-server after stdin closes.
		time.Sleep(30 * time.Second)
	}
	return in.Err()
}

func completePeerTurn(out *json.Encoder, threadID, mode string) error {
	data, err := json.Marshal(map[string]string{"outcome": mode, "judgment": "Fixture assessment.", "recommendation": "Review the fixture report."})
	if err != nil {
		return err
	}
	if mode == "invalid" {
		data = []byte("invalid non-JSON output")
	}
	item := func(turn, phase, text string) error {
		return out.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
			"threadId": threadID, "turnId": turn, "completedAtMs": 2, "item": map[string]any{
				"type": "agentMessage", "id": turn + "-" + phase, "phase": phase, "text": text,
			},
		}})
	}
	if err := item("fixture-turn", "final_answer", string(data)); err != nil {
		return err
	}
	if err := item("other-turn", "final_answer", "unrelated turn"); err != nil {
		return err
	}
	if err := item("fixture-turn", "commentary", "commentary"); err != nil {
		return err
	}
	return out.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
		"threadId": threadID, "turn": map[string]any{"id": "fixture-turn", "status": "completed", "items": []any{}},
	}})
}
