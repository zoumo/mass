package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	go func() {
		for {
			if _, err := stream.Next(t.Context()); err != nil {
				return
			}
		}
	}()
	_, err = driver.Start(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = driver.CloseSession(context.Background(), "native-thread")
		_ = driver.Stop(context.Background())
	})
	address := runtime.Address{Namespace: "one", SessionName: "prompt"}
	spec := runtime.SessionSpec{Cwd: cwd, SystemPrompt: "Append these instructions.\n保留默认提示词。"}
	for i, invalid := range []struct{ prompt, reason string }{{string([]byte{0xff}), "additional_system_prompt_invalid_utf8"}, {"bad\x00prompt", "additional_system_prompt_contains_nul"}} {
		bad := spec
		bad.SystemPrompt = invalid.prompt
		for _, resume := range []bool{false, true} {
			if resume {
				_, err = driver.ResumeSession(t.Context(), "native-thread", address, bad)
			} else {
				_, err = driver.OpenSession(t.Context(), address, bad)
			}
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, "invalid_argument", public.Code)
			require.Equal(t, invalid.reason, public.Reason)
			require.ErrorIs(t, err, runtime.ErrResourcesReleased)
		}
		err = driver.LoadSession(t.Context(), fmt.Sprintf("invalid-%d", i), address, bad, "invalid-load")
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, invalid.reason, public.Reason)
	}
	extra := spec
	extra.AdditionalDirs = []string{"/extra"}
	_, err = driver.OpenSession(t.Context(), address, extra)
	var unsupported *runtime.OperationError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	_, err = driver.ResumeSession(t.Context(), "native-thread", address, extra)
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	err = driver.LoadSession(t.Context(), "invalid-dirs", address, extra, "invalid-dirs-load")
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "additional_dirs_unsupported", unsupported.Reason)
	require.NoFileExists(t, trace, "invalid prompt must precede native open/resume")
	opened, err := driver.OpenSession(t.Context(), address, spec)
	require.NoError(t, err)
	require.Equal(t, spec.SystemPrompt, waitCodexPeerTrace(t, trace, "thread/start")["params"].(map[string]any)["developerInstructions"])
	require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
	for range 2 {
		_, err = driver.ResumeSession(t.Context(), opened.SessionID, address, spec)
		require.NoError(t, err)
		require.Equal(t, spec.SystemPrompt, waitCodexPeerTrace(t, trace, "thread/resume")["params"].(map[string]any)["developerInstructions"])
		require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
	}
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
}
