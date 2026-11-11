package recordingtest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestRecordingDiagnosticMissingExecutable(t *testing.T) {
	if os.Getenv("MASS_DIAGNOSTIC_CHILD") == "1" {
		RunRecordCodexBasic(t)
		return
	}
	output := filepath.Join(t.TempDir(), "evidence")
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRecordingDiagnosticMissingExecutable$")
	command.Env = append(os.Environ(), "MASS_DIAGNOSTIC_CHILD=1", "MASS_RECORD=1",
		"MASS_RECORD_CODEX=/missing/mass-ticket03-codex",
		"MASS_RECORD_MODEL=explicit-test-model", "MASS_RECORD_DIAGNOSTICS="+output,
		"MASS_RECORD_OUTPUT="+filepath.Join(filepath.Dir(output), "export"),
		"GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	log, err := command.CombinedOutput()
	require.Error(t, err, "%s", log)
	report := readDiagnosis(t, filepath.Join(output, "diagnosis.json"))
	require.Equal(t, "blocker", report.Status)
	require.Equal(t, "executable", report.LastStage)
	require.NotEmpty(t, report.Blockers)
	require.Contains(t, report.Blockers[0], "executable unavailable")
}

func readDiagnosis(t *testing.T, path string) DiagnosticReport {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var report DiagnosticReport
	require.NoError(t, json.Unmarshal(data, &report))
	return report
}

func TestRecordingDiagnosticPreflightStageTimeout(t *testing.T) {
	t.Setenv("MASS_RECORD_MODEL", "")
	t.Setenv("TICKET03_DIAGNOSTIC_TOKEN", "ticket03-secret-value")
	root := t.TempDir()
	executable := filepath.Join(root, "slow-cli")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\necho \"token=$TICKET03_DIAGNOSTIC_TOKEN\" >&2\nsleep 20\n"), 0o700))
	report, err := Diagnose(context.Background(), DiagnosticOptions{
		Provider: "codex", Executable: executable, Output: filepath.Join(root, "timeout-report"),
	})
	require.Error(t, err)
	require.Equal(t, "blocker", report.Status)
	require.Equal(t, "version", report.LastStage)
	var stage DiagnosticStage
	for _, candidate := range report.Stages {
		if candidate.Name == "version" {
			stage = candidate
		}
	}
	require.Equal(t, "version", stage.Name)
	require.True(t, stage.TimedOut)
	require.Equal(t, 10*time.Second, stage.Budget)
	require.True(t, stage.ProcessGone)
	require.True(t, stage.GroupGone)
	stderr, readErr := os.ReadFile(stage.Stderr)
	require.NoError(t, readErr)
	require.Contains(t, string(stderr), "<REDACTED>")
	require.NotContains(t, string(stderr), "ticket03-secret-value")

	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 2.1.258; fi\nexit 0\n"), 0o700))
	report, err = Diagnose(context.Background(), DiagnosticOptions{
		Provider: "claude", Executable: executable, Output: filepath.Join(root, "missing-model"),
	})
	require.Error(t, err)
	require.Equal(t, "blocker", report.Status)
	require.Contains(t, report.Blockers, "explicit model required: --model or MASS_RECORD_MODEL; personal settings are not consulted")
	require.Contains(t, report.Blockers, "CLI authentication status not confirmed")
}

func TestRecordingDiagnosticRejectsExistingOutput(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(output, 0o700))
	_, err := Diagnose(context.Background(), DiagnosticOptions{Provider: "codex", Output: output})
	require.Error(t, err)
	entries, readErr := os.ReadDir(output)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestRecordingDiagnosticCLIExitCodes(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "mass-record-diagnose")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/mass-record-diagnose")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	buildOutput, err := build.CombinedOutput()
	require.NoError(t, err, "%s", buildOutput)

	run := func(args ...string) (int, string) {
		command := exec.CommandContext(t.Context(), binary, args...)
		data, runErr := command.CombinedOutput()
		if runErr == nil {
			return 0, string(data)
		}
		var exit *exec.ExitError
		require.ErrorAs(t, runErr, &exit)
		return exit.ExitCode(), string(data)
	}
	code, output := run("--help")
	require.Zero(t, code, output)
	require.Contains(t, output, "--smoke")

	reportPath := filepath.Join(t.TempDir(), "missing")
	code, output = run("--provider", "codex", "--executable", "/missing/ticket03", "--model", "explicit-model", "--output", reportPath)
	require.Equal(t, 1, code, output)
	report := readDiagnosis(t, filepath.Join(reportPath, "diagnosis.json"))
	require.Equal(t, "blocker", report.Status)

	fake := filepath.Join(t.TempDir(), "codex")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo codex-test;;\nexec) echo '{\"type\":\"turn.started\"}';;\nesac\n"), 0o700))
	base := []string{"--provider", "codex", "--executable", fake, "--model", "explicit-model"}
	code, output = run(append(base, "--output", filepath.Join(t.TempDir(), "preflight"))...)
	require.Zero(t, code, output)
	require.Contains(t, output, "status=preflight_only")
	incomplete := filepath.Join(t.TempDir(), "incomplete")
	code, output = run(append(base, "--output", incomplete, "--smoke")...)
	require.Equal(t, 1, code, output)
	report = readDiagnosis(t, filepath.Join(incomplete, "diagnosis.json"))
	require.Equal(t, "blocker", report.Status)
	require.Equal(t, "native-smoke", report.LastStage)
	require.Contains(t, strings.Join(report.Blockers, " "), "without successful completion and assistant message")
}

func TestRecordingFlowCleanupOnMissingNative(t *testing.T) {
	if fault := os.Getenv("MASS_FLOW_CLEANUP_CHILD"); fault != "" {
		h := newHarness(t)
		scratch := t.TempDir()
		h.reserveDiagnostic("codex", scratch)
		h.phase = "flow-cleanup-fault"
		executable := "/missing/ticket03-native"
		var budget time.Duration
		if fault == "deadline" {
			executable = filepath.Join(scratch, "blocked-native")
			require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\necho $$ > "+quote(filepath.Join(h.diagnostic.root, "native.pid"))+"\nexec sleep 30\n"), 0o700))
			budget = 2 * time.Second
		}
		h.flow(route{
			Workspace: scratch,
			Env: map[string]string{
				"HOME": os.Getenv("HOME"), "PATH": os.Getenv("PATH"), "TMPDIR": os.Getenv("TMPDIR"),
			},
		}, executable, flowCase{
			name: "cleanup", project: scratch, prompt: prompt, budget: budget,
		})
		return
	}
	for _, fault := range []string{"missing", "deadline"} {
		t.Run(fault, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "diagnosis")
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRecordingFlowCleanupOnMissingNative$")
			command.Env = append(os.Environ(), "MASS_FLOW_CLEANUP_CHILD="+fault, "MASS_RECORD_DIAGNOSTICS="+output,
				"MASS_RECORD_MODEL=explicit-test-model", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
			log, err := command.CombinedOutput()
			require.Error(t, err, "%s", log)
			report := readDiagnosis(t, filepath.Join(output, "diagnosis.json"))
			require.Equal(t, "blocker", report.Status)
			require.NotNil(t, report.ListenerClosed)
			require.True(t, *report.ListenerClosed)
			require.NotEmpty(t, report.SDKCleanup)
			for _, stage := range report.Stages {
				if stage.ProcessRole == "massd" {
					require.True(t, stage.ProcessGone)
					require.True(t, stage.GroupGone, "cleanup=%v sdk=%s child=%s", report.Cleanup, report.SDKCleanup, log)
				}
			}
			if fault == "deadline" {
				require.Equal(t, 2*time.Second, report.FlowBudget)
				require.True(t, report.FlowTimedOut)
				data, readErr := os.ReadFile(filepath.Join(output, "native.pid"))
				require.NoError(t, readErr)
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				require.NoError(t, parseErr)
				require.True(t, processGone(pid), "owned blocked native process must be reaped")
			} else {
				require.Equal(t, "process_and_wait_confirmed", report.Cleanup["massd"])
			}
		})
	}
}

func TestRecordingDiagnosticCodexMassSmoke(t *testing.T) {
	if os.Getenv("MASS_DIAGNOSE_CODEX_MASS") != "1" {
		t.Skip("set MASS_DIAGNOSE_CODEX_MASS=1 for one real 60-second MASS/SDK smoke")
	}
	h := newHarness(t)
	scratch, err := recordingDirectory()
	require.NoError(t, err)
	h.reserveDiagnostic("codex", scratch)
	h.phase = "preflight"
	identity := h.preflight("codex")
	codex, version := identity.Executable, identity.Version
	recordProject := filepath.Join(scratch, "project")
	session := filepath.Join(scratch, "session")
	require.NoError(t, os.MkdirAll(recordProject, 0o700))
	selectedModel := identity.Model
	plan := filepath.Join(scratch, "plan.json")
	writeJSON(t, plan, map[string]any{
		"version": 1, "agentType": "codex", "caseId": "mass-diagnose-codex",
		"driverProfile": "consumer", "project": recordProject, "cliVersion": version, "model": selectedModel,
	})
	h.phase = "testify-prepare"
	route := h.prepare(session, "--plan", plan, "record", recordProject)
	cli := filepath.Join(scratch, "codex")
	require.NoError(t, os.Symlink(codex, cli))
	native := filepath.Join(scratch, "native-codex")
	require.NoError(t, os.WriteFile(native, []byte("#!/bin/sh\ncd "+quote(recordProject)+" || exit 1\nexec "+quote(cli)+" -c 'project_root_markers=[]' -c 'project_doc_max_bytes=0' \"$@\"\n"), 0o700))
	wrapper := filepath.Join(scratch, "record-codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(h.tool)+" agent record --fixture-dir "+quote(session)+" --cmd "+quote(native)+" -- \"$@\"\n"), 0o700))
	h.phase = "mass-sdk-smoke"
	observed := h.flow(route, wrapper, flowCase{
		name: "diagnostic", prompt: prompt, project: recordProject, evidence: scratch, budget: time.Minute,
		sessionConfig: &runtime.InitialSessionConfig{Model: selectedModel},
	})
	require.True(t, observed.Accepted)
	require.NotEmpty(t, observed.Events)
	h.diagnostic.report.Status = "ready"
	h.diagnostic.report.Boundaries["testify_export_strict_replay"] = "not_executed"
	t.Logf("MASS/SDK smoke ready; report=%s/diagnosis.json session=%s model=%s", h.diagnostic.root, session, selectedModel)
}
