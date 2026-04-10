# M007: OAR Platform Terminal State Refactor

## Vision
Cut the entire OAR platform to its terminal state in one clean pass: bbolt replaces SQLite, spec.Status becomes the single state enum (StatusIdle replaces StatusCreated), Session and Room concepts are eliminated, Workspace becomes the unified grouping+filesystem resource, Agent identity switches to (workspace, name) with no UUID, shim becomes the sole post-bootstrap state write authority, and RestartPolicy governs recovery. No compat layer. No incremental migration. Every layer — storage, model, agentd core, ARI surface, CLI, MCP server, and integration tests — ships in this milestone.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | S01 | high | — | ✅ | After this: `go test ./pkg/meta/...` passes with new bbolt store; `go build ./...` is green; `rg 'meta.AgentState|meta.SessionState|go-sqlite3' --type go` returns zero matches. |
| S02 | S02 | high | — | ✅ | After this: unit tests prove shim-only state writes post-bootstrap, tryReload/alwaysNew recovery semantics, and no Session concept anywhere in agentd. |
| S03 | S03 | medium | — | ✅ | After this: ARI handler tests over Unix socket prove workspace/create→agent/create→agent/prompt→agent/stop with (workspace,name) identity; workspace/send routes messages between agents. |
| S04 | S04 | low | — | ✅ | After this: `agentdctl workspace create` and `agentdctl agent create --workspace w --name a` work; `go build ./cmd/workspace-mcp-server` succeeds; design docs reflect new model. |
| S05 | S05 | low | — | ✅ | After this: `go test ./tests/integration/... -v -timeout 120s` passes; `golangci-lint run ./...` returns 0 issues; full milestone verification confirmed. |
