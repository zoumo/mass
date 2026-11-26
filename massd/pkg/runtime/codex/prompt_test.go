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
	"github.com/zoumo/agent-sdk-go/agent"
	sdk "github.com/zoumo/agent-sdk-go/client/codex"

	"github.com/zoumo/mass/internal/testpeer"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/runtime/agentrelay"
)

type promptSDKSession struct {
	configSDKSession
	promptReceipt agent.InputReceipt
	promptErr     error
}

func (s *promptSDKSession) StartOrSteer(context.Context, agent.Input) (agent.InputReceipt, error) {
	return s.promptReceipt, s.promptErr
}

func TestPromptFailurePreservesDispatchEvidence(t *testing.T) {
	cases := []struct {
		name     string
		code     agent.ErrorCode
		dispatch sdk.DispatchState
		cause    error
		sent     *bool
		wantSent *bool
	}{
		{
			name: "outbound frame rejected before write", code: agent.ErrorUnavailable,
			dispatch: sdk.NotSent, cause: sdk.ErrFrameTooLarge, wantSent: runtime.Bool(false),
		},
		{
			name: "uncertain dispatch", code: agent.ErrorOutcomeUnknown,
			dispatch: sdk.OutcomeUnknown, cause: context.DeadlineExceeded,
		},
		{
			name: "observed write with uncertain outcome", code: agent.ErrorOutcomeUnknown,
			dispatch: sdk.OutcomeUnknown, cause: context.DeadlineExceeded, sent: runtime.Bool(true), wantSent: runtime.Bool(true),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdkErr := &agent.Error{
				Code: tc.code, Reason: "start_or_steer_failed", Message: "Prompt failed",
				Cause: &sdk.CallError{Method: "turn/start", Dispatch: tc.dispatch, Cause: tc.cause},
			}
			native := &promptSDKSession{
				promptReceipt: agent.InputReceipt{Acceptance: agent.Acceptance{Sent: tc.sent}},
				promptErr:     sdkErr,
			}
			relay, err := agentrelay.Start(t.Context(), agentrelay.Config{
				Session: native,
				Address: runtime.Address{Namespace: "one", SessionName: "prompt"},
				Sink:    func(context.Context, events.SessionEvent) error { return nil },
			})
			require.NoError(t, err)
			t.Cleanup(func() { relay.Close(); <-relay.Done() })
			driver := &Driver{sessions: map[string]*session{"native": {native: native, relay: relay}}}
			receipt, err := driver.SendPrompt(t.Context(), "native", runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}})
			var public *runtime.OperationError
			require.ErrorAs(t, err, &public)
			require.Equal(t, string(tc.code), public.Code)
			require.ErrorIs(t, err, tc.cause)
			require.Equal(t, runtime.InputReceipt{Acceptance: runtime.Acceptance{Sent: tc.wantSent}}, receipt)
		})
	}
}

func TestPromptReturnsAssociationForIdentifiedTurn(t *testing.T) {
	for _, tc := range []struct {
		result agent.InputResult
		work   string
	}{
		{result: agent.InputAppliedNewWork, work: "turn-id"},
		{result: agent.InputAppliedCurrentWork, work: "turn-id"},
		{result: agent.InputAppliedBestEffort},
	} {
		t.Run(string(tc.result), func(t *testing.T) {
			native := &promptSDKSession{promptReceipt: agent.InputReceipt{
				Submission: "turn-id",
				Result:     tc.result,
				Acceptance: agent.Acceptance{Sent: runtime.Bool(true)},
			}}
			relay, err := agentrelay.Start(t.Context(), agentrelay.Config{
				Session: native,
				Address: runtime.Address{Namespace: "one", SessionName: "prompt"},
				Sink:    func(context.Context, events.SessionEvent) error { return nil },
			})
			require.NoError(t, err)
			t.Cleanup(func() { relay.Close(); <-relay.Done() })
			driver := &Driver{sessions: map[string]*session{"native": {native: native, relay: relay}}}
			receipt, err := driver.SendPrompt(t.Context(), "native", runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}})
			require.NoError(t, err)
			require.Equal(t, tc.work, receipt.Work)
			wire, err := json.Marshal(receipt)
			require.NoError(t, err)
			expected, err := json.Marshal(agentrelay.MapInputReceipt(native.promptReceipt))
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(wire))
		})
	}
}

func TestCodexTerminalWorkSource(t *testing.T) {
	requiresAction := events.RequiresAction
	running := events.Running
	for _, tc := range []struct {
		name     string
		method   string
		work     string
		thread   string
		payload  events.Payload
		terminal bool
	}{
		{"completed turn with pending action", "turn/completed", "turn-1", "", events.StateUpdate{State: &requiresAction}, true},
		{"completed child turn with shared ID", "turn/completed", "turn-1", "child-thread", events.StateUpdate{State: &requiresAction}, false},
		{"stale completion diagnostic", "turn/completed", "turn-1", "", events.SessionError{Reason: "stale_native_turn", Message: "stale"}, false},
		{"started turn", "turn/started", "turn-1", "", events.StateUpdate{State: &running}, false},
		{"untagged state", "thread/status/changed", "", "", events.StateUpdate{State: &running}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := events.NewSource()
			meta := map[string]any{"method": tc.method, "turnId": tc.work}
			if tc.thread != "" {
				meta["sourceThreadId"] = tc.thread
			}
			event, err := events.NewEvent(events.EventBase{
				EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "prompt", SessionID: "native",
				Time: time.Now(), Meta: map[string]any{"codex": meta},
			}, tc.payload, original)
			require.NoError(t, err)
			source := codexEventSource(event)
			require.True(t, source.SameRun(original))
			require.Equal(t, tc.work, source.Work())
			require.Equal(t, tc.terminal, source.WorkTerminal())
		})
	}
}

func TestAdditionalSystemPromptOpenResumeAndLoad(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	trace := filepath.Join(t.TempDir(), "peer.jsonl")
	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": executable, "args": []string{"-test.run=^TestCodexDriverTestPeer$"}, "envPolicy": "replace",
		"env": testpeer.RuntimeEnv(map[string]string{"MASS_CODEX_DRIVER_PEER": "1", "MASS_CODEX_DRIVER_TRACE": trace, "MASS_CODEX_DRIVER_CWD": cwd, "CODEX_HOME": t.TempDir(), "HOME": t.TempDir()}),
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
