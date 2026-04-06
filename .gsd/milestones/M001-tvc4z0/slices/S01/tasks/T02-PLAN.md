---
estimated_steps: 21
estimated_files: 2
skills_used: []
---

# T02: Create agentd daemon scaffolding with config parsing

### Steps

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

## Inputs

- `pkg/workspace/manager.go`
- `pkg/ari/server.go`
- `pkg/ari/registry.go`

## Expected Output

- `pkg/agentd/config.go`
- `cmd/agentd/main.go`

## Verification

go build -o bin/agentd ./cmd/agentd
