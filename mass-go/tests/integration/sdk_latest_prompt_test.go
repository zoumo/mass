package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
)

func TestLatestSDKClaudeAdditionalPromptOpenResumeAndLoad(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude-prompt-peer")
	out, err := exec.CommandContext(ctx(t), "go", "build", "-o", bin, "./testdata/claude-load-peer").CombinedOutput()
	require.NoError(t, err, string(out))
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	config := t.TempDir()
	evidence := filepath.Join(t.TempDir(), "argv.jsonl")
	id := "33333333-3333-4333-8333-333333333333"
	dir := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	f, err := os.Create(filepath.Join(dir, id+".jsonl"))
	require.NoError(t, err)
	for _, row := range ticket07Rows() {
		row["sessionId"] = id
		row["cwd"] = cwd
		require.NoError(t, json.NewEncoder(f).Encode(row))
	}
	require.NoError(t, f.Close())
	driver, err := (claude.Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": bin, "envPolicy": "replace", "env": map[string]any{"CLAUDE_CONFIG_DIR": config, "LOAD_ID": id, "LOAD_EVIDENCE": evidence},
	})
	require.NoError(t, err)
	stream, err := driver.Events()
	require.NoError(t, err)
	go func() {
		for {
			if _, err := stream.Next(t.Context()); err != nil {
				return
			}
		}
	}()
	_, err = driver.Start(ctx(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = driver.CloseSession(context.Background(), id); _ = driver.Stop(context.Background()) })
	address := runtime.Address{Namespace: "one", SessionName: "prompt"}
	spec := runtime.SessionSpec{Cwd: cwd, SystemPrompt: "Append instructions.\n保留默认提示词。"}
	for i, invalid := range []struct{ prompt, reason string }{{string([]byte{0xff}), "additional_system_prompt_invalid_utf8"}, {"bad\x00prompt", "additional_system_prompt_contains_nul"}} {
		bad := spec
		bad.SystemPrompt = invalid.prompt
		_, err = driver.OpenSession(ctx(t), address, bad)
		assertPromptValidation(t, err, invalid.reason)
		require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		_, err = driver.ResumeSession(ctx(t), id, address, bad)
		assertPromptValidation(t, err, invalid.reason)
		require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		err = driver.LoadSession(ctx(t), fmt.Sprintf("invalid-%d", i), address, bad, "invalid-load")
		assertPromptValidation(t, err, invalid.reason)
	}
	extra := spec
	extra.AdditionalDirs = []string{"/extra"}
	_, err = driver.OpenSession(ctx(t), address, extra)
	var unsupported *runtime.OperationError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	_, err = driver.ResumeSession(ctx(t), id, address, extra)
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	err = driver.LoadSession(ctx(t), "invalid-dirs", address, extra, "invalid-dirs-load")
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	require.NoFileExists(t, evidence, "invalid prompt cannot launch the native CLI")
	opened, err := driver.OpenSession(ctx(t), address, spec)
	require.NoError(t, err)
	require.Equal(t, id, opened.SessionID)
	require.NoError(t, driver.CloseSession(ctx(t), id))
	for range 2 {
		_, err = driver.ResumeSession(ctx(t), id, address, spec)
		require.NoError(t, err)
		require.NoError(t, driver.CloseSession(ctx(t), id))
	}
	require.NoError(t, driver.LoadSession(ctx(t), id, address, spec, "prompt-load"))
	require.NoError(t, driver.CloseSession(ctx(t), id))
	data, err := os.ReadFile(evidence)
	require.NoError(t, err)
	opens := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Event string   `json:"event"`
			Args  []string `json:"args"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record.Event != "opened" {
			continue
		}
		opens++
		require.Contains(t, record.Args, "--append-system-prompt="+spec.SystemPrompt)
		require.NotContains(t, record.Args, "--system-prompt="+spec.SystemPrompt)
		if opens > 1 {
			require.Contains(t, record.Args, "--resume="+id)
		}
	}
	require.Equal(t, 4, opens, "open, two independent resumes, and load must repeat the full prompt")
}

func assertPromptValidation(t *testing.T, err error, reason string) {
	t.Helper()
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "invalid_argument", public.Code)
	require.Equal(t, reason, public.Reason)
}
