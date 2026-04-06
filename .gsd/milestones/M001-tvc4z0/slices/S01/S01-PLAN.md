# S01: Scaffolding + Phase 1.3 exitCode

**Goal:** Add exitCode to shim State/GetStateResult and create agentd daemon scaffolding that parses config.yaml and listens on ARI socket
**Demo:** After this: agentd daemon starts with config.yaml, listens on socket; shim exitCode surfaces in GetState

## Tasks
- [x] **T01: Added ExitCode field to shim State and GetStateResult, capturing process exit code in background goroutine** — ### Steps

1. Add `ExitCode *int` field to `State` struct in `pkg/spec/state_types.go`. The field is optional (pointer) because it's only populated after process exits.
2. Modify `pkg/runtime/runtime.go`: In the background goroutine that calls `cmd.Wait()`, capture exit code using `cmd.ProcessState.ExitCode()` and include it in the `WriteState` call for stopped state.
3. Add `ExitCode *int` field to `GetStateResult` struct in `pkg/rpc/server.go`.
4. In `handleGetState` function in `pkg/rpc/server.go`, populate `ExitCode` from `st.ExitCode`.

### Must-Haves

- [ ] `State` struct has `ExitCode *int` field with JSON tag `exitCode,omitempty`
- [ ] `GetStateResult` struct has `ExitCode *int` field with JSON tag `exitCode,omitempty`
- [ ] Background goroutine in runtime.go captures exit code via `cmd.ProcessState.ExitCode()`
- [ ] `handleGetState` populates ExitCode from state
  - Estimate: 30m
  - Files: pkg/spec/state_types.go, pkg/runtime/runtime.go, pkg/rpc/server.go
  - Verify: go test ./pkg/spec/... ./pkg/runtime/... ./pkg/rpc/... -v
- [x] **T02: Created agentd daemon entry point with YAML config parsing, workspace manager and ARI server bootstrap, and graceful shutdown handling.** — ### Steps

1. Create `pkg/agentd/config.go` with `Config` struct containing fields: `Socket` (string), `WorkspaceRoot` (string), `MetaDB` (string), `Runtime` (struct), `SessionPolicy` (struct), `RuntimeClasses` (map). Add YAML tags for parsing.
2. Implement `ParseConfig(path string) (Config, error)` function that reads file and unmarshals YAML using `gopkg.in/yaml.v3`. Return error if file doesn't exist or parse fails.
3. Create `cmd/agentd/main.go` with main function:
   - Parse `--config` flag (default: `/etc/agentd/config.yaml`)
   - Call `ParseConfig` to load config
   - Create `WorkspaceManager` via `workspace.NewWorkspaceManager()`
   - Create `Registry` via `ari.NewRegistry()`
   - Create ARI `Server` via `ari.New(manager, registry, config.Socket, config.WorkspaceRoot)`
   - Remove existing socket file if present (unclean shutdown recovery)
   - Start server via `srv.Serve()` in goroutine
   - Setup signal handler for SIGTERM/SIGINT, call `srv.Shutdown()` on signal
   - Wait for shutdown completion
4. Add package main declaration and imports.

### Must-Haves

- [ ] `pkg/agentd/config.go` exists with Config struct and ParseConfig function
- [ ] `cmd/agentd/main.go` exists with daemon entry point
- [ ] Config struct has Socket, WorkspaceRoot, MetaDB fields
- [ ] ParseConfig returns error on missing/invalid file
- [ ] Socket file removed before listening if exists
- [ ] SIGTERM/SIGINT handled with graceful shutdown
  - Estimate: 1h
  - Files: pkg/agentd/config.go, cmd/agentd/main.go
  - Verify: go build -o bin/agentd ./cmd/agentd
