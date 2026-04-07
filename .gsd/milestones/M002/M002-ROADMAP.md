# M002: 

## Vision
TBD

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Design contract convergence | high | — | ✅ | TBD |
| S02 | shim-rpc clean break | high | — | ⬜ | After this: the shim server, agentd, CLI, and ARI all speak the clean-break `session/*` + `runtime/*` surface, and focused tests prove replayable history and status hooks without claiming restart truth. |
| S03 | Recovery and persistence truth-source | medium | S01, S02 | ⬜ | TBD |
| S04 | Real CLI integration verification | medium | S02, S03 | ⬜ | TBD |
