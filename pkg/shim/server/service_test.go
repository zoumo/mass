package server

import (
	"context"
	"log/slog"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	spec "github.com/zoumo/mass/pkg/runtime-spec"
	acpruntime "github.com/zoumo/mass/pkg/shim/runtime/acp"
)

func TestStatus_EventCountsOverlay(t *testing.T) {
	// 1. Set up temp dir for state storage.
	stateDir := t.TempDir()

	// 2. Write a state.json with stale EventCounts to simulate a previous state write.
	staleState := apiruntime.State{
		MassVersion:  "0.1.0",
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
	trans := NewTranslator("run-1", in, nil)

	// Broadcast a few state_change events to build up in-memory counts.
	trans.NotifyStateChange("creating", "running", 1, "test", nil)
	trans.NotifyStateChange("running", "running", 1, "heartbeat", nil)

	// 5. Create the Service under test.
	svc := New(mgr, trans, "", slog.Default())

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
