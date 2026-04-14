# S05: Phase 4: Implementation Migration — UAT

**Milestone:** M012
**Written:** 2026-04-14T03:13:08.737Z

## UAT: S05 — Phase 4: Implementation Migration

### Preconditions
- Repository is at M012/S05 HEAD
- `make build` produces `bin/agentd` and `bin/agentdctl`
- Go toolchain available

---

### TC-01: Shim server package builds and satisfies ShimService interface

**Steps:**
1. `go build ./pkg/shim/server/...`
2. `go vet ./pkg/shim/server/...`

**Expected:** Exit 0, no output. Interface compliance is proved transitively by `apishim.RegisterShimService(srv, svc)` call in `cmd/agentd/subcommands/shim/command.go` — compiler rejects mismatches.

---

### TC-02: Shim client package builds and exposes Dial/DialWithHandler/ParseShimEvent

**Steps:**
1. `go build ./pkg/shim/client/...`
2. `grep -n "func Dial\|func DialWithHandler\|func ParseShimEvent" pkg/shim/client/client.go`

**Expected:** Build exits 0. Three public functions are present: `Dial`, `DialWithHandler`, `ParseShimEvent`.

---

### TC-03: ARI server package builds with adapter pattern

**Steps:**
1. `go build ./pkg/ari/server/...`
2. `go vet ./pkg/ari/server/...`
3. `grep -n "workspaceAdapter\|agentRunAdapter\|agentAdapter\|func Register" pkg/ari/server/server.go`

**Expected:** Build exits 0. Four symbols are present: three unexported adapter structs + the `Register` package-level function.

---

### TC-04: ARI client package builds and exposes ARIClient

**Steps:**
1. `go build ./pkg/ari/client/...`
2. `grep -n "type ARIClient\|func Dial\|func.*Close\|func.*DisconnectNotify" pkg/ari/client/client.go`

**Expected:** Build exits 0. `ARIClient` struct with `Workspace`, `AgentRun`, `Agent` fields, plus `Dial`, `Close`, `DisconnectNotify` are present.

---

### TC-05: cmd/agentd/subcommands/server uses pkg/ari/server + explicit listener

**Steps:**
1. `grep -n "ariserver\|jsonrpc.NewServer\|net.Listen\|srv.Serve\|srv.Shutdown" cmd/agentd/subcommands/server/command.go`

**Expected:** All five patterns are present — confirming the old monolithic `ari.New(...)` pattern has been replaced with the explicit listener + typed service pattern.

---

### TC-06: cmd/agentd/subcommands/shim uses pkg/shim/server + explicit listener

**Steps:**
1. `grep -n "shimserver\|apishim.RegisterShimService\|net.Listen\|srv.Serve" cmd/agentd/subcommands/shim/command.go`

**Expected:** All four patterns present — confirming sourcegraph/jsonrpc2 direct usage replaced with shimserver + pkg/jsonrpc pattern.

---

### TC-07: pkg/agentd/process.go uses shimclient.DialWithHandler (not internal Dial)

**Steps:**
1. `grep -n "shimclient.DialWithHandler\|shimclient.NotificationHandler" pkg/agentd/process.go`

**Expected:** Both calls present. `shimclient.DialWithHandler` is the production path; `NotificationHandler` cast ensures type safety if the handler type diverges in future.

---

### TC-08: pkg/agentd/recovery.go uses struct-based Subscribe params

**Steps:**
1. `grep -n "apishim.SessionSubscribeParams\|FromSeq:" pkg/agentd/recovery.go`

**Expected:** `apishim.SessionSubscribeParams{FromSeq: &fromSeq}` pattern present — confirms the migration from positional params to typed struct params.

---

### TC-09: make build produces both binaries

**Steps:**
1. `make build`
2. `ls -la bin/agentd bin/agentdctl`

**Expected:** Exit 0. Both binaries present, recent modification time.

---

### TC-10: Full test suite passes (make build + go test ./...)

**Steps:**
1. `make build && go test ./... -count=1 2>&1 | grep -E "^(ok|FAIL)"`

**Expected:** All packages show `ok`. No `FAIL` lines. Integration tests (tests/integration) pass.

---

### TC-11: WorkspaceService.List / AgentService.List conflict is resolved at compile time

**Steps:**
1. `go build ./pkg/ari/server/...` — must not fail with "ambiguous selector" or "cannot implement interface"
2. Confirm the compiler accepts both `workspaceAdapter` implementing `WorkspaceService` and `agentAdapter` implementing `AgentService` without conflict.

**Expected:** Build exits 0. The adapter pattern resolves the identical-signature conflict structurally.

---

### Edge Cases

**EC-01: pkg/shim/client Dial fails on missing socket**
- Call `shimclient.Dial(ctx, "/tmp/nonexistent.sock")` — expect a dial error, not a panic.

**EC-02: pkg/ari/client Dial fails on missing socket**
- Call `ariclient.Dial(ctx, "/tmp/nonexistent.sock")` — expect a dial error, not a panic.

**EC-03: ARIClient.Close() is idempotent**
- Call `client.Close()` twice — second call should return an error, not panic.

