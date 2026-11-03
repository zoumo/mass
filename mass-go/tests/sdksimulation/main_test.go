package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/internal/testpeer"
)

var (
	build                       sync.Once
	driver, testify, buildError string
	binaryHashes                map[string]string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if driver != "" {
		_ = os.RemoveAll(filepath.Dir(driver))
	}
	os.Exit(code)
}

func binaries(t *testing.T) (string, string) {
	t.Helper()
	build.Do(func() {
		dir, err := os.MkdirTemp("", "mass-sdk-driver-build-") //nolint:usetesting // Shared binaries outlive the first test; TestMain removes them.
		if err != nil {
			buildError = err.Error()
			return
		}
		driver = filepath.Join(dir, "driver")
		testify, err = testpeer.Executable()
		if err != nil {
			buildError = err.Error()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		args := []string{"build", "-o", driver, "."}
		if os.Getenv("MASS_SDK_RACE") == "1" {
			args = []string{"build", "-race", "-o", driver, "."}
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildError = string(out) + err.Error()
			return
		}
		binaryHashes = map[string]string{}
		for _, path := range []string{driver, testify} {
			data, err := os.ReadFile(path)
			if err != nil {
				buildError = err.Error()
				return
			}
			binaryHashes[filepath.Base(path)] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	})
	require.Empty(t, buildError)
	return driver, testify
}

func put(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func get(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

func authoredRun(t *testing.T, provider, mode string) (string, map[string]any) {
	t.Helper()
	driver, _ := binaries(t)
	root := t.TempDir()
	if evidence := os.Getenv("MASS_SDK_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, provider+"-"+mode+"-") //nolint:usetesting // Explicit evidence export must survive test cleanup.
		require.NoError(t, err)
	}
	for _, name := range []string{"materials", "project", "workspace", "output"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o700))
	}
	put(t, filepath.Join(root, "binary-hashes.json"), binaryHashes)
	peer, err := filepath.Abs("testdata/claude-peer.py")
	require.NoError(t, err)
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	command := python
	project := filepath.Join(root, "project")
	workspace := filepath.Join(root, "workspace")
	protocolCWD := project
	args := []string{"-I", peer, mode, workspace, filepath.Join(root, "synthetic-peer.json")}
	var prepared *testpeer.Run
	var requestEnv map[string]string
	if provider == "codex" {
		data, err := os.ReadFile("testdata/codex-two-rounds.json")
		require.NoError(t, err)
		data = []byte(strings.ReplaceAll(string(data), "/synthetic", filepath.Join(root, "project")))
		switch mode {
		case "wrong":
			data = bytes.Replace(data, []byte(`"text": "Done"`), []byte(`"text": "Wrong"`), 1)
		case "empty":
			data = bytes.Replace(data, []byte(`"text": "Done"`), []byte(`"text": ""`), 1)
		case "stale":
			data = bytes.Replace(data, []byte(`"text": "Done"`), []byte(`"text": "Ready"`), 1)
		case "wrong-work":
			data = bytes.Replace(data, []byte(`"turnId": "turn-2"`), []byte(`"turnId": "turn-1"`), 1)
		case "prompt-mismatch":
			data = bytes.Replace(data, []byte(`Reply with the word Ready.`), []byte(`Wrong prompt.`), 1)
		}
		prepared, err = testpeer.Prepare(context.Background(), root, "codex", data)
		require.NoError(t, err)
		main, err := prepared.Connection("main")
		require.NoError(t, err)
		command = main.Launcher
		args = nil
		workspace = prepared.Workspace
		protocolCWD = prepared.ProtocolCWD
		requestEnv = prepared.Env
		t.Cleanup(func() {
			finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = prepared.Finish(finishCtx)
		})
	} else {
		protocolCWD = workspace
	}
	put(t, filepath.Join(root, "materials", "case.json"), map[string]any{
		"caseId": "SDK-CASE-001", "provider": provider, "driverProfile": "continuation",
		"project": protocolCWD, "prompts": []string{"Reply with the word Ready.", "Reply with the word Done."},
	})
	put(t, filepath.Join(root, "materials", "runtime.json"), map[string]any{
		"workspace": workspace, "protocolCwd": protocolCWD,
	})
	put(t, filepath.Join(root, "request.json"), map[string]any{
		"case": "SDK-CASE-001", "materials": filepath.Join(root, "materials"),
		"workspace": workspace, "output": filepath.Join(root, "output"),
		"env":  requestEnv,
		"peer": map[string]any{"command": command, "args": args},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	timeout := "5s"
	if mode == "timeout" {
		timeout = "2s"
	}
	cmd := exec.CommandContext(ctx, driver, "--request", filepath.Join(root, "request.json"), "--timeout", timeout)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	for key, value := range requestEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, os.WriteFile(filepath.Join(root, "driver.log"), out, 0o600))
	require.NoError(t, err, "%s", out)
	if prepared != nil {
		main, connectionErr := prepared.Connection("main")
		require.NoError(t, connectionErr)
		receipt, readErr := os.ReadFile(main.Receipt)
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(root, "strict-peer.json"), receipt, 0o600))
		process, readErr := os.ReadFile(main.ProcessReceipt)
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(root, "strict-process.json"), process, 0o600))
		finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		finishErr := prepared.Finish(finishCtx)
		cancel()
		if mode == "prompt-mismatch" {
			require.Error(t, finishErr)
		} else {
			require.NoError(t, finishErr)
		}
	}
	t.Log("evidence:", root)
	cleanup := get(t, filepath.Join(root, "output", "cleanup.json"))
	require.Equal(t, true, cleanup["shutdownAttempted"])
	require.Equal(t, true, cleanup["storesClosed"])
	return root, get(t, filepath.Join(root, "output", "result.json"))
}

func TestClaudeRejectsWrongReply(t *testing.T) {
	root, result := authoredRun(t, "claude", "wrong")
	require.Equal(t, "failed", result["status"])
	require.Contains(t, result["message"], "reply")
	require.Equal(t, true, get(t, filepath.Join(root, "output", "cleanup.json"))["released"])
}

func TestClaudeTwoRounds(t *testing.T) {
	root, result := authoredRun(t, "claude", "success")
	require.Equal(t, "passed", result["status"], result)
	assertTwoRounds(t, root, "claude")
	require.Equal(t, true, get(t, filepath.Join(root, "output", "cleanup.json"))["released"])
	peer := get(t, filepath.Join(root, "synthetic-peer.json"))
	require.Equal(t, true, peer["complete"])
	require.Equal(t, true, peer["clientEOF"])
	require.Equal(t, "authored-stateful-peer", peer["evidenceClass"])
	require.Equal(t, false, peer["nativeLaunched"])
}

func TestClaudeTimeoutWritesFailureAndCleanup(t *testing.T) {
	root, result := authoredRun(t, "claude", "timeout")
	require.Equal(t, "failed", result["status"])
	require.Contains(t, result["message"], "deadline exceeded")
	require.Equal(t, true, get(t, filepath.Join(root, "output", "cleanup.json"))["released"])
}

func TestCodexTwoRoundsStrictAuthored(t *testing.T) {
	root, result := authoredRun(t, "codex", "success")
	require.Equal(t, "passed", result["status"], result)
	assertTwoRounds(t, root, "codex")
	require.Equal(t, true, get(t, filepath.Join(root, "output", "cleanup.json"))["released"])
	receipt := get(t, filepath.Join(root, "strict-peer.json"))
	t.Logf("strict receipt: %#v", receipt)
	require.Equal(t, true, receipt["fullConsumption"])
	require.Equal(t, true, receipt["clientEOF"])
	require.Equal(t, false, receipt["nativeLaunched"])
	require.Equal(t, "codex", receipt["provider"])
	descriptors, err := filepath.Glob(filepath.Join(root, "prepared-*", "run.json"))
	require.NoError(t, err)
	require.Len(t, descriptors, 1)
	descriptor := get(t, descriptors[0])
	raw, err := os.ReadFile(filepath.Join(descriptor["recording"].(string), "raw", "raw.jsonl"))
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(raw)), receipt["recordingSHA256"])
}

func assertTwoRounds(t *testing.T, root, provider string) {
	t.Helper()
	created := get(t, filepath.Join(root, "output", "created.json"))
	require.Equal(t, provider+"-session", created["status"].(map[string]any)["sessionId"])
	first := created["initialInputReceipt"].(map[string]any)
	second := get(t, filepath.Join(root, "output", "second-receipt.json"))
	require.NotEqual(t, first["submission"], second["submission"])
	rounds := get(t, filepath.Join(root, "output", "rounds.json"))["rounds"].([]any)
	require.Len(t, rounds, 2)
	for index, expected := range []string{"Ready", "Done"} {
		round := rounds[index].(map[string]any)
		require.Equal(t, expected, round["reply"])
		require.Equal(t, true, round["ownershipVerified"])
		require.Equal(t, "idle", round["state"])
		require.Equal(t, provider+"-session", round["sessionId"])
	}
	page := get(t, filepath.Join(root, "output", "mass-history.json"))
	var replies []string
	for _, raw := range page["events"].([]any) {
		event := raw.(map[string]any)
		require.Equal(t, "simulation", event["namespace"])
		require.Equal(t, "continuation", event["sessionName"])
		require.Equal(t, provider+"-session", event["sessionId"])
		if event["type"] == "agent.message" {
			content := event["payload"].(map[string]any)["content"].([]any)
			require.Len(t, content, 1)
			replies = append(replies, content[0].(map[string]any)["text"].(string))
		}
	}
	require.Equal(t, []string{"Ready", "Done"}, replies)
}

func TestClaudeRejectsInvalidObservations(t *testing.T) {
	for _, mode := range []string{"empty", "extra", "stale", "wrong-link", "wrong-identity", "peer-error"} {
		t.Run(mode, func(t *testing.T) {
			root, result := authoredRun(t, "claude", mode)
			require.Equal(t, "failed", result["status"], result)
			require.NotEmpty(t, result["message"])
			cleanup := get(t, filepath.Join(root, "output", "cleanup.json"))
			if mode == "peer-error" {
				// Post-open EOF retains SDK terminal diagnostics. Do not relabel
				// that logical cleanup failure using OS-only process evidence.
				require.Equal(t, false, cleanup["released"])
				require.NotEmpty(t, cleanup["error"])
			} else {
				require.Equal(t, true, cleanup["released"])
			}
		})
	}
}

func TestCodexRejectsInvalidObservations(t *testing.T) {
	for _, mode := range []string{"wrong", "empty", "stale", "wrong-work", "prompt-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			root, result := authoredRun(t, "codex", mode)
			require.Equal(t, "failed", result["status"], result)
			require.NotEmpty(t, result["message"])
			cleanup := get(t, filepath.Join(root, "output", "cleanup.json"))
			if mode == "prompt-mismatch" {
				require.Equal(t, false, cleanup["released"])
				require.NotEmpty(t, cleanup["error"])
			} else {
				require.Equal(t, true, cleanup["released"])
			}
		})
	}
}
