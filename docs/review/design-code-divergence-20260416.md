# Design ↔ Code/Spec Divergence Review — 2026-04-16

## Summary

Deep review of design docs against authoritative specs (`ari-spec.md`, `shim-rpc-spec.md`,
`runtime-spec.md`) and code (`pkg/shim/server/register.go`, `pkg/runtime-spec/api/`).

Code and authoritative specs are aligned. Supporting docs had stale terminology.

## Findings

### 1. ARI Method Names (supporting docs → ari-spec.md)

| # | Issue | Files Affected | Status |
|---|-------|----------------|--------|
| 1.1 | `agent/set` → `agent/create` + `agent/update` | contract-convergence.md, mass.md, roadmap.md | **RESOLVED** |
| 1.2 | `workspace/status` → `workspace/get` | contract-convergence.md, mass.md, roadmap.md, communication.md, workspace-spec.md | **RESOLVED** |
| 1.3 | `agentrun/status` → `agentrun/get` | contract-convergence.md, mass.md, roadmap.md | **RESOLVED** |
| 1.4 | `agentrun/attach` removed | contract-convergence.md, roadmap.md | **RESOLVED** |

### 2. Shim Method/Notification Names (supporting docs → shim-rpc-spec.md)

| # | Issue | Files Affected | Status |
|---|-------|----------------|--------|
| 2.1 | `session/subscribe` + `runtime/history` → `session/watch_event` | agent-shim.md, contract-convergence.md, roadmap.md, ari-spec.md | **RESOLVED** |
| 2.2 | `session/update` + `runtime/state_change` → `shim/event` | contract-convergence.md, roadmap.md, ari-spec.md | **RESOLVED** |

### 3. Code Features Missing from Spec

| # | Issue | Spec File | Status |
|---|-------|-----------|--------|
| 3.1 | `session/load`, `session/set_model`, `session/models` not in shim-rpc-spec | shim-rpc-spec.md | **RESOLVED** — all three methods added |
| 3.2 | `SessionState.Models` / `ModelInfo` not in runtime-spec | runtime-spec.md | **RESOLVED** — `models` field + types added to State schema |

### 4. Removed Event Types

| # | Issue | Spec File | Status |
|---|-------|-----------|--------|
| 4.1 | `file_write`, `file_read`, `command` event types removed from code but still in spec | shim-rpc-spec.md, runtime-spec.md | **RESOLVED** — removed prior to this review (2026-04-16) |

### 5. Internal Contradictions

| # | Issue | File | Status |
|---|-------|------|--------|
| 5.1 | agent-shim.md method table and implementation status inconsistent | agent-shim.md | **RESOLVED** — method table updated, stale "not registered" notes removed |

## Verification

```bash
# All zero hits (verified 2026-04-16):
rg "agent/set|agentrun/status|agentrun/attach" docs/design/  # 1 hit: legacy explanation only
rg "workspace/status" docs/design/                            # 0 hits
rg "session/subscribe|runtime/history" docs/design/           # 0 hits

# Acceptable residual (legacy/ACP context only):
rg "session/update|runtime/state_change" docs/design/         # 4 hits: all "replaces ..." context
```
