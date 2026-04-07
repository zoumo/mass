# M001-terminal: Phase 1.1 — Terminal Operations

## Vision

From "stub responses" to "full terminal execution" — agent-shim can execute shell commands in the agent's workspace, capture output, and control command lifecycle through ACP protocol methods.

## Intent

Enable agents to run shell commands within their workspace for tasks like:
- Building/compiling code
- Running tests
- Git operations
- Package installation
- File manipulation via CLI tools
- System inspection (env, processes, etc.)

This completes the ACP Client interface implementation, moving from stubs that return "terminal not supported" to full terminal lifecycle management.

## Current State

**Implemented stubs (pkg/runtime/client.go):**
- CreateTerminal → returns "terminal not supported"
- KillTerminalCommand → returns "terminal not supported"
- TerminalOutput → returns "terminal not supported"
- ReleaseTerminal → returns "terminal not supported"
- WaitForTerminalExit → returns "terminal not supported"

**Existing infrastructure to leverage:**
- PermissionPolicy in pkg/spec/types.go (approve-all, approve-reads, deny-all)
- agentRoot.path resolution in spec.ResolveAgentRoot
- Process execution patterns from runtime.Manager.Create

## Architecture

Terminal operations are ACP Client callbacks — the agent requests them during a turn:
```
agent (ACP session) → acpClient.CreateTerminal → TerminalManager → fork/exec command
                                      ↓
                               TerminalOutput ← output buffer
                                      ↓
                               KillTerminalCommand → SIGTERM/SIGKILL
                                      ↓
                               ReleaseTerminal → cleanup
                                      ↓
                               WaitForTerminalExit → wait for process
```

### TerminalManager

New component in pkg/runtime/terminal.go:
- Tracks active terminals by ID (UUID)
- Stores output buffers with byte limit truncation
- Manages process lifecycle (cmd.Wait background goroutine)
- Handles concurrent terminals per session

### Permission Policy

Terminal operations respect PermissionPolicy:
- **approve-all**: Allow all terminal operations
- **approve-reads**: Block terminal operations (terminal can execute write commands)
- **deny-all**: Block all terminal operations

This follows the pattern established for fs/* operations.

## Requirements Coverage

| ID | Requirement | Slice | Status |
|----|-------------|-------|--------|
| R020 | Terminal command execution | S01 | pending |
| R026 | Terminal output retrieval | S02 | pending |
| R027 | Terminal command control | S03 | pending |
| R028 | Terminal resource cleanup | S04 | pending |
| R029 | Terminal exit waiting | S05 | pending |

## Technical Findings

### ACP Protocol Details

From ACP SDK documentation:
- **CreateTerminalRequest**: command, args, cwd, env, outputByteLimit, sessionId
- **CreateTerminalResponse**: terminalId (UUID)
- **TerminalOutputRequest**: sessionId, terminalId
- **TerminalOutputResponse**: output (string), truncated (bool), exitStatus (exitCode/signal)
- **KillTerminalCommandRequest**: sessionId, terminalId
- **ReleaseTerminalRequest**: sessionId, terminalId
- **WaitForTerminalExitRequest**: sessionId, terminalId
- **WaitForTerminalExitResponse**: exitStatus

### Output Capture

- stdout + stderr merged into single output string
- OutputByteLimit truncates from beginning when exceeded
- Truncation must occur at character boundary (valid UTF-8)
- Default OutputByteLimit if not specified: 1MB (1048576 bytes)

### Process Execution

- Command runs in agentRoot.path (resolved workspace directory)
- Environment: merge session env with request env
- Fork/exec pattern from runtime.Manager.Create
- Background goroutine waits for process exit
- Signal handling: SIGTERM → 5s grace → SIGKILL

### Exit Status

- exitCode: int (null if signal termination)
- signal: string (null if normal exit)
- Unix signals: SIGTERM, SIGKILL, SIGINT, etc.

## Assumptions

1. **Workspace execution**: Commands execute in agentRoot.path (resolved workspace directory). This follows the OCI pattern where the agent's root is the working directory.

2. **Permission policy mapping**: 
   - approve-all → allow all terminal operations
   - approve-reads → block terminal operations (terminal can write via commands like `rm`, `mv`, etc.)
   - deny-all → block all terminal operations
   
   This differs from fs/* where approve-reads allows read operations. Terminal operations are inherently more powerful and can indirectly modify files.

3. **OutputByteLimit default**: 1MB (1048576 bytes) if not specified. This prevents unbounded memory growth from verbose commands.

4. **TerminalId format**: UUID v4 for uniqueness and collision resistance.

5. **Concurrent terminals**: Multiple terminals can exist per session. TerminalManager tracks them by ID with a map.

6. **Output polling**: ACP uses polling model (TerminalOutput) rather than streaming. Agent calls TerminalOutput repeatedly to check progress.

7. **Cross-platform signals**: Unix-specific (SIGTERM, SIGKILL). Windows would need different implementation (TerminateProcess). Current scope is Unix-only.

8. **Environment merge**: Start with agent process environment, overlay request env. Same pattern as runtime.Manager.Create.

## Risks & Unknowns

### High Risk
- **Output buffer management**: Large outputs can consume memory. OutputByteLimit truncation must be efficient and correct.
- **Signal handling race conditions**: Kill during exit, kill during output read, concurrent kills.

### Medium Risk
- **Concurrent terminal management**: Multiple terminals per session, proper cleanup on session end.
- **Process zombie prevention**: Background goroutine must reap all child processes.
- **Timeout handling**: Long-running commands may block WaitForTerminalExit indefinitely.

### Low Risk
- **ExitCode extraction**: exec.Cmd.Wait returns ProcessState with exit code extraction methods.
- **Output capture**: io.MultiReader on stdout+stderr pipes, buffered reader with limit.

## Integration Points

1. **pkg/runtime/client.go**: acpClient implements acp.Client interface. Add TerminalManager reference.

2. **pkg/runtime/runtime.go**: Manager.Create initializes acpClient. Add TerminalManager initialization.

3. **pkg/spec/types.go**: PermissionPolicy already defined. No changes needed.

4. **pkg/events**: Terminal events could be typed (TerminalCreatedEvent, TerminalOutputEvent, etc.) but not required for Phase 1.1.

## Definition of Done

- [ ] CreateTerminal executes command, returns terminalId
- [ ] TerminalOutput returns captured output with exit status
- [ ] KillTerminalCommand sends SIGTERM/SIGKILL correctly
- [ ] ReleaseTerminal cleans up process and buffers
- [ ] WaitForTerminalExit blocks until completion
- [ ] Permission policy enforced (approve-all/approve-reads/deny-all)
- [ ] OutputByteLimit truncation works correctly
- [ ] Unit tests pass for all terminal methods
- [ ] Integration test: agent-shim → mockagent → terminal command round-trip