package testpeer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fakeTool = `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'agent-sdk-testify test-version'
  exit 0
fi
out=$(dirname "$3")
if [ -f "$out/sleep" ]; then
  exec sleep "$(cat "$out/sleep")"
fi
if [ ! -f "$out/skip-finish" ]; then
  cp "$out/finish.expected" "$out/finish.json"
fi
if [ ! -f "$out/skip-cleanup" ]; then
  cp "$out/cleanup.expected" "$out/cleanup.json"
fi
cat "$out/result.expected"
if [ -f "$out/remove-root" ]; then
  root=$(cat "$out/remove-root")
  rm -f "$root/launcher"
  rmdir "$root"
fi
if [ -f "$out/exit" ]; then
  exit "$(cat "$out/exit")"
fi
exit 0
`

func TestToolVersionMatchesPreparedContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		installed string
		prepared  string
		want      bool
	}{
		{name: "short descriptor version", installed: "agent-sdk-testify 0.1.0-dev", prepared: "0.1.0-dev", want: true},
		{name: "full descriptor version", installed: "agent-sdk-testify 0.1.0-dev", prepared: "agent-sdk-testify 0.1.0-dev", want: true},
		{name: "different suffix", installed: "agent-sdk-testify 0.1.0-dev", prepared: "2.0-dev", want: false},
		{name: "missing installed version", prepared: "0.1.0-dev", want: false},
		{name: "missing prepared version", installed: "agent-sdk-testify 0.1.0-dev", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, toolVersionMatches(test.installed, test.prepared))
		})
	}
}

type finishFixture struct {
	run     *Run
	finish  string
	cleanup string
	result  string
	receipt string
	process string
}

func TestAuthorWritesSyntheticScenario(t *testing.T) {
	synthetic, err := Author(t.TempDir(), "codex", []byte(`{
		"version": 1,
		"provenance": "synthetic",
		"source": "authored",
		"timeoutMs": 125,
		"frames": [
			{"ordinal": 91, "channel": "protocol", "direction": "c2s", "epoch": 1, "wire": {"id": 1, "method": "initialize"}},
			{"ordinal": 92, "channel": "protocol", "direction": "s2c", "epoch": 1, "delayMs": 7, "wire": {"id": 1, "result": {}}}
		]
	}`))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(synthetic.Recording, "raw", "raw.jsonl"))
	require.NoError(t, err)
	var document struct {
		Version    int               `json:"version"`
		Provenance string            `json:"provenance"`
		Source     string            `json:"source"`
		TimeoutMS  int               `json:"timeoutMs"`
		Frames     []json.RawMessage `json:"frames"`
	}
	require.NoError(t, json.Unmarshal(raw, &document))
	require.Equal(t, 1, document.Version)
	require.Equal(t, "synthetic", document.Provenance)
	require.Equal(t, "authored", document.Source)
	require.Equal(t, 125, document.TimeoutMS)
	require.Len(t, document.Frames, 2)
	require.Contains(t, string(document.Frames[1]), `"delayMs":7`)
	require.Contains(t, string(document.Frames[1]), `"ordinal":2`)
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	data, err := os.ReadFile(filepath.Join(synthetic.Recording, "manifest.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(raw)), manifest.Files["raw.jsonl"])
}

func TestFinishPassesStrictArtifacts(t *testing.T) {
	f := newFinishFixture(t)
	require.NoError(t, f.run.Finish(context.Background()))
	_, err := os.Stat(f.run.ResourceRoot)
	require.True(t, os.IsNotExist(err))
}

func TestFinishRejectsStrictArtifactFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *finishFixture)
		want   string
	}{
		{
			name: "run schema",
			mutate: func(t *testing.T, f *finishFixture) {
				f.run.SchemaVersion = 2
				f.persist(t)
			},
			want: "schemaVersion 2 is unsupported",
		},
		{
			name: "finish schema",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.finish, func(value map[string]any) { value["schemaVersion"] = 2 })
				editJSON(t, f.result, func(value map[string]any) { value["schemaVersion"] = 2 })
			},
			want: "finish identity mismatch",
		},
		{
			name: "cleanup schema",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.cleanup, func(value map[string]any) { value["schemaVersion"] = 2 })
			},
			want: "cleanup.json does not prove",
		},
		{
			name: "receipt schema",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["schemaVersion"] = 2 })
			},
			want: "receipt does not prove",
		},
		{
			name: "process schema",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.process, func(value map[string]any) { value["schemaVersion"] = 2 })
			},
			want: "process receipt does not prove",
		},
		{
			name: "receipt plan hash",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["planSHA256"] = "different" })
			},
			want: "receipt does not prove",
		},
		{
			name: "receipt recording hash",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["recordingSHA256"] = "different" })
			},
			want: "receipt does not prove",
		},
		{
			name: "receipt provider",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["provider"] = "claude" })
			},
			want: "receipt does not prove",
		},
		{
			name: "incomplete consumption",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["fullConsumption"] = false })
			},
			want: "receipt does not prove",
		},
		{
			name: "missing client EOF",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["clientEOF"] = false })
			},
			want: "receipt does not prove",
		},
		{
			name: "native launched",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.receipt, func(value map[string]any) { value["nativeLaunched"] = true })
			},
			want: "receipt does not prove",
		},
		{
			name: "process tool version",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.process, func(value map[string]any) { value["toolVersion"] = "other" })
			},
			want: "process receipt does not prove",
		},
		{
			name: "process tool hash",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.process, func(value map[string]any) { value["toolSHA256"] = "different" })
			},
			want: "process receipt does not prove",
		},
		{
			name: "process tool path",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.process, func(value map[string]any) { value["tool"] = f.run.FinishCommand.Command + ".other" })
			},
			want: "different tool",
		},
		{
			name: "finish result mismatch",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.finish, func(value map[string]any) {
					value["status"] = "failed"
					value["error"] = "different"
				})
			},
			want: "differs from command result",
		},
		{
			name: "finish missing connection",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.finish, func(value map[string]any) { value["connections"] = []any{} })
				editJSON(t, f.result, func(value map[string]any) { value["connections"] = []any{} })
			},
			want: "does not report every declared connection",
		},
		{
			name: "finish history",
			mutate: func(t *testing.T, f *finishFixture) {
				for _, path := range []string{f.finish, f.result} {
					editJSON(t, path, func(value map[string]any) {
						value["history"] = map[string]any{"status": "failed", "error": "drift"}
					})
				}
			},
			want: "does not prove history completion",
		},
		{
			name: "cleanup incomplete",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.cleanup, func(value map[string]any) { value["cleanupComplete"] = false })
			},
			want: "does not prove complete cleanup",
		},
		{
			name: "cleanup history",
			mutate: func(t *testing.T, f *finishFixture) {
				editJSON(t, f.cleanup, func(value map[string]any) {
					value["history"] = map[string]any{"status": "failed", "error": "drift"}
				})
			},
			want: "does not prove history completion",
		},
		{
			name: "missing finish report",
			mutate: func(t *testing.T, f *finishFixture) {
				write(t, filepath.Join(filepath.Dir(f.finish), "skip-finish"), nil)
			},
			want: "read mock finish",
		},
		{
			name: "missing cleanup report",
			mutate: func(t *testing.T, f *finishFixture) {
				write(t, filepath.Join(filepath.Dir(f.finish), "skip-cleanup"), nil)
			},
			want: "read mock cleanup",
		},
		{
			name: "missing receipt",
			mutate: func(t *testing.T, f *finishFixture) {
				require.NoError(t, os.Remove(f.receipt))
			},
			want: "read main receipt",
		},
		{
			name: "missing process receipt",
			mutate: func(t *testing.T, f *finishFixture) {
				require.NoError(t, os.Remove(f.process))
			},
			want: "read main process receipt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFinishFixture(t)
			test.mutate(t, f)
			err := f.run.Finish(context.Background())
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestFinishHonorsIndependentDeadline(t *testing.T) {
	f := newFinishFixture(t)
	write(t, filepath.Join(f.run.Output, "sleep"), []byte("5\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := f.run.Finish(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 2*time.Second)
}

func newFinishFixture(t *testing.T) *finishFixture {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "agent-sdk-testify")
	write(t, tool, []byte(fakeTool))
	require.NoError(t, os.Chmod(tool, 0o700))
	t.Setenv("AGENT_SDK_TESTIFY_BIN", tool)
	toolBytes, err := os.ReadFile(tool)
	require.NoError(t, err)
	toolHash := fmt.Sprintf("%x", sha256.Sum256(toolBytes))

	output := t.TempDir()
	contract := filepath.Join(output, "case.json")
	materialization := filepath.Join(output, "materialization.json")
	write(t, contract, []byte(`{"caseId":"case"}`))
	write(t, materialization, []byte(`{"status":"prepared"}`))
	contractHash, err := fileSHA256(contract)
	require.NoError(t, err)
	materializationHash, err := fileSHA256(materialization)
	require.NoError(t, err)

	resourceRoot := filepath.Join(t.TempDir(), "agent-sdk-testify-mock-test")
	require.NoError(t, os.Mkdir(resourceRoot, 0o700))
	launcher := filepath.Join(resourceRoot, "launcher")
	write(t, launcher, []byte("#!/bin/sh\nexit 0\n"))
	require.NoError(t, os.Chmod(launcher, 0o700))
	recording := t.TempDir()
	descriptor := filepath.Join(output, "run.json")
	receipt := filepath.Join(output, "main.receipt.json")
	process := filepath.Join(output, "main.process.json")
	run := &Run{
		SchemaVersion:   1,
		RunID:           "run-1",
		Tool:            Tool{Command: tool},
		ToolVersion:     "test-version",
		ToolSHA256:      toolHash,
		Recording:       recording,
		Provider:        "codex",
		CaseID:          "case",
		ResourceRoot:    resourceRoot,
		Workspace:       filepath.Join(output, "workspace"),
		ProtocolCWD:     filepath.Join(output, "project"),
		Env:             map[string]string{"HOME": filepath.Join(output, "home")},
		Contract:        contract,
		ContractSHA:     contractHash,
		Materialized:    materialization,
		MaterializedSHA: materializationHash,
		Connections: []Connection{{
			ID:              "main",
			Command:         tool,
			Args:            []string{"mock", "--run", descriptor, "--connection", "main"},
			Launcher:        launcher,
			Recording:       recording,
			PlanSHA256:      contractHash,
			ManifestSHA256:  "manifest-hash",
			RecordingSHA256: "recording-hash",
			Receipt:         receipt,
			ProcessReceipt:  process,
		}},
		FinishCommand: Command{Command: tool, Args: []string{"mock", "--finish", descriptor}},
		Descriptor:    descriptor,
		Output:        output,
	}
	writeJSON(t, receipt, Receipt{
		SchemaVersion:   1,
		RunID:           run.RunID,
		ConnectionID:    "main",
		Status:          "passed",
		Provider:        run.Provider,
		Recording:       recording,
		PlanSHA256:      contractHash,
		RecordingSHA256: "recording-hash",
		FullConsumption: true,
		ClientEOF:       true,
		CleanupComplete: true,
		NativeLaunched:  false,
		ExitCode:        0,
	})
	writeJSON(t, process, map[string]any{
		"schemaVersion": 1,
		"runId":         run.RunID,
		"connectionId":  "main",
		"status":        "passed",
		"exitCode":      0,
		"tool":          tool,
		"toolVersion":   run.ToolVersion,
		"toolSHA256":    run.ToolSHA256,
	})

	finish := map[string]any{
		"schemaVersion":   1,
		"runId":           run.RunID,
		"status":          "passed",
		"cleanupComplete": true,
		"connections": []any{
			map[string]any{"id": "main", "status": "passed"},
		},
		"history": map[string]any{"status": "passed"},
	}
	cleanup := map[string]any{
		"schemaVersion":   1,
		"runId":           run.RunID,
		"cleanupComplete": true,
		"history":         map[string]any{"status": "passed"},
	}
	finishPath := filepath.Join(output, "finish.expected")
	resultPath := filepath.Join(output, "result.expected")
	cleanupPath := filepath.Join(output, "cleanup.expected")
	writeJSON(t, finishPath, finish)
	writeJSON(t, resultPath, finish)
	writeJSON(t, cleanupPath, cleanup)
	write(t, filepath.Join(output, "remove-root"), []byte(resourceRoot))

	f := &finishFixture{
		run: run, finish: finishPath, cleanup: cleanupPath, result: resultPath,
		receipt: receipt, process: process,
	}
	f.persist(t)
	return f
}

func (f *finishFixture) persist(t *testing.T) {
	t.Helper()
	writeJSON(t, f.run.Descriptor, f.run)
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	write(t, path, data)
}

func editJSON(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))
	edit(value)
	writeJSON(t, path, value)
}
