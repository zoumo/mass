# M001-tlbeko: 

## Vision
Declarative workspace provisioning — Workspace Manager prepares workspaces from spec (Git/EmptyDir/Local), executes hooks, tracks references, and cleans up when unused

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Workspace Spec + Git Handler | high | — | ⬜ | WorkspaceSpec types defined; Git clone works with ref/depth support |
| S02 | EmptyDir + Local Handlers | low | S01 | ⬜ | EmptyDir creates managed directory; Local validates existing path |
| S03 | Hook Execution | medium | S01 | ⬜ | Setup hooks execute sequentially; failure aborts prepare and cleans up |
| S04 | Workspace Lifecycle | medium | S01, S02, S03 | ⬜ | WorkspaceManager Prepare/Cleanup work; ref counting prevents premature cleanup |
| S05 | ARI Workspace Methods | low | S04 | ⬜ | ARI workspace/* methods work; integration test: prepare → session → cleanup |
