# S05 — Research

**Date:** 2026-04-03

## Summary

Slice S05 (Process Manager) implements the shim process lifecycle management for agentd. The Process Manager is responsible for starting agent-shim processes, managing their lifecycle (stop, state queries), connecting to shim RPC for agent interaction, and subscribing to typed event streams from shim.

This is a **high-risk** slice because it involves process forking with exec.Cmd, Unix socket communication with jsonrpc2, event subscription and forwarding, and integration with SessionManager, RuntimeClassRegistry, and workspace paths.

## Recommendation

Implement ProcessManager with a ShimClient helper type following the established patterns from pkg/runtime and pkg/rpc. Create `pkg/agentd/process.go` with ProcessManager that manages shim processes, and `pkg/agentd/shim_client.go` with ShimClient for shim RPC communication. Follow the Start workflow from design doc: resolve runtimeClass → generate config.json → create bundle → fork shim → connect socket → subscribe events.

## Implementation Landscape

### Key Files

**Create:**
- `pkg/agentd/process.go` — ProcessManager struct with Start/Stop/State methods, ShimProcess tracking, config.json generation, bundle creation
- `pkg/agentd/shim_client.go` — ShimClient wrapping jsonrpc2.Conn for shim RPC (Prompt, Cancel, Subscribe, GetState, Shutdown)
- `pkg/agentd/process_test.go` — Integration tests using mockagent binary

**Modify:**
- `pkg/agentd/config.go` — May need BundleRoot or StateRoot fields for directory paths
- `cmd/agentd/main.go` — Wire ProcessManager into daemon initialization (S06 will complete this)

### Dependencies (from completed slices)

- **S02 (Metadata Store)**: meta.Store for session persistence, meta.Session model
- **S03 (RuntimeClass Registry)**: RuntimeClassRegistry for resolving runtimeClass names, RuntimeClass struct with Command/Args/Env/Capabilities
- **S04 (Session Manager)**: SessionManager for state transitions, Transition method, SessionState constants

### Existing Patterns to Follow

1. **JSON-RPC client** (pkg/rpc/server_test.go:68-76):
   ```go
   nc, err := net.Dial("unix", socketPath)
   stream := jsonrpc2.NewPlainObjectStream(nc)
   conn := jsonrpc2.NewConn(context.Background(), stream, jsonrpc2.AsyncHandler(handler))
   ```

2. **Event notification handling** (pkg/rpc/server_test.go:50-78): notifHandler collects "$/event" notifications, jsonrpc2.Handler interface for async notifications

3. **Config generation** (pkg/spec/types.go): spec.Config struct with OarVersion, Metadata, AgentRoot, AcpAgent, Permissions; spec.AcpProcess with Command, Args, Env; spec.AgentRoot.Path is relative ("workspace")

4. **State directory management** (pkg/spec/state.go): spec.StateDir(baseDir, id), spec.ShimSocketPath(stateDir), spec.WriteState/ReadState

5. **Test pattern** (pkg/runtime/runtime_test.go): TestMain builds mockagent binary once, suite.Suite pattern with testify, newManager helper for test setup

### Build Order

1. **ShimClient** (pkg/agentd/shim_client.go): Wrap jsonrpc2.Conn for shim RPC, methods: Prompt, Cancel, Subscribe, GetState, Shutdown, handle "$/event" notifications, unit tests with mock JSON-RPC server

2. **ProcessManager core** (pkg/agentd/process.go): ProcessManager struct with registry, sessionMgr, config, ShimProcess tracking, config.json generation from RuntimeClass + Session, bundle directory creation with workspace symlink

3. **Start workflow** (ProcessManager.Start): Resolve RuntimeClass, get Session, generate config.json, create bundle, fork shim process, wait for socket, connect via ShimClient, subscribe events, transition session state to "running"

4. **Stop/State/Connect methods**: Stop (call Shutdown RPC or kill process), State (call GetState RPC), Connect (return ShimClient)

5. **Integration tests** (pkg/agentd/process_test.go): Test Start, Prompt, Stop, multiple sessions

### Start Workflow Detail (from design doc)

```
ProcessManager.Start(sessionID):
  1. Resolve session → get runtimeClass, workspace path, systemPrompt, mcpServers, permissions
  2. Lookup runtimeClass → get command, args, env, capabilities
  3. Generate OAR Runtime Spec (config.json):
       acpAgent:
         systemPrompt = session.systemPrompt
         process: { command, args, env }
         session: { mcpServers }
       agentRoot.path = "workspace"
       permissions    = session.permissions
  4. Create bundle directory, write config.json
  5. Create workspace symlink: bundle/workspace → workspace.path
  6. Fork shim: agent-shim --bundle <bundle-dir> --id <sessionID> --state-dir /run/agentd/shim
  7. Wait for socket at /run/agentd/shim/<sessionID>/agent-shim.sock
  8. Connect to shim socket via ShimClient
  9. Call shim.Subscribe() to receive events
  10. Transition session state → Running
```

### Directory Structure

```
/run/agentd/shim/<sessionID>/
├── agent-shim.sock    ← RPC socket
├── state.json         ← shim state
└── events.jsonl       ← event log

<workspaceRoot>/bundles/<sessionID>/
├── config.json        ← OAR Runtime Spec
└── workspace          ← symlink to actual workspace path
```

### Key Design Decisions

1. **Bundle directory location**: `<workspaceRoot>/bundles/<sessionID>` (or use separate BundleRoot config field)
2. **State directory location**: `/run/agentd/shim/<sessionID>` (matches design doc)
3. **Process health monitoring**: Background goroutine watches cmd.Wait, updates session state on exit
4. **Event forwarding**: ShimClient.Subscribe handler forwards events to ProcessManager event channel
5. **Connection reuse**: ShimClient caches jsonrpc2.Conn for repeated RPC calls

### Risks and Gotchas

1. **Socket path length**: macOS has 104-byte sun_path limit. Use short paths like `/run/agentd/shim/<uuid>/agent-shim.sock`
2. **Race condition on startup**: Socket may not exist immediately after fork. Use polling with timeout.
3. **Process cleanup**: Ensure shim process is killed on error paths (handshake failure, timeout)
4. **Bundle cleanup**: Remove bundle directory when session is deleted
5. **State directory cleanup**: Remove state directory when shim exits

### Test Strategy

1. **Unit tests for ShimClient**: Mock JSON-RPC server to test RPC methods
2. **Integration tests with mockagent**: Build mockagent binary in TestMain, test full lifecycle
3. **Test cases**: TestProcessManagerStart, TestProcessManagerPrompt, TestProcessManagerStop, TestProcessManagerMultipleSessions, TestProcessManagerBadRuntimeClass, TestProcessManagerProcessCrash