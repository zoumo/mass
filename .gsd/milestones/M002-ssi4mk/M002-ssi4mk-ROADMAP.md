# M002-ssi4mk: Contract Convergence and ACP Runtime Truthfulness

## Vision
收口设计契约、执行 shim-rpc clean break，并让 OAR 的 runtime / recovery 语义在真实 ACP CLI 面前说真话。

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Design contract convergence | high | — | ⬜ | After this: the design docs and runtime contract can be read as one coherent story for Room, Session, Runtime, Workspace, bootstrap, and state mapping. |
| S02 | shim-rpc clean break | high | S01 | ⬜ | After this: the shim surface has one clean-break protocol story — `session/*` plus `runtime/*` — and recovery no longer depends on a history-then-subscribe race. |
| S03 | Recovery and persistence truth-source | medium | S01, S02 | ⬜ | After this: session config, workspace identity, cleanup, and security-boundary rules are explicit enough that restart/recovery and cleanup behavior can be trusted instead of inferred. |
| S04 | Real CLI integration verification | medium | S02, S03 | ⬜ | After this: the converged contract is proven against real `gsd-pi` and `claude-code` bundle flows, so M002 ends with live ACP evidence instead of mock-only confidence. |
