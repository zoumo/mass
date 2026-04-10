# S04: CLI + workspace-mcp-server + Design Docs — UAT

**Milestone:** M007
**Written:** 2026-04-09T22:05:21.605Z

## S04 UAT: CLI + workspace-mcp-server + Design Docs

### Preconditions
- Go toolchain installed and `go build` available
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- All previous slices (S01–S03) complete; `go build ./...` was green before S04

---

### TC-01: workspace-mcp-server binary builds successfully

**Steps:**
1. Run `go build ./cmd/workspace-mcp-server/...`

**Expected:** Exit code 0, no errors, binary produced.

---

### TC-02: workspace-mcp-server uses correct env var and tool names

**Steps:**
1. Inspect `cmd/workspace-mcp-server/main.go`
2. Confirm: `OAR_WORKSPACE_NAME` (not `OAR_ROOM_NAME`)
3. Confirm: tool names `workspace_send`, `workspace_status`
4. Confirm: ARI calls `workspace/send`, `workspace/status`
5. Confirm: log prefix `workspace-mcp-server:` and log file `workspace-mcp-server.log`

**Expected:** All five items confirmed, no `OAR_ROOM_NAME` or `room_` strings present.

---

### TC-03: workspace-mcp-server logs startup fields

**Steps:**
1. Inspect startup log block in `cmd/workspace-mcp-server/main.go`
2. Confirm `workspace=`, `agentName=`, `agentID=` fields are logged on startup

**Expected:** All three fields present in log output, matching room-mcp-server predecessor pattern.

---

### TC-04: agentdctl workspace subcommands include send

**Steps:**
1. Run `go run ./cmd/agentdctl/ workspace --help`
2. Observe the subcommand list

**Expected:** Output contains `create`, `delete`, `list`, and `send` subcommands.

---

### TC-05: agentdctl workspace send flags

**Steps:**
1. Run `go run ./cmd/agentdctl/ workspace send --help`

**Expected:** Help output shows `--workspace` (required), `--from` (required), `--to` (required), `--text` (required) flags.

---

### TC-06: agentdctl room command no longer exists

**Steps:**
1. Run `go run ./cmd/agentdctl/ room --help`

**Expected:** Exit code 1 with message `unknown command "room" for "agentdctl"`.

---

### TC-07: agentdctl agent create flags

**Steps:**
1. Run `go run ./cmd/agentdctl/ agent create --help`

**Expected:** Flags include `--workspace` and `--name` (not `--agent-id`).

---

### TC-08: Full build clean — no room-mcp-server directory

**Steps:**
1. Run `go build ./...`
2. Run `ls cmd/` and confirm `room-mcp-server` directory is absent

**Expected:** Build exits 0; `cmd/room-mcp-server/` does not exist.

---

### TC-09: No stale Room references in cmd/

**Steps:**
1. Run `grep -rn 'room-mcp-server\|Room\|roomCmd' cmd/`

**Expected:** No matches (exit code 1 from grep).

---

### TC-10: ari-spec.md contains workspace and agent methods

**Steps:**
1. Run `grep -c 'workspace/create\|workspace/send\|workspace/status\|workspace/list\|workspace/delete' docs/design/agentd/ari-spec.md`
2. Run `grep -c 'agent/create\|agent/prompt\|agent/stop\|agent/delete\|agent/status' docs/design/agentd/ari-spec.md`

**Expected:** Both greps return counts ≥ 3 (each method name appears at least once in the doc).

---

### TC-11: ari-spec.md contains no Room methods, agentId, or session/* in contract section

**Steps:**
1. Run `grep -n 'room/create\|room/delete\|room/status\|room/send' docs/design/agentd/ari-spec.md`
2. Run `grep -n 'agentId' docs/design/agentd/ari-spec.md`

**Expected:** Both greps return no matches (exit code 1).

---

### TC-12: ari-spec.md documents async polling pattern

**Steps:**
1. Run `grep -q 'poll\|polling\|workspace/status' docs/design/agentd/ari-spec.md && echo PASS`

**Expected:** Output `PASS` — async polling instructions present.

---

### TC-13: ari-spec.md state values are correct (idle not created)

**Steps:**
1. Run `grep -n '"idle"\|"creating"\|"running"\|"stopped"\|"error"' docs/design/agentd/ari-spec.md`
2. Run `grep -n '"created"' docs/design/agentd/ari-spec.md`

**Expected:** First grep has matches; second grep has NO matches (StatusCreated renamed to StatusIdle).

---

### TC-14: agentd.md uses workspace+name identity

**Steps:**
1. Run `grep -c 'workspace.*name\|name.*workspace' docs/design/agentd/agentd.md`

**Expected:** Count ≥ 1.

---

### TC-15: agentd.md has no Session Manager subsystem section

**Steps:**
1. Run `grep -n 'Session Manager' docs/design/agentd/agentd.md`

**Expected:** No matches (exit code 1).

---

### Edge Cases

**EC-01: workspace-mcp-server fails fast when OAR_WORKSPACE_NAME is unset**
- Run the binary without setting OAR_WORKSPACE_NAME
- Expected: binary exits with an error message referencing `OAR_WORKSPACE_NAME`

**EC-02: agentdctl workspace send requires all four flags**
- Run `go run ./cmd/agentdctl/ workspace send --workspace w --from a` (omit --to and --text)
- Expected: cobra validation error listing missing required flags, exit code 1

