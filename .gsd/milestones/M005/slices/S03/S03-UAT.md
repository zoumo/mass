# S03: ARI Agent Surface — Method Migration — UAT

**Milestone:** M005
**Written:** 2026-04-08T18:56:26.785Z

# S03 UAT — ARI Agent Surface Method Migration

## Preconditions

- `agentd` binary built from current source (`go build ./...` exits 0)
- `agentdctl` binary built: `go build -o /tmp/agentdctl ./cmd/agentdctl`
- No running agentd daemon required for static/unit verification tests
- For integration tests: agentd socket available at `$ARI_SOCKET`

---

## 1. Unit — AgentManager lifecycle (pkg/agentd)

**Test:** `TestAgentCreate_RoundTrip`
- Precondition: in-memory SQLite with room and workspace fixtures
- Steps: Call `AgentManager.Create(ctx, room, name, ...)` → call `AgentManager.Get(ctx, id)`
- Expected: returned agent matches all input fields; state = `created`

**Test:** `TestAgentGetByRoomName`
- Steps: Create agent → call `GetByRoomName(ctx, room, name)`
- Expected: returns the same agent

**Test:** `TestAgentList_StateFilter`
- Steps: Create two agents with different states → list with state filter
- Expected: only matching-state agents returned

**Test:** `TestAgentList_RoomFilter`
- Steps: Create agents in two rooms → list with room filter
- Expected: only agents in the specified room returned

**Test:** `TestAgentUpdateState`
- Steps: Create agent → UpdateState to `running` → Get
- Expected: agent.State = `running`

**Test:** `TestAgentDelete_RequiresStopped` (happy path)
- Steps: Create agent → UpdateState to `stopped` → Delete
- Expected: Delete returns nil; subsequent Get returns nil

**Test:** `TestAgentDelete_Protected` (guard)
- Steps: Create agent (state = `created`) → Delete immediately
- Expected: Delete returns `ErrDeleteNotStopped`

**Test:** `TestAgentGet_NotFound`
- Steps: Get with a non-existent ID
- Expected: returns nil, nil (not an error)

**Test:** `TestAgentDelete_NotFound`
- Steps: Delete with a non-existent ID
- Expected: returns `ErrAgentNotFound`

**Run:** `go test ./pkg/agentd/... -count=1 -timeout 60s -run TestAgent`
**Expected:** PASS, 9 tests

---

## 2. Integration — agent/* ARI handlers (pkg/ari)

**Test:** `TestARIAgentLifecycle`
- Steps: create room → prepare workspace → `agent/create` → `agent/list` (verify present) → `agent/status` (state=created) → `agent/stop` → `agent/delete`
- Expected: full lifecycle completes without error; agent absent from list after delete

**Test:** `TestARIAgentCreateAndList`
- Steps: create room → prepare workspace → `agent/create` with name "bot1" → `agent/list` with room filter
- Expected: returned list contains "bot1" with correct room, state=created

**Test:** `TestARIAgentCreateDuplicateName`
- Steps: `agent/create` with room+name "room1/bot" → `agent/create` again with same room+name
- Expected: second call returns error (ErrAgentAlreadyExists or equivalent)

**Test:** `TestARIAgentCreateMissingRoom`
- Steps: `agent/create` with non-existent room name
- Expected: returns error (room not found)

**Test:** `TestARIAgentStatus`
- Steps: create → `agent/status`
- Expected: Agent.State = "created", ShimState = nil (no shim running)

**Test:** `TestARIAgentDeleteRequiresStopped`
- Steps: create agent → immediately call `agent/delete`
- Expected: returns error (agent not stopped)

**Test:** `TestARIAgentDeleteAfterStop`
- Steps: create → `agent/stop` → `agent/delete`
- Expected: delete succeeds (exit 0)

**Test:** `TestARISessionMethodsRemoved`
- Steps: send JSON-RPC request with method `session/new` to ARI server
- Expected: response contains `code: -32601` (MethodNotFound)

**Test:** `TestARIAgentRestartStub`
- Steps: send `agent/restart` request
- Expected: response is an error with "not implemented" or MethodNotFound

**Run:** `go test ./pkg/ari/... -count=1 -timeout 120s`
**Expected:** PASS, 64 tests, 0 failures

---

## 3. Integration — room/send uses agents table

**Test:** `TestARIRoomSend` (migrated)
- Steps: create room → prepare workspace → `agent/create` name="bot" → shim harness wires agent as room member → `room/send` with targetAgent="bot"
- Expected: message delivered via agents table lookup (not sessions RoomAgent filter)

**Test:** `TestARIMultiAgentRoundTrip` (migrated)
- Steps: create room with 3 agents via `agent/create` → send messages between A↔B, A→C
- Expected: all messages delivered; state transitions correct

**Run:** `go test ./pkg/ari/... -count=1 -timeout 120s -run TestARIRoom`
**Expected:** all room tests pass

---

## 4. CLI — agentdctl agent/* surface

**Test:** agent --help shows all subcommands
```
/tmp/agentdctl agent --help
```
Expected: output contains create, list, status, prompt, stop, delete, attach, cancel

**Test:** session/* not in root help
```
! /tmp/agentdctl --help 2>&1 | grep -q 'session'
```
Expected: exit 0 (grep finds nothing)

**Test:** agent create --help shows correct flags
```
/tmp/agentdctl agent create --help
```
Expected: `--room`, `--name`, `--workspace-id`, `--runtime-class`, `--description`, `--system-prompt` flags present

**Test:** agent list --help shows correct flags
```
/tmp/agentdctl agent list --help
```
Expected: `--room`, `--state` flags present

---

## 5. Dispatch table audit (static)

```bash
# Exactly 10 agent/* dispatch cases
grep -c '"agent/' pkg/ari/server.go
# Expected output: 10

# session/* completely removed from dispatch
grep -q '"session/new"' pkg/ari/server.go && echo FAIL || echo PASS
# Expected: PASS
```

---

## 6. Edge cases

**agent/delete ordering (ON DELETE SET NULL):**
- Steps: agent/delete on an agent that has a linked session
- Expected: session is cleaned up correctly (not orphaned) because server pre-fetches sessionId before agents.Delete

**room/delete with stopped agents:**
- Steps: create room → create agent → stop agent → room/delete
- Expected: room deleted without FK violation (auto-delete stopped agents implemented in handleRoomDelete)

**room/delete with running agents:**
- Steps: create room → create agent → room/delete without stopping
- Expected: error (RESTRICT FK protects running agents from accidental room deletion)
