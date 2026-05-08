# Code Reuse & Simplification Review Report

Date: 2026-04-30

## Summary

The MASS codebase (~20k non-test lines across ~130 Go files) is well-structured overall, with clean package boundaries and a consistent K8s-style architecture. The main opportunities for improvement are:

1. **Repeated CLI command patterns** (get/list/delete across agent, agentrun, workspace) that could be generalized
2. **Duplicated polling/wait logic** in CLI utilities
3. **Two `Service` types** with identical names in different packages causing confusion
4. **A dead `RawClient` escape hatch** that duplicates the typed Client
5. **Redundant `GetByWorkspaceName` method** that is a pure alias
6. **Repeated `ObjectKey` validation patterns** in server handlers
7. **`splitN` and `isAbs` reimplementations** in workspace/spec.go

Impact is mostly Medium/Low -- the code is reasonably well-factored already.

---

## Duplicated Code

### D1: CLI "get" commands are near-identical across resources (High)

**Files:**
- `cmd/massctl/commands/agent/get.go`
- `cmd/massctl/commands/agentrun/get.go`
- `cmd/massctl/commands/workspace/get.go`

**Description:** Each `get` command implements the same pattern:
1. If no args, list all resources
2. If args provided, get each by name, collect into a list
3. Convert `[]TypedItem` to `[]any`
4. Call `printer.Print` for single items, `printer.PrintList` for multiple

The only differences are: the type (Agent/AgentRun/Workspace), the ObjectKey construction, and additional filter flags.

**Suggestion:** Extract a generic `GetOrList` helper:

```go
// pkg/cmd/cliutil/getlist.go
type GetListConfig[T any, L any] struct {
    ListFn   func(ctx context.Context) ([]T, L, error)
    GetFn    func(ctx context.Context, name string) (T, error)
    ToAny    func([]T) []any
    ListObj  func([]T) any
}

func RunGetOrList[T any](ctx context.Context, cmd *cobra.Command, printer *ResourcePrinter, args []string, cfg GetListConfig[T, any]) error {
    // ... unified logic
}
```

---

### D2: CLI "delete" commands follow identical loop pattern (Medium)

**Files:**
- `cmd/massctl/commands/agent/delete.go`
- `cmd/massctl/commands/agentrun/delete.go`
- `cmd/massctl/commands/workspace/delete.go`

**Description:** Each iterates args, calls `client.Delete(ctx, key, &TypeMarker{})`, prints confirmation. The only differences are the ObjectKey construction and the type marker.

**Suggestion:** Extract a `RunBulkDelete` helper in `cliutil`:

```go
func RunBulkDelete(ctx context.Context, cmd *cobra.Command, client Client, args []string, buildKey func(string) ObjectKey, marker Object, label string) error {
    for _, name := range args {
        if err := client.Delete(ctx, buildKey(name), marker); err != nil {
            return fmt.Errorf("deleting %s %q: %w", label, name, err)
        }
        fmt.Fprintf(cmd.OutOrStdout(), "%s %q deleted\n", label, name)
    }
    return nil
}
```

---

### D3: Repeated `[]TypedItem` -> `[]any` conversion (Medium)

**Files:**
- `cmd/massctl/commands/agent/get.go` (lines 53-55, 68-70)
- `cmd/massctl/commands/agentrun/get.go` (lines 68-70, 85-87)
- `cmd/massctl/commands/workspace/get.go` (lines 53-55, 68-70)

**Description:** Every get/list command manually converts typed slices to `[]any`:
```go
items := make([]any, len(list.Items))
for i := range list.Items {
    items[i] = list.Items[i]
}
```

**Suggestion:** Add a generic helper in `cliutil`:

```go
func ToAnySlice[T any](items []T) []any {
    out := make([]any, len(items))
    for i := range items {
        out[i] = items[i]
    }
    return out
}
```

---

### D4: Duplicated polling/wait logic (Medium)

**Files:**
- `cmd/massctl/commands/cliutil/wait.go` (`WaitAgentIdle`)
- `cmd/massctl/commands/cliutil/workspace.go` (`WaitWorkspaceReady`)
- `cmd/massctl/commands/workspace/delete.go` (`waitForExited`)

**Description:** Three separate poll loops, each with `time.Sleep(500ms)` + `client.Get()` + phase-check switch. They differ in the target type and terminal conditions but share the same structure.

**Suggestion:** Extract a generic poller:

```go
func PollUntil[T any](ctx context.Context, interval, timeout time.Duration, get func() (T, error), done func(T) (bool, error)) error {
    deadline := time.After(timeout)
    tick := time.NewTicker(interval)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-deadline:
            return fmt.Errorf("timed out")
        case <-tick.C:
            obj, err := get()
            if err != nil { return err }
            ok, err := done(obj)
            if err != nil { return err }
            if ok { return nil }
        }
    }
}
```

---

### D5: ARI server handler ObjectKey validation boilerplate (Medium)

**Files:**
- `pkg/ari/server/service.go` (lines 64-89, 104-161, 166-196)

**Description:** Multiple handlers repeat the same pattern of: unmarshal ObjectKey, validate required fields, call service method. The "get", "delete", "cancel", "stop", "restart" handlers for both workspace and agentrun are nearly identical in structure.

**Suggestion:** Extract a `keyMethod` helper that handles unmarshal + validation:

```go
func keyMethod(required []string, fn func(ctx context.Context, key ObjectKey) (any, error)) jsonrpc.Method {
    return func(ctx context.Context, unmarshal func(any) error) (any, error) {
        var key ObjectKey
        if err := unmarshal(&key); err != nil {
            return nil, jsonrpc.ErrInvalidParams(err.Error())
        }
        for _, f := range required {
            switch f {
            case "workspace":
                if key.Workspace == "" { return nil, jsonrpc.ErrInvalidParams("workspace is required") }
            case "name":
                if key.Name == "" { return nil, jsonrpc.ErrInvalidParams("name is required") }
            }
        }
        return fn(ctx, key)
    }
}
```

---

### D6: Version command duplication (Low)

**Files:**
- `cmd/mass/commands/version/command.go`
- `cmd/massctl/commands/version/command.go`

**Description:** Both implement `printText()` and `printJSON()` for client-side version info. The massctl version adds daemon version querying, but the client-side portion is duplicated.

**Suggestion:** Extract shared version formatting into `internal/version`:

```go
func PrintText(w io.Writer) {
    fmt.Fprintln(w, "version:    ", Version)
    fmt.Fprintln(w, "git commit: ", GitCommit)
    // ...
}
```

---

## Simplification Opportunities

### S1: `splitN` and `isAbs` reimplement stdlib (Low)

**File:** `pkg/workspace/spec.go` (lines 329-358)

**Description:** `splitN` reimplements `strings.SplitN` and `isAbs` reimplements `filepath.IsAbs`. Comments say "to avoid importing strings/filepath in validation code" but both packages are zero-cost standard library imports with no external dependencies.

**Suggestion:** Replace with:
```go
import (
    "path/filepath"
    "strings"
)
// Then use strings.SplitN and filepath.IsAbs directly.
```

---

### S2: `OutputJSON` is deprecated but still exported (Low)

**File:** `cmd/massctl/commands/cliutil/cliutil.go` (line 19)

**Description:** `OutputJSON` is marked `// Deprecated: prefer PrintJSON` but remains exported. If no callers remain, remove it. If callers still exist, migrate them.

**Suggestion:** Search for usages; if none, remove. If some remain, replace with `PrintJSON(os.Stdout, result)`.

---

### S3: `HandleError` uses `os.Exit` (Low)

**File:** `cmd/massctl/commands/cliutil/cliutil.go` (line 36)

**Description:** `HandleError` calls `os.Exit(1)` which makes the function untestable. All cobra commands already return errors via `RunE` — `HandleError` may be unnecessary.

**Suggestion:** Verify no callers use `HandleError` in `RunE` flows (which handle errors via cobra). Remove if unused.

---

### S4: `AgentRunManager.GetByWorkspaceName` is a pure alias (Low)

**File:** `pkg/agentd/agent.go` (line 116)

**Description:** `GetByWorkspaceName` is documented as "Alias for Get" with identical parameters and implementation. It adds no value and creates maintenance burden.

**Suggestion:** Remove `GetByWorkspaceName`, update any callers to use `Get` directly.

---

---

## Over-engineering

### O1: `watch.WatchServer` generic with unbuffered mailbox (Low)

**File:** `pkg/watch/server.go`

**Description:** `WatchServer[T]` uses unbuffered mailbox channels, meaning `Publish` blocks until every watcher has consumed the event. The `publishMu` mutex serializes all publishes globally. This is a correct design for ordered fan-out, but the current codebase appears to only use `RetryWatcher` (client-side) and the `Translator` (server-side fan-out with its own mechanism). `WatchServer` may be unused in production paths.

**Suggestion:** Verify `WatchServer` is actually used in production code. If it's only for tests or unused, consider removing to reduce surface area.

---

### O2: `RawClient` escape hatch has zero callers — dead code (Low)

**File:** `pkg/ari/client/simple.go`

**Description:** `RawClient` provides raw `Call(method, params, result)` access to the ARI socket. Confirmed via grep: zero external callers. This is dead code, now tracked under DC2.

**Suggestion:** Remove `simple.go` entirely.

---

## Dead Code

### DC1: `AgentRunManager.GetByWorkspaceName` — zero callers (Low)

**File:** `pkg/agentd/agent.go` (line 116-118)

**Description:** Pure alias of `Get` with no external callers (confirmed via grep). Dead code from a rename refactor.

**Suggestion:** Remove `GetByWorkspaceName`.

---

### DC2: `ari/client.RawClient` — zero external callers (Low)

**File:** `pkg/ari/client/simple.go`

**Description:** `RawClient` and `NewRawClient` have NO callers outside of `simple.go` itself (confirmed via grep). This was likely an early prototype escape hatch. The typed `Client` interface covers all needed functionality.

**Suggestion:** Remove `RawClient` and `simple.go`.

---

### DC3: `watch.WatchServer[T]` + `watch.Event[T]` — unused in production (Low)

**Files:** `pkg/watch/server.go`, `pkg/watch/event.go`

**Description:** `WatchServer` and `Event[T]` are defined but have NO callers outside their own file and tests (confirmed via grep). The production code uses `RetryWatcher` (client-side) and `Translator` (server-side fan-out). The `WatchServer` generic implementation is dead code.

**Suggestion:** Remove `WatchServer`, `ServerConn`, and `Event[T]` unless they are planned for future use. If keeping for future use, add a `// TODO: planned for X` comment.

---

### DC4: `OutputJSON` — deprecated but still has callers (Low)

**File:** `cmd/massctl/commands/cliutil/cliutil.go` (line 19)

**Description:** Marked deprecated but still used by 3 callers in workspace create commands (`git.go`, `local.go`, `empty.go`). Not fully dead, but should be migrated.

**Suggestion:** Replace the 3 callers with `PrintJSON(cmd.OutOrStdout(), ws)` and then remove `OutputJSON`.

---

## Dependency Analysis

### Cross-package dependency health

The dependency graph is clean and acyclic:

```
cmd/mass → pkg/agentd, pkg/agentrun, pkg/ari, pkg/jsonrpc, pkg/runtime-spec, pkg/workspace
cmd/massctl → pkg/ari/client, pkg/ari/api, pkg/agentrun/api, pkg/workspace, cliutil
pkg/ari/server → pkg/agentd, pkg/agentd/store, pkg/workspace, pkg/agentrun/api, pkg/jsonrpc
pkg/agentd → pkg/agentd/store, pkg/agentrun/client, pkg/ari/api, pkg/runtime-spec, pkg/watch
pkg/agentrun/server → pkg/agentrun/api, pkg/agentrun/runtime/acp, pkg/jsonrpc
pkg/agentrun/client → pkg/agentrun/api, pkg/jsonrpc, pkg/watch
pkg/jsonrpc → (external: sourcegraph/jsonrpc2)
pkg/watch → (no deps)
pkg/workspace → pkg/agentd/store, pkg/ari/api (for InitRefCounts only)
```

**Finding:** `pkg/workspace` imports `pkg/agentd/store` and `pkg/ari/api` solely for `InitRefCounts`. This creates a coupling where a pure workspace-preparation library depends on the daemon's store layer.

**Suggestion (Low priority):** Move `InitRefCounts` to the caller (daemon startup code) rather than putting it on `WorkspaceManager`. Pass ref counts as a map parameter or have the daemon call `Acquire` in a loop.

### Third-party dependencies

| Dependency | Usage | Could replace? |
|---|---|---|
| `sourcegraph/jsonrpc2` | JSON-RPC transport | No — deeply integrated, well-suited |
| `go.etcd.io/bbolt` | Metadata store | No — appropriate for embedded KV |
| `sigs.k8s.io/yaml` | YAML parsing | No — needed for K8s-style YAML |
| `github.com/coder/acp-go-sdk` | ACP protocol | No — required protocol SDK |
| `github.com/spf13/cobra` | CLI framework | No — standard choice |
| `github.com/google/uuid` | UUID generation | Could use `crypto/rand` but uuid is cleaner |
| `github.com/lmittmann/tint` | Colored slog output | Fine — small, focused |
| `gopkg.in/natefinch/lumberjack.v2` | Log rotation | Fine — standard choice |
| `mvdan.cc/sh/v3` | Shell execution (hooks) | Appropriate for shell hook execution |
| `github.com/santhosh-tekuri/jsonschema/v6` | JSON Schema validation | Fine — used in pipeline validation |
| `github.com/hashicorp/golang-lru/v2` | LRU cache | Fine — may be removable if only one usage |

No unnecessary dependencies found. The `third_party/charmbracelet/crush/` vendored code is used by the TUI layer and is appropriate.

---

## Recommendations

### Priority 1 (High impact, moderate effort)

1. **Extract `ToAnySlice[T]` helper** — eliminates the most-repeated 4-line pattern across 6+ locations. Tiny change, immediate readability gain.

2. **Extract `PollUntil` generic poller** — unifies 3 poll loops (WaitAgentIdle, WaitWorkspaceReady, waitForExited) into one testable utility with configurable timeout and interval.

3. **Extract `keyMethod` helper for ARI server registration** — removes ~60 lines of duplicated ObjectKey unmarshal+validate boilerplate in `pkg/ari/server/service.go`.

### Priority 2 (Medium impact, low effort)

4. **Replace `splitN`/`isAbs` with stdlib** — trivial cleanup, removes unnecessary custom implementations.

5. **Remove `GetByWorkspaceName` alias** — confirmed zero callers, safe to delete.

6. **Remove dead `RawClient` (`simple.go`)** — confirmed zero external callers.

7. **Remove dead `WatchServer` + `Event[T]`** — confirmed unused in production code.

8. **Migrate 3 `OutputJSON` callers to `PrintJSON`** then delete the deprecated function.

### Priority 3 (Low impact, medium effort)

9. **Generalize CLI get/delete commands** — higher refactor effort but good for maintainability as more resource types are added.

10. **Decouple `WorkspaceManager.InitRefCounts` from store** — move the DB-reading logic to the daemon startup, keep `WorkspaceManager` as a pure preparation engine.

11. **Consolidate version formatting** — extract shared version text formatting to `internal/version` package.
