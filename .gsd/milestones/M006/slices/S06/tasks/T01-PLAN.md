---
estimated_steps: 44
estimated_files: 11
skills_used: []
---

# T01: Fix all 13 gocritic issues across 11 files

Apply mechanical fixes for every gocritic finding. The 13 issues fall into 7 categories:

1. **filepathJoin** (2 issues) — split path literals that embed separators:
   - `pkg/agentd/process_test.go:131` — `filepath.Join("/tmp/agentd-shim", sessionID)` → `filepath.Join("/tmp", "agentd-shim", sessionID)`
   - `pkg/agentd/shim_client_test.go:51` — `filepath.Join("/tmp", ...)` → `filepath.Join(os.TempDir(), ...)`

2. **importShadow** (5 issues) — rename local variables that shadow imported package names:
   - `pkg/ari/registry.go:80` — `meta := r.workspaces[id]` → rename `meta` to `wsMeta`; also update all uses in that function scope (the `for _, meta := range` on line ~91)
   - `pkg/ari/server.go:291` — `workspace := &meta.Workspace{...}` → rename `workspace` to `ws`; update all uses in that block
   - `pkg/ari/server.go:374` — `meta := h.srv.registry.Get(p.WorkspaceId)` → rename `meta` to `wsMeta`; update all uses in that block
   - `pkg/ari/server_test.go:281` — `workspace := &meta.Workspace{...}` → rename `workspace` to `ws`; update all uses in that function
   - `pkg/ari/server_test.go:432` — `meta := h.registry.Get(result.WorkspaceId)` → rename `meta` to `wsMeta`; update all uses in that block

3. **appendAssign** (1 issue) — avoid accidental mutation of `turn1` by pre-allocating:
   - `pkg/events/translator_test.go:620` — `all := append(turn1, turn2...)` → replace with:
     ```go
     all := make([]Envelope, 0, len(turn1)+len(turn2))
     all = append(all, turn1...)
     all = append(all, turn2...)
     ```

4. **exitAfterDefer** (2 issues) — in TestMain functions, replace `defer os.RemoveAll(tmpDir)` + `os.Exit(m.Run())` with explicit cleanup:
   - `pkg/rpc/server_test.go:47` and `pkg/runtime/runtime_test.go:46`:
     Remove the `defer os.RemoveAll(tmpDir)` line; before `os.Exit(m.Run())` add the explicit call:
     ```go
     code := m.Run()
     os.RemoveAll(tmpDir)
     os.Exit(code)
     ```

5. **builtinShadowDecl** (1 issue) — delete the custom `min` function in `pkg/runtime/terminal.go` (Go 1.21+ provides built-in `min`; the existing call at line 115 `min(limit, 4096)` will resolve to the built-in automatically):
   - Remove the `// min returns the minimum of two integers.` comment and the `func min(a, b int) int { ... }` block (lines ~377–383)

6. **appendCombine** (1 issue) — merge two consecutive appends into one call:
   - `pkg/workspace/hook.go:33–34`:
     ```go
     parts = append(parts, fmt.Sprintf("workspace: hook %s failed", e.Phase))
     parts = append(parts, fmt.Sprintf("hookIndex=%d", e.HookIndex))
     ```
     → `parts = append(parts, fmt.Sprintf("workspace: hook %s failed", e.Phase), fmt.Sprintf("hookIndex=%d", e.HookIndex))`

7. **elseif** (1 issue) — flatten `else { if cond {} }` to `else if cond {}`:
   - `pkg/workspace/hook_test.go:594–600`:
     ```go
     } else {
         if err != nil {
             t.Errorf("expected nil, got: %v", err)
         }
     }
     ```
     → `} else if err != nil { t.Errorf("expected nil, got: %v", err) }`

## Inputs

- ``pkg/agentd/process_test.go``
- ``pkg/agentd/shim_client_test.go``
- ``pkg/ari/registry.go``
- ``pkg/ari/server.go``
- ``pkg/ari/server_test.go``
- ``pkg/events/translator_test.go``
- ``pkg/rpc/server_test.go``
- ``pkg/runtime/runtime_test.go``
- ``pkg/runtime/terminal.go``
- ``pkg/workspace/hook.go``
- ``pkg/workspace/hook_test.go``

## Expected Output

- ``pkg/agentd/process_test.go``
- ``pkg/agentd/shim_client_test.go``
- ``pkg/ari/registry.go``
- ``pkg/ari/server.go``
- ``pkg/ari/server_test.go``
- ``pkg/events/translator_test.go``
- ``pkg/rpc/server_test.go``
- ``pkg/runtime/runtime_test.go``
- ``pkg/runtime/terminal.go``
- ``pkg/workspace/hook.go``
- ``pkg/workspace/hook_test.go``

## Verification

golangci-lint run ./... 2>&1 | grep gocritic; [ $? -eq 1 ] && echo PASS || echo FAIL
