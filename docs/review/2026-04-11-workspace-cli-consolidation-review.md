# Workspace CLI Consolidation Review

**Date**: 2026-04-11
**Status**: Actionable review
**Scope**: `agentdctl workspace` command shape and CLI directory layout vs. `docs/plan/cli-consolidation.md`

---

## Summary

`agentdctl workspace` is still using the older command shape and does not match the workspace section of the CLI consolidation plan.

This review covers the `workspace` command group and the related CLI directory structure. The current top-level split between `agent`, `agentrun`, `shim`, and `workspace` is treated as intentional product direction and is not evaluated here.

---

## Finding

### P2: Workspace CLI shape still follows the old API, not the planned resource-first UX

File:

- `cmd/agentdctl/workspace.go`

Current implementation:

```text
agentdctl workspace create <type> <name>
agentdctl workspace create emptyDir <name>
agentdctl workspace send --workspace <name> --from <agent> --to <agent> --text "..."
```

Planned implementation from `docs/plan/cli-consolidation.md`:

```text
agentdctl workspace list
agentdctl workspace get <name>
agentdctl workspace create local <name> --path <path>
agentdctl workspace create git <name> --url <url> [--ref <ref>] [--depth <n>]
agentdctl workspace create empty <name>
agentdctl workspace create -f <file>
agentdctl workspace delete <name>
agentdctl workspace send <name> --from <agent> --to <agent> --text "..."
```

Impact:

- The documented `workspace get <name>` command is missing.
- The documented nested `workspace create local|git|empty` command shape is missing.
- The documented `workspace create -f <file>` path for full specs is missing.
- The command still uses legacy `emptyDir` instead of planned `empty`.
- `workspace send` takes workspace name through `--workspace` instead of positional `<name>`.
- Users following `cli-consolidation.md` will hit missing or incompatible commands.

### P2: CLI code is still flat instead of the planned `subcommands/...` layout

Files:

- `cmd/agentd/main.go`
- `cmd/agentd/server.go`
- `cmd/agentd/shim.go`
- `cmd/agentd/workspacemcp.go`
- `cmd/agentdctl/main.go`
- `cmd/agentdctl/workspace.go`
- `cmd/agentdctl/shim.go`
- `cmd/agentdctl/agent.go`
- `cmd/agentdctl/agent_template.go`

Current implementation:

```text
cmd/
  agentd/
    main.go
    server.go
    shim.go
    workspacemcp.go

  agentdctl/
    main.go
    agent.go
    agent_template.go
    daemon.go
    helpers.go
    shim.go
    workspace.go
```

Planned implementation from `docs/plan/cli-consolidation.md`:

```text
cmd/
  agentd/
    main.go
    subcommands/
      root.go
      server/
        command.go
      shim/
        command.go
      workspacemcp/
        command.go

  agentdctl/
    main.go
    subcommands/
      root.go
      agent/
        command.go
      agentrun/
        command.go
      workspace/
        command.go
        list.go
        get.go
        create/
          command.go
          local.go
          git.go
          empty.go
          file.go
        delete.go
        send.go
      shim/
        command.go
```

Impact:

- The command implementation is harder to compare against the planned CLI surface.
- Workspace create variants cannot be implemented as focused files without making the existing flat file larger.
- Future command ownership is unclear because `main.go` still manually assembles commands from package-level globals.
- The code organization does not satisfy the design requirement that CLI changes align with `docs/plan/cli-consolidation.md`.

Note:

- The original plan names a `runtime` group, but the current product direction is `agent` plus `agentrun`. The target directory layout above reflects that updated direction while preserving the plan's `subcommands/...` organization principle.

---

## Required Changes

### 0. Move CLI code into `subcommands/...`

Refactor `cmd/agentd` to:

```text
cmd/agentd/main.go
cmd/agentd/subcommands/root.go
cmd/agentd/subcommands/server/command.go
cmd/agentd/subcommands/shim/command.go
cmd/agentd/subcommands/workspacemcp/command.go
```

Refactor `cmd/agentdctl` to:

```text
cmd/agentdctl/main.go
cmd/agentdctl/subcommands/root.go
cmd/agentdctl/subcommands/agent/command.go
cmd/agentdctl/subcommands/agentrun/command.go
cmd/agentdctl/subcommands/workspace/command.go
cmd/agentdctl/subcommands/workspace/list.go
cmd/agentdctl/subcommands/workspace/get.go
cmd/agentdctl/subcommands/workspace/create/command.go
cmd/agentdctl/subcommands/workspace/create/local.go
cmd/agentdctl/subcommands/workspace/create/git.go
cmd/agentdctl/subcommands/workspace/create/empty.go
cmd/agentdctl/subcommands/workspace/create/file.go
cmd/agentdctl/subcommands/workspace/delete.go
cmd/agentdctl/subcommands/workspace/send.go
cmd/agentdctl/subcommands/shim/command.go
```

Implementation notes:

- Keep `cmd/agentd/main.go` minimal: call `subcommands.NewRootCommand().Execute()`.
- Keep `cmd/agentdctl/main.go` minimal: call `subcommands.NewRootCommand().Execute()`.
- Move global socket configuration into `cmd/agentdctl/subcommands/root.go`.
- Avoid package-level command globals where possible; prefer `NewCommand(...) *cobra.Command` constructors.
- Move shared CLI helpers into an internal helper package or keep them under `cmd/agentdctl/subcommands` if they are only used by command packages.
- Do the directory move before reshaping `workspace create`; the nested `workspace/create/...` layout maps directly to the desired command model.

### 1. Reshape `workspace create`

Replace the current generic command:

```text
workspace create <type> <name>
```

With a `create` command group:

```text
workspace create local <name> --path <path>
workspace create git <name> --url <url> [--ref <ref>] [--depth <n>]
workspace create empty <name>
workspace create -f <file>
```

Implementation notes:

- Keep the existing `workspace/create` ARI method unless the server API also needs to change.
- For `local`, build `workspace.Source{Type: workspace.SourceTypeLocal, Local: ...}`.
- For `git`, build `workspace.Source{Type: workspace.SourceTypeGit, Git: ...}`.
- For `empty`, build `workspace.Source{Type: workspace.SourceTypeEmptyDir}`.
- Decide whether `emptyDir` should be removed immediately or retained as a hidden/deprecated alias. Since the project currently says compatibility is not required, prefer removing it from the public help output.

### 2. Add `workspace get <name>`

Add:

```text
workspace get <name>
```

Expected behavior:

- Return a single workspace status/spec as JSON.
- Prefer an existing ARI method if one exists.
- If no `workspace/get` method exists, either add the method server-side or document that `get` is temporarily implemented through `workspace/status`.

Suggested command behavior:

```text
agentdctl workspace get oar-project
```

Output should use the existing `outputJSON(...)` helper for consistency with other `agentdctl` commands.

### 3. Add `workspace create -f <file>`

Add file-based create for complete workspace specs:

```text
workspace create -f workspace.yaml
```

Implementation notes:

- Parse YAML.
- Validate required fields before calling ARI.
- Support advanced workspace fields that cannot be represented by the simple `local`, `git`, and `empty` shortcuts.
- If the current server-side workspace model cannot yet represent all advanced fields, implement the parser and simple supported subset first, then clearly return an error for unsupported fields.

### 4. Change `workspace send`

Replace:

```text
workspace send --workspace <name> --from <agent> --to <agent> --text "..."
```

With:

```text
workspace send <name> --from <agent> --to <agent> --text "..."
```

Implementation notes:

- Make `<name>` a required positional argument.
- Remove the public `--workspace` flag.
- Continue calling the existing `workspace/send` ARI method with `Workspace: args[0]`.

---

## Suggested Implementation Order

1. Move `cmd/agentd` and `cmd/agentdctl` to the planned `subcommands/...` layout.
2. Keep the first refactor behavior-preserving and run `make build`.
3. Update workspace command declarations so help output matches the target shape.
4. Split workspace create into dedicated `local`, `git`, `empty`, and `file` handlers.
5. Update workspace send to read workspace name from `args[0]`.
6. Add workspace get.
7. Add or update tests that assert command help and basic argument validation.
8. Run `make build`.
9. Run the affected integration tests.

---

## Acceptance Criteria

The command source tree should follow this shape:

```text
cmd/agentd/subcommands/...
cmd/agentdctl/subcommands/...
```

The binary entrypoints should stay minimal:

```text
cmd/agentd/main.go
cmd/agentdctl/main.go
```

The following commands should appear in help output:

```bash
agentdctl workspace --help
agentdctl workspace create --help
```

The following command forms should be accepted by Cobra argument parsing:

```bash
agentdctl workspace list
agentdctl workspace get oar-project
agentdctl workspace create local oar-project --path /tmp/oar
agentdctl workspace create git oar-project --url https://github.com/org/repo --ref main
agentdctl workspace create empty oar-project
agentdctl workspace create -f workspace.yaml
agentdctl workspace delete oar-project
agentdctl workspace send oar-project --from codex --to reviewer --text "review this"
```

The following old command forms should not appear in public help output:

```bash
agentdctl workspace create emptyDir oar-project
agentdctl workspace send --workspace oar-project --from codex --to reviewer --text "review this"
```

---

## Verification Commands

Run:

```bash
make build
```

Then inspect help:

```bash
./bin/agentdctl workspace --help
./bin/agentdctl workspace create --help
```

If command parser tests are added, run the relevant package tests. If no focused test package exists yet, run:

```bash
go test ./cmd/agentdctl
go test ./tests/integration
```

---

## Notes

This review intentionally does not require changing the top-level CLI design to match the original `runtime` / `agent` split in `cli-consolidation.md`. If the current product direction is `agent` for templates and `agentrun` for runtime lifecycle, update `docs/plan/cli-consolidation.md` separately so future reviews do not flag intentional design changes as defects.
