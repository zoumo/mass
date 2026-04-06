# S07: agentdctl CLI — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-06T16:31:20.460Z

# S07 UAT: agentdctl CLI

## Preconditions

1. agentd daemon running on ARI socket (default: /var/run/agentd/ari.sock)
2. mockagent RuntimeClass configured in agentd config.yaml
3. At least one workspace prepared (for session creation tests)
4. agentdctl binary built and available in PATH or working directory

## Test Cases

### TC1: CLI Help and Structure

**Purpose:** Verify CLI structure and command hierarchy

**Steps:**
1. Run `agentdctl --help`
   - Expected: Shows usage, available commands (session, workspace, daemon, completion, help)
   - Expected: Shows --socket persistent flag with default /var/run/agentd/ari.sock

2. Run `agentdctl session --help`
   - Expected: Shows 7 subcommands: attach, list, new, prompt, remove, status, stop

3. Run `agentdctl workspace --help`
   - Expected: Shows 3 subcommands: cleanup, list, prepare

4. Run `agentdctl daemon --help`
   - Expected: Shows status subcommand

### TC2: Daemon Health Check

**Purpose:** Verify daemon status command works

**Steps:**
1. Run `agentdctl daemon status` (daemon running)
   - Expected: Output "daemon: running"

2. Run `agentdctl --socket /nonexistent/path daemon status`
   - Expected: Output "daemon: not running"
   - Expected: Error message to stderr about connection failure

### TC3: Workspace Operations

**Purpose:** Verify workspace prepare/list/cleanup commands

**Steps:**
1. Run `agentdctl workspace prepare --name test-ws --type emptyDir`
   - Expected: Pretty-printed JSON with workspace_id field
   - Expected: workspace_id is valid UUID

2. Run `agentdctl workspace list`
   - Expected: Pretty-printed JSON array containing test-ws

3. Run `agentdctl workspace cleanup <workspace-id-from-step-1>`
   - Expected: Output "Workspace <id> cleaned up"

4. Run `agentdctl workspace prepare --name git-ws --type git --url https://github.com/octocat/Hello-World.git --depth 1`
   - Expected: Pretty-printed JSON with workspace_id
   - Expected: Git repository cloned in workspace directory

### TC4: Session Lifecycle

**Purpose:** Verify full session lifecycle: create → prompt → stop → remove

**Precondition:** Workspace prepared from TC3 step 1

**Steps:**
1. Run `agentdctl session new --workspace-id <ws-id> --runtime-class mockagent`
   - Expected: Pretty-printed JSON with session_id
   - Expected: session_id is valid UUID

2. Run `agentdctl session list`
   - Expected: Pretty-printed JSON array containing new session

3. Run `agentdctl session status <session-id>`
   - Expected: Pretty-printed JSON with session state (Running)

4. Run `agentdctl session prompt <session-id> --text "hello"`
   - Expected: Pretty-printed JSON with prompt result

5. Run `agentdctl session stop <session-id>`
   - Expected: Output "Session <id> stopped"

6. Run `agentdctl session remove <session-id>`
   - Expected: Output "Session <id> removed"

7. Run `agentdctl session list`
   - Expected: Empty array (session removed)

### TC5: Session Attach

**Purpose:** Verify session attach returns shim socket path

**Precondition:** Running session from TC4

**Steps:**
1. Run `agentdctl session new --workspace-id <ws-id> --runtime-class mockagent`
2. Run `agentdctl session attach <session-id>`
   - Expected: Pretty-printed JSON with shim_socket_path field

### TC6: Error Handling

**Purpose:** Verify error handling for invalid inputs

**Steps:**
1. Run `agentdctl session new` (missing required flags)
   - Expected: Cobra error "required flag(s) \"workspace-id\", \"runtime-class\" not set"
   - Expected: Exit code 1

2. Run `agentdctl session status nonexistent-session-id`
   - Expected: ARI error message to stderr
   - Expected: Exit code 1

3. Run `agentdctl session prompt stopped-session-id --text "test"`
   - Expected: ARI InvalidParams error (session not running)
   - Expected: Exit code 1

4. Run `agentdctl workspace cleanup nonexistent-ws-id`
   - Expected: ARI error message to stderr
   - Expected: Exit code 1

### TC7: Labels Parsing

**Purpose:** Verify labels flag parsing works

**Steps:**
1. Run `agentdctl session new --workspace-id <ws-id> --runtime-class mockagent --labels "env=dev,team=infra"`
   - Expected: Session created successfully
   - Expected: Labels visible in session status output

## Edge Cases

1. **Empty labels string:** --labels "" should create session without labels
2. **Malformed labels:** --labels "invalid" should silently skip malformed pair
3. **Socket path override:** --socket /custom/path should use custom path
4. **Workspace prepare validation:** --type git without --url should return validation error before connecting

## Success Criteria

- All 11 subcommands execute without crashes
- JSON output is valid and pretty-printed
- Error handling produces stderr output and exit code 1
- Full session lifecycle works: new → prompt → stop → remove
- Workspace lifecycle works: prepare → list → cleanup
