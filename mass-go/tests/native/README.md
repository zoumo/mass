# Real Agent validation

This opt-in group drives the public Go Runtime/catalog APIs with real installed
Codex and Claude CLIs and models. Default `go test ./...` excludes it. It uses
the configured account/authentication, creates native session history and uses
private test workspaces. It does not modify credentials or global configuration.

From `mass-go`, with Go 1.26.9 and absolute executable/evidence paths:

```sh
mkdir -p /absolute/evidence
MASS_NATIVE_CODEX=/absolute/codex \
MASS_NATIVE_CLAUDE=/absolute/claude \
MASS_NATIVE_EVIDENCE=/absolute/evidence \
mise exec go@1.26.9 -- go test -tags=native -timeout=15m -count=1 -v ./tests/native
```

Selected tests fail on missing prerequisites; they never silently replace native
CLIs with mocks. The helper-only `TestColdWorker` skips direct top-level execution
and is invoked by the cold-recovery parent in separate host processes. Use
`-run 'TestConcurrentSessions/codex'` to select one protocol and scenario. A
filtered run certifies only that selection.

| Scenario | Required evidence |
| --- | --- |
| Concurrent sessions | Two overlapping creations/foreground turns, distinct native IDs and markers, contiguous per-session cursors, survivor context after sibling detach |
| Subagents | Actual native child tool, child relationship, independently attributed child marker and terminal event; child facts never acquire root submission authority |
| Cold recovery | First host creates/completes/detaches and exits; second host loads only key/opaque disk binding, verifies original identity through ResumeSession, then recalls original marker on explicit new input |

Each fixture retains JSONL events, snapshots and operation receipts under a new
evidence directory. Cold recovery also retains its host logs, original key and
opaque bindings. Environment values and credentials are not saved. Bindings use
atomic file replacement for this clean-process test, without claiming crash/power
loss durability. CLI cleanup is performed through Runtime.Stop on every test exit.
The test does not assert native deletion or rollback. For an OS-level descendant
cleanup audit, capture PID/start identities during the run and read them back
afterward; a Stop receipt alone does not audit every descendant.

This group was verified on native macOS arm64 with Codex 0.163.0 and Claude 2.1.258. Codex 0.144.4
cannot decode with the pinned SDK because its Thread response omits required
`projectId`; use an explicit supported executable rather than relying on PATH.
If retaining a Codex 0.163.0 binary snapshot, also retain its adjacent packaged
`codex-code-mode-host`; copying only `codex` can initialize and resume successfully
while failing real tool execution. Retain hashes for both files.
These checks do not certify Linux, remote transports, all provider/model settings,
native release, arbitrary background agents, lossless history replay or production
readiness.
