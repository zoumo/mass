# S01: Scaffolding + Phase 1.3 exitCode — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-03T01:17:36.061Z

# S01 UAT: Shim ExitCode + agentd Daemon Scaffolding

## Preconditions
- Go toolchain installed
- Project cloned with all dependencies
- Working directory at project root

## Test Cases

### TC-01: ExitCode field in State struct
**Purpose:** Verify ExitCode field exists with correct type and JSON tag

1. Read `pkg/spec/state_types.go`
2. Verify State struct contains `ExitCode *int` field
3. Verify JSON tag is `json:"exitCode,omitempty"`
4. **Expected:** Field exists with pointer type and omitempty tag

### TC-02: ExitCode captured after process exit
**Purpose:** Verify runtime captures exit code when process stops

1. Read `pkg/runtime/runtime.go`
2. Find background goroutine that calls `cmd.Wait()`
3. Verify `cmd.ProcessState.ExitCode()` is called after Wait
4. Verify exit code is included in WriteState call for stopped state
5. **Expected:** Exit code captured and persisted in stopped state

### TC-03: ExitCode in GetStateResult
**Purpose:** Verify RPC returns ExitCode to clients

1. Read `pkg/rpc/server.go`
2. Verify GetStateResult struct has `ExitCode *int` field with omitempty
3. Verify handleGetState populates ExitCode from st.ExitCode
4. **Expected:** GetStateResult includes ExitCode, populated from state

### TC-04: Config struct YAML parsing
**Purpose:** Verify Config parses valid YAML with required fields

1. Create test config file:
   ```yaml
   socket: /tmp/test.sock
   workspaceRoot: /tmp/workspaces
   metaDB: /tmp/metadata.db
   ```
2. Run: `go run ./cmd/agentd --config test.yaml` (should start)
3. **Expected:** Config parsed without error, daemon initializes

### TC-05: Config validation rejects missing required fields
**Purpose:** Verify ParseConfig validates required fields

1. Create config missing socket field:
   ```yaml
   workspaceRoot: /tmp/workspaces
   metaDB: /tmp/metadata.db
   ```
2. Run: `go run ./cmd/agentd --config invalid.yaml`
3. **Expected:** Error message about missing Socket field

### TC-06: Daemon startup sequence
**Purpose:** Verify daemon initializes all components

1. Create minimal config with all required fields
2. Start daemon: `bin/agentd --config config.yaml`
3. Check logs for:
   - "loaded config from {path}"
   - "workspace manager initialized"
   - "registry initialized"
   - "ARI server created"
   - "starting ARI server on {socket}"
4. **Expected:** All initialization messages logged

### TC-07: Socket file created
**Purpose:** Verify ARI socket file exists after startup

1. Start daemon with config pointing to /tmp/test.sock
2. Check: `ls -la /tmp/test.sock`
3. **Expected:** Socket file exists as Unix domain socket (type 's')

### TC-08: Graceful shutdown
**Purpose:** Verify daemon shuts down cleanly on SIGTERM

1. Start daemon in background
2. Send SIGTERM: `kill -TERM {pid}`
3. Check logs for:
   - "received signal terminated, shutting down"
   - "shutdown complete"
4. Verify socket file removed
5. **Expected:** Clean shutdown messages, socket cleaned up

### TC-09: Unclean shutdown recovery
**Purpose:** Verify daemon recovers from leftover socket file

1. Create fake socket file: `touch /tmp/test.sock`
2. Start daemon with same socket path
3. **Expected:** Daemon starts successfully, old socket file replaced

### TC-10: Build verification
**Purpose:** Verify all packages build cleanly

1. Run: `go build ./...`
2. Run: `go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/...`
3. **Expected:** Build succeeds, all tests pass

## Pass Criteria
- All 10 test cases pass
- No build errors
- No test failures
