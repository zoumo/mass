package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	prepareClaudeSavedSession(t, config, cwd, id)
	driver, err := (claude.Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": bin, "envPolicy": "replace", "env": map[string]any{"CLAUDE_CONFIG_DIR": config, "LOAD_ID": id, "LOAD_EVIDENCE": evidence},
	})
	require.NoError(t, err)
	stream, err := driver.Events()
	require.NoError(t, err)
	readCtx, stopRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	var openedID string
	t.Cleanup(func() {
		if openedID != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if closeErr := driver.CloseSession(cleanup, openedID); closeErr != nil {
				t.Errorf("close owned Claude session: %v", closeErr)
			}
			cancel()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if stopErr := driver.Stop(cleanup); stopErr != nil {
			t.Errorf("stop owned Claude driver: %v", stopErr)
		}
		cancel()
		stopRead()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("prompt event reader cleanup did not finish")
		}
	})
	go func() {
		defer close(readDone)
		for {
			if _, err := stream.Next(readCtx); err != nil {
				return
			}
		}
	}()
	_, err = driver.Start(ctx(t))
	require.NoError(t, err)
	address := runtime.Address{Namespace: "one", SessionName: "prompt"}
	spec := runtime.SessionSpec{Cwd: cwd, SystemPrompt: "Append instructions.\n保留默认提示词。"}
	for i, invalid := range []struct{ prompt, reason string }{{string([]byte{0xff}), "additional_system_prompt_invalid_utf8"}, {"bad\x00prompt", "additional_system_prompt_contains_nul"}} {
		bad := spec
		bad.SystemPrompt = invalid.prompt
		rejected, err := driver.OpenSession(ctx(t), address, bad)
		openedID = rejected.SessionID
		assertPromptValidation(t, err, invalid.reason)
		require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		rejected, err = driver.ResumeSession(ctx(t), id, address, bad)
		openedID = rejected.SessionID
		assertPromptValidation(t, err, invalid.reason)
		require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		invalidID := fmt.Sprintf("invalid-%d", i)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Preflight rejection can leave no load; accept only that exact absence.
			if cancelErr := driver.CancelHistoryLoad(cleanup, invalidID, "invalid-load"); cancelErr != nil &&
				(!errors.Is(cancelErr, runtime.ErrFailedPrecondition) || cancelErr.Error() != runtime.FailedPrecondition("original native load unavailable").Error()) {
				t.Errorf("release attempted rejected history load: %v", cancelErr)
			}
		})
		err = driver.LoadSession(ctx(t), invalidID, address, bad, "invalid-load")
		assertPromptValidation(t, err, invalid.reason)
	}
	extra := spec
	extra.AdditionalDirs = []string{"/extra"}
	rejected, err := driver.OpenSession(ctx(t), address, extra)
	openedID = rejected.SessionID
	var unsupported *runtime.OperationError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	rejected, err = driver.ResumeSession(ctx(t), id, address, extra)
	openedID = rejected.SessionID
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Preflight rejection can leave no load; accept only that exact absence.
		if cancelErr := driver.CancelHistoryLoad(cleanup, "invalid-dirs", "invalid-dirs-load"); cancelErr != nil &&
			(!errors.Is(cancelErr, runtime.ErrFailedPrecondition) || cancelErr.Error() != runtime.FailedPrecondition("original native load unavailable").Error()) {
			t.Errorf("release attempted rejected history load: %v", cancelErr)
		}
	})
	err = driver.LoadSession(ctx(t), "invalid-dirs", address, extra, "invalid-dirs-load")
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	require.NoFileExists(t, evidence, "invalid prompt cannot launch the native CLI")
	opened, err := driver.OpenSession(ctx(t), address, spec)
	openedID = opened.SessionID
	require.NoError(t, err)
	require.Equal(t, id, opened.SessionID)
	require.NoError(t, driver.CloseSession(ctx(t), openedID))
	openedID = ""
	for range 2 {
		opened, err = driver.ResumeSession(ctx(t), id, address, spec)
		openedID = opened.SessionID
		require.NoError(t, err)
		require.NoError(t, driver.CloseSession(ctx(t), openedID))
		openedID = ""
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if loadErr := driver.CancelHistoryLoad(cleanup, id, "prompt-load"); loadErr != nil {
			t.Errorf("release owned Claude history load: %v", loadErr)
		}
	})
	openedID = id
	require.NoError(t, driver.LoadSession(ctx(t), id, address, spec, "prompt-load"))
	require.NoError(t, driver.CloseSession(ctx(t), openedID))
	openedID = ""
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
