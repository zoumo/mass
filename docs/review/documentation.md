# Documentation Completeness Review Report

Date: 2026-04-30

## Summary

The MASS project has a strong documentation foundation with well-structured design specs, a comprehensive architecture doc, and detailed developer guides. However, several areas need attention: the CHANGELOG is significantly behind (210+ commits since the last update on 2026-04-15), three files referenced in the design README are missing, multiple Go packages lack `doc.go` files, and the ARCHITECTURE.md does not cover the newer `ext/pipeline` subsystem. The Skills documentation (SKILL.md files) is well-maintained and accurately reflects the current CLI capabilities, though a few recently-added commands (like `agentrun task wait`) are not yet documented.

---

## Missing Documentation

### 1. Missing Referenced Files (Broken Links in docs/design/README.md)

| Referenced Path | Status |
|---|---|
| `docs/design/roadmap.md` | MISSING - referenced in README.md doc index |
| `docs/design/orchestration-guide.md` | MISSING - referenced in README.md; actual file is at `docs/develop/orchestration-guide.md` |
| `docs/design/mass/lifecycle-hooks.md` | MISSING - referenced in README.md and `docs/develop/orchestration-guide.md` |

The `orchestration-guide.md` reference is a path error (it exists in `docs/develop/` not `docs/design/`). The other two files simply do not exist.

### 2. Packages Without doc.go

Only one `doc.go` file exists in the entire codebase (`third_party/charmbracelet/crush/csync/doc.go`). All primary packages lack package-level documentation files:

- `pkg/workspace/` - Workspace provisioning (Git/EmptyDir/Local handlers)
- `pkg/jsonrpc/` - Transport-agnostic JSON-RPC 2.0 framework
- `pkg/jsonrpc/ndjson/` - NDJSON streaming utility
- `pkg/watch/` - Generic event push framework
- `pkg/agentd/` - ProcessManager, recovery, agent lifecycle
- `pkg/agentd/store/` - bbolt metadata store
- `pkg/agentrun/api/` - Agent-run wire types and methods
- `pkg/agentrun/client/` - Agent-run client and watcher
- `pkg/agentrun/server/` - Agent-run service, Translator, EventLog
- `pkg/agentrun/runtime/acp/` - ACP runtime Manager
- `pkg/ari/api/` - ARI wire types and domain models
- `pkg/ari/client/` - Typed ARI client
- `pkg/ari/server/` - ARI service implementation
- `pkg/runtime-spec/` - Runtime specification types
- `pkg/runtime-spec/api/` - Pure runtime state types
- `pkg/tui/chat/` - Chat TUI
- `pkg/tui/component/` - TUI UI components
- `internal/logging/` - Logging utilities
- `internal/version/` - Version info

### 3. Exported Functions/Types Without Godoc Comments

The following exported symbols lack documentation:

**pkg/jsonrpc/**:
- `ErrMethodNotFound`, `ErrInvalidParams`, `ErrInternal` (errors.go)
- `UnaryMethod`, `NullaryMethod`, `UnaryCommand`, `NullaryCommand` (method_helpers.go)

**pkg/ari/server/**:
- `type Service struct` (server.go)

**pkg/tui/component/**:
- `NewThinkingItem`, `NewPlanItem`, `NewSystemItem` (simple_items.go)

### 4. Missing cmd/ Documentation

- `cmd/massctl/commands/ext/pipeline/` - The `ext pipeline` subcommand (validate, example, schema) is not documented in ARCHITECTURE.md's package layout section
- No top-level README.md for the project (only CLAUDE.md and AGENTS.md exist for AI assistants)

### 5. Missing `agentrun task wait` Documentation in SKILL.md

The recently added `agentrun task wait` command (commit 7290137) is not documented in `skills/mass-guide/SKILL.md`. The SKILL.md covers `task do`, `task done`, `task get`, `task retry` but not `task wait`.

---

## Stale Documentation

### 1. CHANGELOG Significantly Behind

`docs/CHANGELOG.md` was last updated 2026-04-15 (after M014). There are **210+ commits** since then including major features:
- `agentrun task` system (do/done/get/retry/wait)
- `ext pipeline validate` and `ext pipeline example` commands
- `compose run` subcommand
- Context window usage tracking
- Watch framework (`pkg/watch/`)
- Self-PID restart fix
- Workspace `-f` flag promotion
- YAML parsing unification

None of these are reflected in the CHANGELOG.

### 2. ARCHITECTURE.md Missing Recent Packages and Features

The ARCHITECTURE.md (last updated 2026-04-15) is missing:

- `pkg/watch/` - The watch framework package (WatchServer, RetryWatcher) is not in the component map or package layout
- `cmd/massctl/commands/ext/` - The ext pipeline subsystem is not listed
- `cmd/massctl/commands/compose/` - Listed in package layout but not in the component map
- Task system - `agentrun/task/*` methods are not mentioned in the data flow section
- `compose run` subcommand is not mentioned in the binaries table description

### 3. Design README.md References Non-Existent Documents

The `docs/design/README.md` (last_updated: 2026-04-17) references:
- `roadmap.md` - never created
- `orchestration-guide.md` - wrong path (should be `../develop/orchestration-guide.md`)
- `mass/lifecycle-hooks.md` - never created (design proposal mentioned in the table but file does not exist)

### 4. ARI Spec Missing Task Methods

`docs/design/mass/ari-spec.md` documents `agentrun/task/*` methods, but these were added to the spec after the implementation. The task methods (`agentrun/task/do`, `agentrun/task/get`, `agentrun/task/list`, `agentrun/task/retry`) are documented in the spec, but `agentrun/task/done` and `agentrun/task/wait` (CLI-only operations) are not mentioned.

### 5. ARCHITECTURE.md References Outdated Binary Names

The architecture doc correctly names `bin/mass` and `bin/massctl`, but the "Binaries produced" table description says `mass mesh-mcp` for the workspace MCP server entrypoint, while earlier the component map still says `workspace-mcp-server`. This is internally inconsistent but minor.

---

## Incomplete Documentation

### 1. No Developer Onboarding Guide

There is no comprehensive "getting started for developers" document. The closest is:
- `AGENTS.md` - brief build instructions (`make fmt`, `make lint`, `make build`)
- `docs/develop/rules/code-principle.md` - coding principles

Missing: how to set up a development environment, how to run the daemon locally, how to run integration tests, how to add a new ARI method, how to add a new CLI command.

### 2. No API Reference Generated from Code

No godoc-style API documentation is generated or hosted. Given the complexity of the public API surface (`pkg/ari/client`, `pkg/agentrun/client`, etc.), auto-generated API docs would be valuable.

### 3. Design Docs Missing Error Handling Sections

Several design docs document the happy path thoroughly but lack error handling details:
- `docs/design/mass/watch-framework.md` - No section on error recovery when EventLog is corrupted
- `docs/design/workspace/workspace-spec.md` - No detail on what happens when git clone partially fails (network interruption mid-clone)

### 4. orchestration-guide.md Lifecycle Hooks Section Incomplete

The orchestration guide references "Lifecycle Hooks (designing)" with a link to a non-existent `mass/lifecycle-hooks.md`. The feature appears to be planned but undocumented.

### 5. No Security Documentation

While `contract-convergence.md` has a "Security Boundaries" section, there is no dedicated security document covering:
- Authentication/authorization model (or explicit statement that none exists)
- Socket permission model
- Threat model for multi-agent scenarios

---

## Quality Issues

### 1. Language Inconsistency

Documentation alternates between Chinese and English:
- Design docs are primarily in Chinese (runtime-spec, config-spec, watch-framework, etc.)
- ARCHITECTURE.md, CHANGELOG.md, contract-convergence.md are in English
- Some docs mix both (e.g., design/README.md uses Chinese headers with English content)

This is not necessarily a problem (per project convention of "writing docs in Chinese") but may reduce accessibility for non-Chinese contributors.

### 2. Formatting Consistency

- Design docs consistently use `---` + `last_updated` frontmatter - good
- ARCHITECTURE.md and CHANGELOG.md use `> Auto-generated. Do not edit directly.` header but are manually updated
- Some YAML examples in orchestration-guide.md have typos (`meta` instead of `meta`)

### 3. .DS_Store Files in docs/

`.DS_Store` files are committed under `docs/` and `docs/design/`. These should be in `.gitignore`.

### 4. DECISIONS.md Growing Without Structure

`.gsd/DECISIONS.md` has 127+ decisions in a flat list that has become difficult to navigate. The "Decisions Table" at the end is incomplete (only covers D001-D011 in tabular form, though all 127 decisions are listed sequentially above). Some decisions are duplicated (D028-D033 have duplicates from planning sessions).

---

## Code-Documentation Consistency

### 1. ARCHITECTURE.md Package Layout vs Actual Code

| In ARCHITECTURE.md | Actual Status |
|---|---|
| `cmd/massctl/commands/version/` | EXISTS |
| `cmd/massctl/commands/cliutil/` | EXISTS |
| `pkg/watch/` | EXISTS but NOT in ARCHITECTURE.md |
| `cmd/massctl/commands/ext/` | EXISTS but NOT in ARCHITECTURE.md |
| `cmd/massctl/commands/ext/pipeline/` | EXISTS but NOT in ARCHITECTURE.md |

### 2. ARI Spec vs Code - Task Methods

The ARI spec documents `agentrun/task/*` methods but the implementation is in `cmd/massctl/commands/agentrun/task.go` and `task_wait.go` as CLI commands backed by file operations, not as actual ARI JSON-RPC methods. The task system uses file-based state (`task.json` files in the workspace), not the ARI socket. This architectural choice is not clearly documented.

### 3. Watch Framework Design vs Implementation

`docs/design/mass/watch-framework.md` describes the watch framework design which is fully implemented in `pkg/watch/`. However, the design doc is not referenced from ARCHITECTURE.md, making it discoverable only through the design README index.

### 4. Skills Documentation vs CLI

The mass-guide SKILL.md accurately reflects most CLI capabilities. Notable gaps:
- `massctl agentrun task wait` - not documented (added in commit 7290137)
- `massctl ext pipeline validate` - not documented in mass-guide (documented in mass-pipeline)
- `massctl ext pipeline example` - not documented
- `massctl agentrun debug` - not documented in SKILL.md (exists as a command)

### 5. Contract Convergence vs Current Implementation

`docs/design/contract-convergence.md` accurately reflects the current state machine and ARI boundary. The document is well-maintained and consistent with the code.

---

## Recommendations

### Priority 1 (High Impact, Low Effort)

1. **Fix broken links in `docs/design/README.md`**: Update the orchestration-guide link to `../develop/orchestration-guide.md`; remove or mark as TODO the references to `roadmap.md` and `lifecycle-hooks.md`.

2. **Add `pkg/watch/` to ARCHITECTURE.md**: The watch framework is a core infrastructure package that is undocumented in the architecture overview.

3. **Document `agentrun task wait` in SKILL.md**: Add the wait command to the Task Lifecycle section of mass-guide.

4. **Add `cmd/massctl/commands/ext/` to ARCHITECTURE.md package layout**: The ext/pipeline subsystem should be listed.

### Priority 2 (High Impact, Medium Effort)

5. **Update CHANGELOG.md**: Add entries for the major features since M014 (task system, watch framework, compose run, ext pipeline, usage tracking).

6. **Add doc.go files for top-level packages**: At minimum, `pkg/workspace`, `pkg/jsonrpc`, `pkg/watch`, `pkg/agentd`, `pkg/agentrun`, `pkg/ari`, `pkg/runtime-spec`, and `pkg/tui` should have package-level documentation.

7. **Add godoc comments to exported pkg/jsonrpc helpers**: The `UnaryMethod`, `NullaryMethod`, etc. helpers are part of the public API and need documentation.

8. **Add .DS_Store to .gitignore** and remove committed .DS_Store files.

### Priority 3 (Medium Impact, Higher Effort)

9. **Create a developer onboarding guide** (`docs/develop/getting-started.md`): Cover local setup, running the daemon, running tests, adding new features.

10. **Clarify task system architecture**: Document that `agentrun/task/*` are CLI-level operations using file-based state, not ARI RPC methods. This is a common source of confusion given the ARI spec lists them alongside actual RPC methods.

11. **Create `docs/design/roadmap.md`**: The README references it; either create it or remove the reference.

12. **Restructure DECISIONS.md**: Consider splitting into per-milestone decision files or adding a table of contents with categories.

### Priority 4 (Nice to Have)

13. **Generate API reference documentation**: Set up `godoc` or `pkgsite` hosting for the public packages.

14. **Add a security model document**: Even if it only states "MASS runs as a local daemon with no authentication; all access is trusted via Unix socket permissions."

15. **Standardize language**: Consider adding a note to CLAUDE.md about which docs should be in Chinese vs English to maintain consistency.
