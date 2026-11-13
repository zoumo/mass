package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestAdditionalSystemPromptOpenResumeAndLoad(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	trace := filepath.Join(t.TempDir(), "peer.jsonl")
	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": executable, "args": []string{"-test.run=^TestCodexDriverTestPeer$"}, "envPolicy": "replace",
		"env": map[string]any{"MASS_CODEX_DRIVER_PEER": "1", "MASS_CODEX_DRIVER_TRACE": trace, "MASS_CODEX_DRIVER_CWD": cwd, "CODEX_HOME": t.TempDir(), "HOME": t.TempDir()},
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
			assert.NoError(t, driver.CloseSession(cleanup, openedID), "cleanup must release the native session")
			cancel()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		assert.NoError(t, driver.Stop(cleanup), "cleanup must release the provider even if session close failed")
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
	_, err = driver.Start(t.Context())
	require.NoError(t, err)
	address := runtime.Address{Namespace: "one", SessionName: "prompt"}
	spec := runtime.SessionSpec{Cwd: cwd, SystemPrompt: "Append these instructions.\n保留默认提示词。"}
	for i, invalid := range []struct{ prompt, reason string }{{string([]byte{0xff}), "additional_system_prompt_invalid_utf8"}, {"bad\x00prompt", "additional_system_prompt_contains_nul"}} {
		bad := spec
		bad.SystemPrompt = invalid.prompt
		for _, resume := range []bool{false, true} {
			var rejected runtime.DriverSession
			if resume {
				rejected, err = driver.ResumeSession(t.Context(), "native-thread", address, bad)
			} else {
				rejected, err = driver.OpenSession(t.Context(), address, bad)
			}
			openedID = rejected.SessionID
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, "invalid_argument", public.Code)
			require.Equal(t, invalid.reason, public.Reason)
			require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		}
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
		err = driver.LoadSession(t.Context(), invalidID, address, bad, "invalid-load")
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, invalid.reason, public.Reason)
	}
	extra := spec
	extra.AdditionalDirs = []string{"/extra"}
	rejected, err := driver.OpenSession(t.Context(), address, extra)
	openedID = rejected.SessionID
	var unsupported *runtime.OperationError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	rejected, err = driver.ResumeSession(t.Context(), "native-thread", address, extra)
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
	err = driver.LoadSession(t.Context(), "invalid-dirs", address, extra, "invalid-dirs-load")
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	require.NoFileExists(t, trace, "invalid prompt must precede native open/resume")
	opened, err := driver.OpenSession(t.Context(), address, spec)
	openedID = opened.SessionID
	require.NoError(t, err)
	require.Equal(t, spec.SystemPrompt, waitCodexPeerTrace(t, trace, "thread/start")["params"].(map[string]any)["developerInstructions"])
	require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
	openedID = ""
	for range 2 {
		resumed, resumeErr := driver.ResumeSession(t.Context(), opened.SessionID, address, spec)
		openedID = resumed.SessionID
		require.NoError(t, resumeErr)
		require.Equal(t, spec.SystemPrompt, waitCodexPeerTrace(t, trace, "thread/resume")["params"].(map[string]any)["developerInstructions"])
		require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
		openedID = ""
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, driver.CancelHistoryLoad(cleanup, opened.SessionID, "prompt-load"), "cleanup must release the native history load")
	})
	openedID = opened.SessionID
	require.NoError(t, driver.LoadSession(t.Context(), opened.SessionID, address, spec, "prompt-load"))
	require.Equal(t, spec.SystemPrompt, waitCodexPeerTrace(t, trace, "thread/resume")["params"].(map[string]any)["developerInstructions"])
	data, err := os.ReadFile(trace)
	require.NoError(t, err)
	resumes := 0
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		var record struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(line, &record) != nil || record.Method != "thread/resume" {
			continue
		}
		resumes++
		require.Equal(t, spec.SystemPrompt, record.Params["developerInstructions"])
	}
	require.Equal(t, 3, resumes)
	closeErr := driver.CloseSession(t.Context(), opened.SessionID)
	stopErr := driver.Stop(t.Context())
	require.NoError(t, closeErr, "loaded session must be released before the test succeeds")
	openedID = ""
	require.NoError(t, stopErr, "provider release must be confirmed before the test succeeds")
}
