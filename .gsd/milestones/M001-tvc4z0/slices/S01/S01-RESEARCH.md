# S01 — Scaffolding + Phase 1.3 exitCode — Research

**Date:** 2026-04-03

## Summary

S01 delivers two distinct work items: (1) add `exitCode` to the shim's `State` and `GetStateResult` so agentd can surface process exit codes, and (2) create the `cmd/agentd` daemon scaffolding that can parse config.yaml, create basic infrastructure, and listen on the ARI socket.

The shim exitCode change is self-contained — modify `pkg/spec/state_types.go`, `pkg/runtime/runtime.go`, and `pkg/rpc/server.go`. The scaffolding creates new files (`pkg/agentd/config.go`, `cmd/agentd/main.go`) and wires up existing components (WorkspaceManager, ARI Server).

## Recommendation

Implement shim exitCode first (no dependencies, easy to verify), then agentd scaffolding. Keep the daemon minimal for S01 — just config parsing, infrastructure setup, socket listening, and signal handling. RuntimeClass registry, Session Manager, Process Manager, and session/* ARI methods come in later slices.

## Implementation Landscape

### Key Files

**Shim exitCode (modify existing):**
- `pkg/spec/state_types.go` — Add `ExitCode *int` field to `State` struct. Optional because it's only populated when process has exited.
- `pkg/runtime/runtime.go` — Capture exit code in the background goroutine that calls `cmd.Wait()`. Use `cmd.ProcessState.ExitCode()` after `cmd.Wait()` returns. Update `WriteState` call with ExitCode.
- `pkg/rpc/server.go` — Add `ExitCode *int` to `GetStateResult`. Populate from `st.ExitCode` in `handleGetState`.

**agentd scaffolding (new files):**
- `pkg/agentd/config.go` — Define `Config` struct for config.yaml parsing. Fields: `Socket`, `WorkspaceRoot`, `MetaDB`, `Runtime`, `SessionPolicy`, `RuntimeClasses`. Implement `ParseConfig(path string) (Config, error)` using `gopkg.in/yaml.v3`.
- `cmd/agentd/main.go` — Daemon entry point. Parse flags (--config path), parse config.yaml, create WorkspaceManager, create Registry, create ARI Server, start server, handle SIGTERM/SIGINT for graceful shutdown.

**agentd scaffolding (existing files to use):**
- `pkg/workspace/manager.go` — `WorkspaceManager` with `Prepare` and `Cleanup` methods. Already implemented.
- `pkg/ari/server.go` — ARI JSON-RPC server over Unix socket. Already handles `workspace/prepare`, `workspace/list`, `workspace/cleanup`. Constructor: `New(manager, registry, socketPath, baseDir)`.
- `pkg/ari/registry.go` — Registry for workspace metadata tracking. Already implemented.

### Build Order

1. **Shim exitCode** (independent, testable immediately)
   - Add `ExitCode *int` to `State` struct
   - Modify runtime to capture exit code on process exit
   - Add `ExitCode` to `GetStateResult`
   - Run existing tests: `go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/...`

2. **agentd config parsing** (pkg/agentd/config.go)
   - Define Config struct with YAML tags
   - Implement ParseConfig
   - Unit test parsing

3. **agentd main** (cmd/agentd/main.go)
   - Wire up flags, config parsing, infrastructure creation
   - Start ARI server
   - Signal handling
   - Integration test: start daemon, verify socket exists, send SIGTERM, verify clean exit

### Verification Approach

**Shim exitCode:**
```bash
# Run existing tests
go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/... -v

# Integration test: start shim with mockagent, kill agent process, call GetState, verify exitCode
```

**agentd scaffolding:**
```bash
# Build daemon
go build -o bin/agentd ./cmd/agentd

# Create minimal config.yaml
cat > /tmp/agentd-config.yaml <<EOF
socket: /tmp/agentd-test.sock
workspaceRoot: /tmp/agentd-workspaces
EOF

# Start daemon
./bin/agentd --config /tmp/agentd-config.yaml &
PID=$!

# Verify socket exists
test -S /tmp/agentd-test.sock && echo "socket OK"

# Send SIGTERM
kill -TERM $PID
wait $PID
echo "Exit code: $?"  # should be 0
```

## Don't Hand-Roll

| Problem | Existing Solution | Why Use It |
|---------|------------------|------------|
| YAML parsing | `gopkg.in/yaml.v3` | Standard YAML library, already used in Go ecosystem |
| JSON-RPC 2.0 | `github.com/sourcegraph/jsonrpc2` | Already used in pkg/ari and pkg/rpc |
| UUID generation | `github.com/google/uuid` | Already used in pkg/ari for workspace IDs |

## Constraints

- **Config file format**: Must be YAML (as specified in design doc). Path configurable via --config flag, default to `/etc/agentd/config.yaml`.
- **Socket permissions**: Unix socket should be created with 0600 permissions (only owner can connect).
- **Graceful shutdown**: Must handle SIGTERM and SIGINT, close listener, clean up resources.
- **Backward compatibility**: Adding `ExitCode` to `State` is additive — existing state.json files without `exitCode` field will parse correctly (field will be nil).

## Common Pitfalls

- **ExitCode vs ExitStatus**: Use `cmd.ProcessState.ExitCode()` (Go 1.12+), not `cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()`. The former handles signal-terminated processes correctly (returns -1).
- **Process already reaped**: The background goroutine calls `cmd.Wait()` which reaps the process. Subsequent calls to `cmd.Wait()` return the same error. Store the exit code in the Manager struct or write to state.json immediately.
- **Config file missing**: If config file doesn't exist, agentd should fail with clear error, not use defaults silently. This differs from shim which has sensible defaults.
- **Socket already exists**: If socket file exists from previous run (unclean shutdown), agentd should remove it before listening. Check with `os.Remove` before `net.Listen("unix", path)`.

## Open Risks

- **RuntimeClass validation**: S01 creates the config parsing but RuntimeClass registry is S03. Should we validate runtimeClasses in config parsing or defer to registry? Recommendation: defer validation to S03, just parse the structure for now.
- **Session methods in ARI Server**: The existing `pkg/ari/server.go` only handles workspace/*. S06 adds session/*. For S01, we just use the existing server as-is. Session methods will be added later.

## Forward Intelligence

- **S02 Metadata Store**: Will need `MetaDB` path from config. Ensure config parsing includes this field.
- **S03 RuntimeClass Registry**: Will need `RuntimeClasses` map from config. Ensure config parsing includes this field.
- **S04 Session Manager**: Will need `SessionPolicy` from config. Ensure config parsing includes this field.
- **S06 ARI Service**: Will extend `pkg/ari/server.go` with session/* methods. The server struct may need refactoring to accept additional managers.

## Skills Discovered

No relevant skills found for this slice (Go daemon scaffolding, JSON-RPC, YAML parsing are standard library or well-known packages).