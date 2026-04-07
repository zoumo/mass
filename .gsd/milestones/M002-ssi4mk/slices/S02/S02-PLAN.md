# S02: shim-rpc clean break

**Goal:** Replace the old PascalCase + `$/event` shim surface with the converged protocol and close the obvious recovery entrypoint gap.
**Demo:** After this: After this: the shim surface has one clean-break protocol story — `session/*` plus `runtime/*` — and recovery no longer depends on a history-then-subscribe race.

## Tasks
