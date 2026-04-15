package agentd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	shim "github.com/zoumo/mass/pkg/shim/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// RecoveryPhase unit tests
// ────────────────────────────────────────────────────────────────────────────

// TestRecoveryPhase_DefaultIsIdle verifies that a freshly created
// ProcessManager starts in the idle recovery phase.
func TestRecoveryPhase_DefaultIsIdle(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	assert.Equal(t, RecoveryPhaseIdle, pm.GetRecoveryPhase(),
		"new ProcessManager should start in idle phase")
}

// TestRecoveryPhase_TransitionsWork verifies that SetRecoveryPhase/
// GetRecoveryPhase round-trips correctly for all defined phases.
func TestRecoveryPhase_TransitionsWork(t *testing.T) {
	pm, _ := setupRecoveryTest(t)

	pm.SetRecoveryPhase(RecoveryPhaseRecovering)
	assert.Equal(t, RecoveryPhaseRecovering, pm.GetRecoveryPhase())

	pm.SetRecoveryPhase(RecoveryPhaseComplete)
	assert.Equal(t, RecoveryPhaseComplete, pm.GetRecoveryPhase())

	pm.SetRecoveryPhase(RecoveryPhaseIdle)
	assert.Equal(t, RecoveryPhaseIdle, pm.GetRecoveryPhase())
}

// TestIsRecovering_TrueOnlyDuringRecovery verifies that IsRecovering returns
// true only when the phase is RecoveryPhaseRecovering.
func TestIsRecovering_TrueOnlyDuringRecovery(t *testing.T) {
	pm, _ := setupRecoveryTest(t)

	assert.False(t, pm.IsRecovering(), "idle → not recovering")

	pm.SetRecoveryPhase(RecoveryPhaseRecovering)
	assert.True(t, pm.IsRecovering(), "recovering → is recovering")

	pm.SetRecoveryPhase(RecoveryPhaseComplete)
	assert.False(t, pm.IsRecovering(), "complete → not recovering")
}

// ────────────────────────────────────────────────────────────────────────────
// RecoverSessions phase lifecycle tests
// ────────────────────────────────────────────────────────────────────────────

// TestRecoverSessions_PhaseTransitions_NoCandidates verifies that
// RecoverSessions sets the recovery phase to Recovering at the start and
// Complete at the end, even when there are no candidates.
func TestRecoverSessions_PhaseTransitions_NoCandidates(t *testing.T) {
	pm, _ := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assert.Equal(t, RecoveryPhaseIdle, pm.GetRecoveryPhase(), "starts idle")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	assert.Equal(t, RecoveryPhaseComplete, pm.GetRecoveryPhase(),
		"should be complete after recovery with no candidates")
}

// TestRecoverSessions_PhaseTransitions_WithLiveShim verifies the recovery
// phase reaches Complete after recovering a live agent, and that per-agent
// RecoveryInfo is populated on the recovered ShimProcess.
func TestRecoverSessions_PhaseTransitions_WithLiveShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a mock shim server.
	srv, socketPath := newMockShimServer(t)
	srv.mu.Lock()
	srv.statusResult = shim.RuntimeStatusResult{
		State: apiruntime.State{
			MassVersion: "0.1.0",
			ID:         "phase-test-agent",
			Status:     apiruntime.StatusRunning,
			Bundle:     "/tmp/test-bundle",
		},
		Recovery: shim.RuntimeStatusRecovery{LastSeq: 0},
	}
	srv.mu.Unlock()

	ws, name := createRecoveryTestAgent(t, ctx, store, "default", "phase-test", apiruntime.StatusRunning, socketPath)
	key := agentKey(ws, name)

	before := time.Now()
	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	// Phase should be complete.
	assert.Equal(t, RecoveryPhaseComplete, pm.GetRecoveryPhase())

	// Per-agent RecoveryInfo should be populated.
	shimProc := pm.GetProcess(key)
	require.NotNil(t, shimProc, "agent should be in processes map")
	require.NotNil(t, shimProc.Recovery, "RecoveryInfo should be set")
	assert.True(t, shimProc.Recovery.Recovered)
	assert.Equal(t, RecoveryOutcomeRecovered, shimProc.Recovery.Outcome)
	assert.NotNil(t, shimProc.Recovery.RecoveredAt)
	assert.False(t, shimProc.Recovery.RecoveredAt.Before(before),
		"RecoveredAt should be at or after test start time")

	// Cleanup.
	srv.close()
	select {
	case <-shimProc.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for recovered process cleanup")
	}
}

// TestRecoverSessions_PhaseTransitions_WithDeadShim verifies the recovery
// phase reaches Complete even when all agents fail to recover.
func TestRecoverSessions_PhaseTransitions_WithDeadShim(t *testing.T) {
	pm, store := setupRecoveryTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	createRecoveryTestAgent(t, ctx, store, "default", "dead-phase-agent",
		apiruntime.StatusRunning, "/tmp/dead-phase-unique.sock")

	err := pm.RecoverSessions(ctx)
	require.NoError(t, err)

	assert.Equal(t, RecoveryPhaseComplete, pm.GetRecoveryPhase(),
		"phase should be complete even when all agents fail")
}
