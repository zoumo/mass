# S06: ARI Service — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-06T15:52:42.721Z

# S06 UAT: ARI Service

## Preconditions
1. agentd daemon built and available at `./bin/agentd`
2. agent-shim built and available at `./bin/agent-shim`
3. Mockagent runtime class configured
4. Working directory is project root

## Test Case 1: Session Lifecycle - Create, Prompt, Stop, Remove

### Steps
1. Start agentd daemon with test configuration:
   ```bash
   ./bin/agentd --config test-config.yaml &
   sleep 2
   ```

2. Create a workspace:
   ```bash
   # Using nc to send JSON-RPC request
   echo '{"jsonrpc":"2.0","id":1,"method":"workspace/prepare","params":{"spec":{"oarVersion":"0.1.0","metadata":{"name":"test-ws"},"source":{"type":"emptyDir"}}}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Response with `workspaceId` and `status: "ready"`

3. Create a session:
   ```bash
   echo '{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"workspaceId":"<WORKSPACE_ID>","runtimeClass":"mockagent"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Response with `sessionId` and `state: "created"`

4. Prompt the session (auto-start):
   ```bash
   echo '{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"<SESSION_ID>","text":"hello"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Response with `stopReason: "end_turn"` and session transitions to "running"

5. Check session status:
   ```bash
   echo '{"jsonrpc":"2.0","id":4,"method":"session/status","params":{"sessionId":"<SESSION_ID>"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Response with session info including `state: "running"` or `"stopped"` and shimState if running

6. Stop the session:
   ```bash
   echo '{"jsonrpc":"2.0","id":5,"method":"session/stop","params":{"sessionId":"<SESSION_ID>"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Success response (null result)

7. Remove the session:
   ```bash
   echo '{"jsonrpc":"2.0","id":6,"method":"session/remove","params":{"sessionId":"<SESSION_ID>"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Success response (null result)

8. Cleanup workspace:
   ```bash
   echo '{"jsonrpc":"2.0","id":7,"method":"workspace/cleanup","params":{"workspaceId":"<WORKSPACE_ID>"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Success response

## Test Case 2: Error Handling - Prompt on Stopped Session

### Steps
1. Create workspace and session (steps 1-3 from Test Case 1)
2. Stop the session immediately:
   ```bash
   echo '{"jsonrpc":"2.0","id":5,"method":"session/stop","params":{"sessionId":"<SESSION_ID>"}}' | nc -U /tmp/agentd.sock
   ```
3. Attempt to prompt stopped session:
   ```bash
   echo '{"jsonrpc":"2.0","id":6,"method":"session/prompt","params":{"sessionId":"<SESSION_ID>","text":"test"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Error response with `code: -32602` (InvalidParams) and message containing "not running"

## Test Case 3: Error Handling - Remove Running Session

### Steps
1. Create workspace, session, and prompt (auto-start) - session should be running
2. Attempt to remove running session:
   ```bash
   echo '{"jsonrpc":"2.0","id":5,"method":"session/remove","params":{"sessionId":"<SESSION_ID>"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Error response with `code: -32602` (InvalidParams) and message about "running" or "protected"

## Test Case 4: Session List

### Steps
1. Create multiple sessions (2-3)
2. List all sessions:
   ```bash
   echo '{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Response with `sessions` array containing all created sessions

## Test Case 5: Session Not Found

### Steps
1. Try to get status of non-existent session:
   ```bash
   echo '{"jsonrpc":"2.0","id":1,"method":"session/status","params":{"sessionId":"nonexistent-uuid"}}' | nc -U /tmp/agentd.sock
   ```
   **Expected:** Error response with `code: -32602` (InvalidParams) and message containing "not found"

## Postconditions
- All sessions cleaned up (stopped and removed)
- All workspaces cleaned up
- agentd daemon stopped

## Notes
- Mockagent returns `stopReason: "end_turn"` after each prompt
- Auto-start happens automatically when prompting a session in "created" state
- Session state transitions: created → running → stopped
