# M001-terminal: Phase 1.1 — Terminal Operations

## Vision

From "stub responses" to "full terminal execution" — agent-shim can execute shell commands in the agent's workspace, capture output, and control command lifecycle through ACP protocol methods.

## Slice Overview

| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | TerminalManager + CreateTerminal | medium | — | pending | Can execute commands in agent workspace, returns terminalId |
| S02 | TerminalOutput | low | S01 | pending | Can retrieve captured stdout/stderr with exit status |
| S03 | KillTerminalCommand | medium | S01 | pending | Can stop running commands with SIGTERM/SIGKILL |
| S04 | ReleaseTerminal | low | S01 | pending | Can cleanup terminal resources after use |
| S05 | WaitForTerminalExit | low | S01 | pending | Can wait for command completion synchronously |
| S06 | Integration Tests | low | S01-S05 | pending | Full terminal lifecycle works end-to-end, permission policy enforced |

## Slice Details

### S01: TerminalManager + CreateTerminal

**Intent:** Create TerminalManager component and implement CreateTerminal to execute shell commands.

**Demo:** `CreateTerminal(command="echo hello", args=[], cwd=workspace)` → returns `terminalId="abc123"`

**Proof Strategy:**
1. Unit test: CreateTerminal creates process, returns terminalId
2. Unit test: Command executes in correct cwd (workspace)
3. Unit test: Permission policy enforced (approve-all works, deny-all blocked)
4. Unit test: OutputByteLimit defaults to 1MB

**Verification Classes:**
- Process execution: cmd.Start succeeds, process running
- Working directory: cmd.Dir matches resolved agentRoot
- Permission check: correct error for blocked policies
- TerminalId: UUID format, unique per call

**Key Files:**
- pkg/runtime/terminal.go (new): TerminalManager struct, Create method
- pkg/runtime/client.go: acpClient.CreateTerminal implementation
- pkg/runtime/runtime.go: Initialize TerminalManager in Manager

### S02: TerminalOutput

**Intent:** Implement TerminalOutput to retrieve captured command output.

**Demo:** `TerminalOutput(terminalId="abc123")` → returns `output="hello\n", truncated=false, exitStatus={exitCode:0}`

**Proof Strategy:**
1. Unit test: TerminalOutput returns captured stdout+stderr
2. Unit test: ExitStatus populated after command completes
3. Unit test: OutputByteLimit truncation works (truncate from beginning)
4. Unit test: Truncated flag set when limit exceeded

**Verification Classes:**
- Output capture: stdout+stderr merged into single string
- ExitStatus: exitCode or signal populated correctly
- Truncation: correct UTF-8 boundary truncation
- Truncated flag: accurate when limit exceeded

**Key Files:**
- pkg/runtime/terminal.go: Output method
- pkg/runtime/client.go: acpClient.TerminalOutput implementation

### S03: KillTerminalCommand

**Intent:** Implement KillTerminalCommand to stop running commands.

**Demo:** `KillTerminalCommand(terminalId="abc123")` → process receives SIGTERM, then SIGKILL if not dead after 5s

**Proof Strategy:**
1. Unit test: KillTerminalCommand sends SIGTERM
2. Unit test: SIGKILL sent after timeout if process survives
3. Unit test: Error if terminal already exited
4. Unit test: Output capture continues until process dead

**Verification Classes:**
- Signal delivery: SIGTERM sent to process
- Timeout handling: SIGKILL after grace period
- State tracking: terminal marked as killed
- Output finalization: remaining output captured

**Key Files:**
- pkg/runtime/terminal.go: Kill method
- pkg/runtime/client.go: acpClient.KillTerminalCommand implementation

### S04: ReleaseTerminal

**Intent:** Implement ReleaseTerminal to cleanup terminal resources.

**Demo:** `ReleaseTerminal(terminalId="abc123")` → terminal removed from manager, buffers freed

**Proof Strategy:**
1. Unit test: ReleaseTerminal removes terminal from map
2. Unit test: Output buffers freed
3. Unit test: Process cleaned up if still running
4. Unit test: Error if terminal not found

**Verification Classes:**
- Resource cleanup: terminal removed from TerminalManager
- Buffer freed: output buffer memory released
- Process cleanup: process killed if still running
- Map cleanup: terminalId no longer in active terminals

**Key Files:**
- pkg/runtime/terminal.go: Release method
- pkg/runtime/client.go: acpClient.ReleaseTerminal implementation

### S05: WaitForTerminalExit

**Intent:** Implement WaitForTerminalExit to block until command completes.

**Demo:** `WaitForTerminalExit(terminalId="abc123")` → blocks until process exits, returns exitStatus

**Proof Strategy:**
1. Unit test: WaitForTerminalExit blocks until process exits
2. Unit test: Returns exitStatus correctly
3. Unit test: Returns immediately if already exited
4. Unit test: Context cancellation works

**Verification Classes:**
- Blocking behavior: waits until process exit
- ExitStatus: correct exitCode/signal returned
- Already-exited case: returns immediately
- Context cancellation: returns on ctx.Done()

**Key Files:**
- pkg/runtime/terminal.go: WaitForExit method
- pkg/runtime/client.go: acpClient.WaitForTerminalExit implementation

### S06: Integration Tests

**Intent:** End-to-end integration tests for terminal lifecycle.

**Demo:** agent-shim starts → agent requests terminal → full lifecycle works → cleanup succeeds

**Proof Strategy:**
1. Integration test: CreateTerminal → TerminalOutput → ReleaseTerminal round-trip
2. Integration test: KillTerminalCommand on long-running command
3. Integration test: Permission policy blocking (approve-reads, deny-all)
4. Integration test: OutputByteLimit truncation
5. Integration test: Concurrent terminals

**Verification Classes:**
- End-to-end: full terminal lifecycle via ACP
- Permission enforcement: policy blocks correctly
- Concurrent terminals: multiple terminals per session work
- Cleanup: no resource leaks after session end

**Key Files:**
- pkg/runtime/terminal_test.go (new): integration tests
- tests/integration/terminal_test.go (new): e2e tests

## Definition of Done

- [ ] CreateTerminal executes command, returns terminalId
- [ ] TerminalOutput returns captured output with exit status
- [ ] KillTerminalCommand sends SIGTERM/SIGKILL correctly
- [ ] ReleaseTerminal cleans up process and buffers
- [ ] WaitForTerminalExit blocks until completion
- [ ] Permission policy enforced (approve-all/approve-reads/deny-all)
- [ ] OutputByteLimit truncation works correctly (UTF-8 boundary)
- [ ] Unit tests pass for all terminal methods
- [ ] Integration test: full terminal lifecycle works
- [ ] Integration test: permission policy blocking works

## Requirement Coverage

| Requirement | Primary Slice | Supporting |
|-------------|---------------|------------|
| R020: Terminal command execution | S01 | S06 |
| R026: Terminal output retrieval | S02 | S06 |
| R027: Terminal command control | S03 | S06 |
| R028: Terminal resource cleanup | S04 | S06 |
| R029: Terminal exit waiting | S05 | S06 |