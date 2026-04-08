---
estimated_steps: 13
estimated_files: 3
skills_used: []
---

# T01: Add recovery posture types and phase tracking to ProcessManager

Add the type infrastructure for expressing recovery state at both the daemon level and per-session level. This task introduces:

1. `RecoveryPhase` type (idle/recovering/complete) as an atomic field on `ProcessManager`
2. `RecoveryInfo` struct with `Recovered bool`, `RecoveredAt *time.Time`, `Outcome string` (recovered/failed/pending)
3. Per-session recovery metadata stored on `ShimProcess`
4. Methods on `ProcessManager`: `SetRecoveryPhase()`, `RecoveryPhase()`, `IsRecovering()`, `SetSessionRecoveryInfo()`
5. `RecoveryInfo` field added to `SessionStatusResult` in ARI types

Steps:
1. Open `pkg/agentd/process.go`, add `recoveryPhase` atomic field to `ProcessManager` struct and `RecoveryPhase` type constants
2. Add `IsRecovering()`, `SetRecoveryPhase()`, `GetRecoveryPhase()` methods
3. Add `RecoveryInfo` field to `ShimProcess` struct
4. Create new file `pkg/agentd/recovery_posture.go` for the `RecoveryPhase` type, `RecoveryInfo` struct, and constants
5. Open `pkg/ari/types.go`, add `RecoveryInfo` field to `SessionStatusResult`
6. Ensure all new types have proper JSON tags and documentation

## Inputs

- `pkg/agentd/process.go`
- `pkg/ari/types.go`
- `pkg/meta/models.go`

## Expected Output

- `pkg/agentd/recovery_posture.go`
- `pkg/agentd/process.go`
- `pkg/ari/types.go`

## Verification

go build ./pkg/agentd/... && go build ./pkg/ari/... && go vet ./pkg/agentd/... && go vet ./pkg/ari/...
