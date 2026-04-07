# M003: Recovery and Safety Hardening

## Vision
Harden restart truth and fail-closed recovery so `agentd` can restart, rediscover live shims, rebuild session state honestly, and block unsafe operations whenever recovery certainty is incomplete.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Fail-Closed Recovery Posture and Discovery Contract | high | — | ⬜ | After this: operators can inspect sessions after an uncertain restart and see an explicit healthy/degraded/blocked recovery posture, while prompt/stop/cleanup-style operations are rejected until certainty is re-established. |
| S02 | Live Shim Reconnect and Truthful Session Rebuild | high | S01 | ⬜ | After this: restarting `agentd` while shims stay alive reconnects recovered sessions, restores truthful running state, and allows prompt/stop only when reconciliation reaches healthy. |
| S03 | Atomic Event Resume and Damaged-Tail Tolerance | medium | S02 | ⬜ | After this: a recovered session with a truncated `events.jsonl` tail still reports truthful status, replays only the durable event prefix, and refuses live operation if resume certainty cannot be established. |
| S04 | Reconciled Workspace Ref Truth and Safe Cleanup | medium | S02, S03 | ⬜ | After this: after restart, workspaces referenced by recovered or uncertain sessions cannot be cleaned up; once sessions stop and refs reconcile to zero, cleanup succeeds normally. |
