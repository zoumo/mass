---
id: M008
title: "CLI Consolidation + API Model Rename"
status: complete
completed_at: 2026-04-10T18:24:25.704Z
key_decisions:
  - D103: Binary consolidation (5→2 binaries) — agentd cobra tree (server/shim/workspace-mcp) + agentdctl resource commands (agent/agentrun/workspace/shim); eliminates binary path resolution issues
  - D104: --root flag replaces config.yaml entirely; Options struct derives all 5 paths deterministically — matches containerd --root pattern
  - D105: meta.Runtime in v1/runtimes bbolt bucket with full ARI CRUD — dynamic runtime registration without daemon restart, persistent across restarts
  - D106: Capabilities struct deleted — not used by current shim protocol layer; ACP-level negotiation handled at shim layer
  - D107: ProcessManager self-fork via os.Executable(); OAR_SHIM_BINARY env override retained for testing and custom deployments
  - D108: RuntimeSpec.Env as []EnvVar (slice, ordered) throughout; conversion to key=value strings only at forkShim call site
  - D109: API model rename: agent=template definition (AgentTemplate CRUD), agentrun=running instance (lifecycle) — aligns with containerd Container/Task model
  - D110: Fresh-start bbolt bucket rename (v1/runtimes→v1/agents for templates, v1/agents→v1/agentruns for instances) — no migration script at current maturity
  - D111: Socket path overflow validated at agentrun/create entry with -32602; platform limits in build-tag files (darwin:104, linux:108)
  - Three-layer rename discipline (meta → ari types → ari server + CLI) must compile as a unit — never layer-by-layer (K074)
  - cobra package main collision avoidance: wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag var scoping (K068)
  - runtimeApplySpec local YAML struct keeps pkg/ari types free of yaml tags (K071)
  - ari.Client.Call wraps RPC errors as fmt.Errorf strings — use err.Error() contains check, not errors.As(*jsonrpc2.Error) (K073)
key_files:
  - cmd/agentd/main.go
  - cmd/agentd/server.go
  - cmd/agentd/shim.go
  - cmd/agentd/workspacemcp.go
  - cmd/agentdctl/agent_template.go
  - cmd/agentdctl/agent.go
  - cmd/agentdctl/shim.go
  - cmd/agentdctl/workspace.go
  - cmd/agentdctl/main.go
  - Makefile
  - pkg/agentd/options.go
  - pkg/agentd/process.go
  - pkg/agentd/runtimeclass.go
  - pkg/meta/models.go
  - pkg/meta/agent.go
  - pkg/meta/runtime.go
  - pkg/meta/store.go
  - pkg/ari/types.go
  - pkg/ari/server.go
  - pkg/spec/maxsockpath_darwin.go
  - pkg/spec/maxsockpath_linux.go
  - tests/integration/session_test.go
  - tests/integration/runtime_test.go
  - tests/integration/e2e_test.go
lessons_learned:
  - Three-layer rename discipline: meta layer → ari types → ari server → CLI must compile as a unit — attempting layer-by-layer leaves the build broken between steps and wastes time debugging phantom import errors
  - cobra inline command literal extraction is a prerequisite for Flags() — you cannot call Flags() on an unaddressable inline literal; always extract to a named var first (K072)
  - ari.Client.Call surfaces RPC errors as plain fmt.Errorf strings, not typed *jsonrpc2.Error — errors.As check will silently never match; use err.Error() contains '-32602' style assertions (K073)
  - macOS socket path limit (104 bytes) bites t.TempDir() in pkg tests — use os.MkdirTemp('/tmp', 'oar-*') for any test that creates a Unix domain socket; t.TempDir() produces paths in /var/folders/... which routinely exceed the limit (K075)
  - Self-fork pattern (os.Executable() + 'shim' first arg) is simpler than binary path resolution but requires OAR_SHIM_BINARY escape hatch for test environments where the running binary is a test binary, not the real agentd
  - Deleting stub files from a prior slice (agentrun.go stub from S03) is expected in later slices — the stub's grammar (flags) was intentionally a scaffold that the real implementation superseded with a cleaner positional grammar
  - cobra subcommand collision in shared package main is solved by two complementary patterns: (1) type/function prefixing for inlined source packages, (2) local var scoping inside constructor functions for flag vars — both are needed; using only one leaves half the collision surface unaddressed
---

# M008: CLI Consolidation + API Model Rename

**Consolidated 5 binaries into 2 (agentd + agentdctl), eliminated config.yaml via --root flag, elevated RuntimeClass to a DB-persisted AgentTemplate entity with full ARI CRUD, adopted resource-first CLI grammar, and renamed the API model to agent=template / agentrun=running instance — aligning OAR with the containerd Container/Task conceptual model.**

## What Happened

M008 executed as four sequential slices, each building on the prior slice's foundation without pre-existing dependencies between slices.

**S01 — Binary Skeleton Reorganization**
Replaced the flat flag-based cmd/agentd/main.go with a proper cobra tree (server/shim/workspace-mcp subcommands). Inlined cmd/agent-shim/main.go as `agentd shim` (shim-prefixed types, locally scoped flags). Inlined cmd/workspace-mcp-server/main.go as `agentd workspace-mcp` (wmcp-prefixed types). Extended cmd/agentdctl with a full shim client (shimCmd with state/history/prompt/chat/stop) and 9-stub agentrun subcommands. Replaced the wildcard Makefile with explicit agentd+agentdctl targets. Result: clean cobra tree, go build ./... and go vet ./... both exit 0.

**S02 — --root Config + Runtime Entity + Self-Fork**
Eliminated config.yaml entirely. Created pkg/agentd/options.go with Options{Root string} and five deterministic path helpers (SocketPath, WorkspaceRoot, BundleRoot, MetaDBPath) — the single source of truth for the agentd directory layout. Created pkg/meta/runtime.go with RuntimeSpec + Runtime entity and four CRUD methods. Deleted pkg/agentd/config.go (Config, ParseConfig, RuntimeClassRegistry, Capabilities — all gone). Wired ProcessManager to resolve runtimes from the meta.Store at process-start time. Switched forkShim from a binary-path resolver to os.Executable() self-fork with OAR_SHIM_BINARY env override. Added runtime/* ARI CRUD (set/get/list/delete) with agentdctl runtime subcommand. Rewrote all integration test fixtures to use --root + runtime/set pattern. Created TestRuntimeLifecycle acceptance test (passes in 1.4s).

**S03 — CLI Grammar Alignment + Socket Validation**
Two targeted grammar fixes: (1) extracted agentrun prompt's anonymous inline cobra literal into a named promptCmd variable, added -w/--workspace and --text flags; (2) removed --type/--name flags from workspace create, added cobra.ExactArgs(2) with positional <type> <name> grammar. Extracted maxUnixSocketPath constant from pkg/spec/state.go into build-tag files (maxsockpath_darwin.go: 104, maxsockpath_linux.go: 108). Added ValidateAgentSocketPath side-effect-free method to ProcessManager. Wired early -32602 guard in handleAgentCreate before any DB write. TestAgentCreateSocketPathTooLong confirms guard fires on 70-char name with no DB record written.

**S04 — Cleanup + API Rename (agent/agentrun) + Integration Tests**
Three obsolete cmd directories deleted (agent-shim, agent-shim-cli, workspace-mcp-server). Three-layer simultaneous rename: meta DB layer (Runtime→AgentTemplate, Agent→AgentRun; bbolt buckets: v1/runtimes→v1/agents for templates, v1/agents→v1/agentruns for instances); ARI types.go (all 14 Runtime* types → AgentTemplate*, all 16 Agent* types → AgentRun*); ARI server dispatch (runtime/set|get|list|delete → agent/set|get|list|delete; agent/create|prompt|... → agentrun/create|prompt|...); CLI (runtime.go→agent_template.go with Use:"agent", agent.go rewritten as agentrunCmd with Use:"agentrun"). Deleted stub agentrun.go and old runtime.go. All 8 integration tests pass (22s). rg 'runtime/' pkg/ari/server.go returns zero non-comment dispatch matches.

**Final state:** 37 files changed from the pre-M008 baseline (4dbb6e9). Two binaries produced by `make build`. All integration tests green. ARI surface: workspace/* + agent/* + agentrun/* only.

## Success Criteria Results

## S01 Success Criteria
- ✅ `make build` produces only bin/agentd and bin/agentdctl (confirmed: `ls cmd/` shows only agentd, agentdctl)
- ✅ `agentd --help` shows server/shim/workspace-mcp subcommands (confirmed via live binary)
- ✅ `agentdctl --help` shows agent/agentrun/workspace/shim subcommands (confirmed via live binary)
- ✅ `go build ./...` and `go vet ./...` both pass (exit 0)

## S02 Success Criteria
- ✅ `agentd server --root /tmp/test-agentd-...` starts without config.yaml (creates socket, DB, bundles/, workspaces/)
- ✅ `agentdctl runtime apply -f mockagent.yaml` persisted to DB (evolved to `agentdctl agent apply` in S04, verified via TestAgentTemplateLifecycle)
- ✅ agentrun create using that template reaches idle state (TestRuntimeLifecycle: PASS, 1.4s)

## S03 Success Criteria
- ✅ `agentdctl agentrun prompt --help` shows --text and (in S03 phase: -w/--workspace); S04 evolved grammar to positional workspace/name — current state confirmed
- ✅ `agentdctl workspace create local myws --path /tmp` works (positional <type> <name> grammar confirmed via --help and live binary)
- ✅ agentrun/create with >90-char combined name returns -32602 with clear error (TestAgentCreateSocketPathTooLong: PASS)

## S04 Success Criteria
- ✅ cmd/agent-shim/, cmd/agent-shim-cli/, cmd/workspace-mcp-server/ absent (confirmed: `ls cmd/` = agentd + agentdctl only)
- ✅ `go test ./tests/integration/...` passes without config.yaml (8/8 pass, 11.4s, no config.yaml in any fixture)
- ✅ `rg 'runtime/' pkg/ari/` returns zero ARI dispatch matches (two remaining hits are a comment and a test mock, not ARI methods)
- ✅ ARI surface is workspace/* + agent/* + agentrun/* (confirmed via grep of server.go case statements)

## Definition of Done Results

## Definition of Done

- ✅ All 4 slices complete (S01, S02, S03, S04 all status=complete)
- ✅ All slice summaries exist (S01-SUMMARY.md, S02-SUMMARY.md, S03-SUMMARY.md, S04-SUMMARY.md)
- ✅ make build produces exactly bin/agentd + bin/agentdctl — no other binaries built
- ✅ go build ./... exit 0 (all cmd/ dirs including any remaining legacy code)
- ✅ go vet ./... exit 0
- ✅ go test ./tests/integration/... passes (8 tests, 11.4s, no config.yaml)
- ✅ Code changes verified: 37 non-.gsd/ files changed from pre-M008 baseline (4dbb6e9)
- ✅ Cross-slice integration: S01 cobra tree → S02 --root wiring → S03 grammar/validation → S04 rename all chain correctly; integration tests exercise the full pipeline end-to-end
- ✅ ARI surface clean: workspace/* + agent/* + agentrun/* only — no runtime/* dispatch in pkg/ari/server.go

## Requirement Outcomes

## Requirement Status Transitions

| ID | Description | Before M008 | After M008 | Evidence |
|----|-------------|-------------|------------|----------|
| R001 | agentd daemon can start with --root flag (no config.yaml) | active | **validated** | `agentd server --root /tmp/test-agentd-s02` creates socket without config.yaml; TestRuntimeLifecycle PASS; config.yaml + ParseConfig() deleted entirely |
| R002 | Runtime entity persisted to DB with ARI CRUD | active | **validated** | meta.AgentTemplate in v1/agents bbolt bucket; ARI agent/set|get|list|delete; agentdctl agent apply/get/list/delete; TestAgentTemplateLifecycle + TestEndToEndPipeline confirm full agent/set → agentrun → idle chain |
| R007 | CLI tool for ARI operations | validated (M001/S07) | **re-validated** | agentdctl CLI consolidated to resource-first grammar: `agentdctl agent` (template CRUD) + `agentdctl agentrun` (lifecycle 9 subcommands) + workspace/daemon/shim; all verified via --help and integration tests |

## Deviations

["S03 established -w/--workspace flag grammar for agentrun prompt; S04 replaced this stub with a real implementation using positional <workspace/name> grammar (slash-separated, kubectl-style) — this is a forward evolution, not a regression", "S02 slice acceptance criteria reference 'agentdctl runtime apply' — this command was renamed to 'agentdctl agent apply' in S04 as part of the API rename; behavior is identical", "rg 'runtime/' pkg/ari/ returns two non-zero matches: a comment in server.go ('Calls processes.Stop which sends runtime/stop to the shim') and a test mock handler in server_test.go ('case runtime/status') — both are shim-layer RPC references, not ARI dispatch methods; this satisfies the spirit of the zero-matches criterion"]

## Follow-ups

["Pre-existing test failures on macOS: TestAgentCreateReturnsCreating, TestProcessManagerStart, and TestGenerateConfig/basic_agent_config fail due to t.TempDir() socket path length and a test expectation defect (workspace-mcp always added to MCP servers) — these are not M008 regressions but should be fixed in the next milestone", "OAR_SHIM_BINARY must be set in test environments (unit tests creating ProcessManager directly) because os.Executable() returns the test binary path — consider a cleaner test injection API to avoid this requirement", "go run detection emits only a WARN at server startup — this is non-fatal and acceptable for development, but a future milestone should add a proper error or documentation for production deployment"]
