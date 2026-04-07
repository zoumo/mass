---
estimated_steps: 4
estimated_files: 5
skills_used: []
---

# T02: Move the shim client, process manager, and CLI onto the clean-break session/runtime surface

**Slice:** S02 — shim-rpc clean break
**Milestone:** M002

## Description

Once the server contract is stable, migrate the matched consumers together. The shim client, in-memory process manager, and debug CLI currently mirror the old surface closely enough that they should move in one wave; splitting them would only create temporary protocol drift inside the repo.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| Shim Unix socket plus JSON-RPC transport | Return contextual connect, subscribe, and state errors without pretending recovery succeeded. | Fail start, stop, and status requests with explicit session context and leave the process record inspectable. | Treat malformed notifications and status or history payloads as parse failures or test failures, not best-effort guesses. |
| Long-lived event channel in `ProcessManager` | Keep the process alive and expose the delivery failure instead of crashing the manager. | Preserve non-blocking delivery and surface drops in tests or diagnostics. | Reject the envelope rather than sending partially typed events into the channel. |

## Load Profile

- **Shared resources**: the long-lived shim socket, per-session event channel, and `ProcessManager` in-memory process registry.
- **Per-operation cost**: one RPC call per prompt, cancel, status, or stop plus asynchronous event parsing on the same connection.
- **10x breakpoint**: event-channel backpressure or many concurrent status checks would show up as dropped events or serialized transport latency before anything else.

## Negative Tests

- **Malformed inputs**: unknown notification method names, malformed status or history payloads, and invalid replay offsets passed through the client API.
- **Error paths**: dial failure, disconnect during prompt or subscribe, and stop or shutdown on a server that is already closing.
- **Boundary conditions**: no history available yet, `recovery.lastSeq` unset or zero, and repeated subscribe or status calls on the same long-lived client.

## Steps

1. Rename the `pkg/agentd/shim_client.go` RPC helpers to the clean-break method names, parse `session/update` and `runtime/stateChange`, and add typed helpers for `runtime/status` and `runtime/history` so downstream callers stop depending on `$/event` or flattened GetState results.
2. Update `pkg/agentd/process.go` to subscribe with the new surface, read nested runtime status and recovery metadata, and keep the current in-memory lifecycle model intact without claiming daemon-restart reconnect support.
3. Refresh `cmd/agent-shim-cli/main.go` so the direct debug harness uses the renamed methods and prints the new notification, status, and history payloads rather than the legacy wrapper.
4. Extend `pkg/agentd/shim_client_test.go` and `pkg/agentd/process_test.go` so prompt, cancel, subscribe, status, and shutdown paths all pass on the renamed protocol with no dependency on `$/event`.

## Must-Haves

- [ ] No live source-path code in `pkg/agentd` or `cmd/agent-shim-cli` depends on `$/event` or PascalCase shim methods.
- [ ] `ProcessManager.State` and related callers remain truthful when `runtime/status` becomes nested and adds recovery metadata.
- [ ] The debug CLI still works as a human proof harness against a live shim after the rename.

## Verification

- `go test ./pkg/agentd -count=1`
- `go build ./cmd/agent-shim-cli`

## Observability Impact

- Signals added and changed: the client parses session/update and runtime/stateChange, and process state reads come from runtime/status recovery metadata
- How a future agent inspects this: go test ./pkg/agentd -count=1 and the agent-shim-cli state, prompt, and shutdown commands against a live shim
- Failure state exposed: connect, subscribe, and status failures keep their session context instead of collapsing into transport-only errors

## Inputs

- `pkg/rpc/server.go` — new protocol surface from T01
- `pkg/rpc/server_test.go` — server-level proof of the renamed methods
- `pkg/agentd/shim_client.go` — legacy client assumptions to remove
- `pkg/agentd/process.go` — long-lived subscribed runtime path that depends on the client shape
- `cmd/agent-shim-cli/main.go` — direct human-facing shim harness

## Expected Output

- `pkg/agentd/shim_client.go` — clean-break shim client methods and parsers
- `pkg/agentd/shim_client_test.go` — client parsing and RPC assertions
- `pkg/agentd/process.go` — process manager wired to runtime/status and the renamed subscribe surface
- `pkg/agentd/process_test.go` — process-manager lifecycle proof on the new shim protocol
- `cmd/agent-shim-cli/main.go` — debug CLI updated to the renamed protocol and envelope shape
