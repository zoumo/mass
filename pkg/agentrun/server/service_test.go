package server

import (
	"context"
	"log/slog"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	acpruntime "github.com/zoumo/mass/pkg/agentrun/runtime/acp"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

func TestStatus_EventCountsOverlay(t *testing.T) {
	// 1. Set up temp dir for state storage.
	stateDir := t.TempDir()

	// 2. Write a state.json with stale EventCounts to simulate a previous state write.
	staleState := apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "test-session",
		Status:      apiruntime.StatusRunning,
		Bundle:      "/tmp/fake-bundle",
		EventCounts: map[string]int{"stale_event": 99},
	}
	require.NoError(t, spec.WriteState(stateDir, staleState))

	// 3. Create Manager pointing at the temp stateDir. bundleDir is unused
	//    by GetState(), so we pass an arbitrary path.
	mgr := acpruntime.New(apiruntime.Config{}, "/tmp/fake-bundle", stateDir, slog.Default())

	// 4. Create a Translator with a buffered channel. We do NOT need to Start()
	//    it — NotifyStateChange calls broadcast() directly.
	in := make(chan acp.SessionNotification, 16)
	trans := NewTranslator("run-1", in, "", slog.Default())

	// Broadcast a few state_change events to build up in-memory counts.
	trans.NotifyStateChange("creating", "running", 1, "test", nil)
	trans.NotifyStateChange("running", "running", 1, "heartbeat", nil)

	// 5. Create the Service under test.
	svc := New(mgr, trans, slog.Default())

	// 6. Call Status().
	result, err := svc.Status(context.Background())
	require.NoError(t, err)

	// 7. The returned EventCounts must match the Translator's in-memory counts,
	//    NOT the stale {"stale_event": 99} from state.json.
	expected := trans.EventCounts()
	assert.Equal(t, expected, result.State.EventCounts,
		"Status() should overlay Translator's real-time EventCounts, not the stale file value")
	// Sanity: the stale key must NOT appear in the overlay.
	assert.NotContains(t, result.State.EventCounts, "stale_event",
		"stale EventCounts from state.json should be replaced by Translator memory")
	// Sanity: the overlay must contain the event type that was broadcast.
	assert.Equal(t, 2, expected["runtime_update"],
		"Translator should have counted 2 runtime_update events")
}

// newTestService creates a minimal Service for handler-level tests that only
// exercise validation logic (no real ACP session needed).
func newTestService(t *testing.T) *Service {
	t.Helper()
	stateDir := t.TempDir()

	st := apiruntime.State{
		MassVersion: "0.1.0",
		ID:          "test-session",
		Status:      apiruntime.StatusRunning,
		Bundle:      "/tmp/fake-bundle",
	}
	require.NoError(t, spec.WriteState(stateDir, st))

	mgr := acpruntime.New(apiruntime.Config{}, "/tmp/fake-bundle", stateDir, slog.Default())
	in := make(chan acp.SessionNotification, 16)
	trans := NewTranslator("run-1", in, "", slog.Default())
	return New(mgr, trans, slog.Default())
}

func TestService_Stop_ReturnsNil(t *testing.T) {
	svc := newTestService(t)
	err := svc.Stop(context.Background())
	assert.NoError(t, err)
}

func TestService_SetModel_EmptyModelID(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.SetModel(context.Background(), &runapi.SessionSetModelParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing modelId")
}

func TestService_Prompt_EmptyPrompt(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Prompt(context.Background(), &runapi.SessionPromptParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing prompt")
}

func TestService_Stop_EmitsAuditEvent(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	trans := NewTranslator("run-1", in, "", slog.Default())
	ch, _, _ := trans.Subscribe()

	stateDir := t.TempDir()
	st := apiruntime.State{MassVersion: "0.1.0", ID: "s", Status: apiruntime.StatusRunning, Bundle: "/b"}
	require.NoError(t, spec.WriteState(stateDir, st))
	mgr := acpruntime.New(apiruntime.Config{}, "/b", stateDir, slog.Default())
	svc := New(mgr, trans, slog.Default())

	require.NoError(t, svc.Stop(context.Background()))

	ev := <-ch
	assert.Equal(t, runapi.EventTypeRuntimeUpdate, ev.Type)
	ru, ok := ev.Payload.(runapi.RuntimeUpdateEvent)
	require.True(t, ok)
	require.NotNil(t, ru.OperationAudit)
	assert.Equal(t, "stop", ru.OperationAudit.Operation)
	assert.True(t, ru.OperationAudit.Success)
}

func TestService_SetModel_AuditOnValidationFailure(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	trans := NewTranslator("run-1", in, "", slog.Default())
	ch, _, _ := trans.Subscribe()

	stateDir := t.TempDir()
	st := apiruntime.State{MassVersion: "0.1.0", ID: "s", Status: apiruntime.StatusRunning, Bundle: "/b"}
	require.NoError(t, spec.WriteState(stateDir, st))
	mgr := acpruntime.New(apiruntime.Config{}, "/b", stateDir, slog.Default())
	svc := New(mgr, trans, slog.Default())

	_, err := svc.SetModel(context.Background(), &runapi.SessionSetModelParams{})
	require.Error(t, err)

	ev := <-ch
	ru, ok := ev.Payload.(runapi.RuntimeUpdateEvent)
	require.True(t, ok)
	require.NotNil(t, ru.OperationAudit)
	assert.Equal(t, "set_model", ru.OperationAudit.Operation)
	assert.False(t, ru.OperationAudit.Success)
	assert.NotEmpty(t, ru.OperationAudit.Error)
}
