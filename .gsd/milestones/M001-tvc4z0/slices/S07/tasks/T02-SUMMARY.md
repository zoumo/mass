---
id: T02
parent: S07
milestone: M001-tvc4z0
key_files:
  - cmd/agentdctl/session.go
  - cmd/agentdctl/main.go
key_decisions:
  - Created minimal main.go stub for build verification (T04 will expand)
  - Used global socketPath variable set by root command persistent flag
  - Implemented parseLabels helper for comma-separated key=value parsing
duration: 
verification_result: passed
completed_at: 2026-04-06T16:20:55.074Z
blocker_discovered: false
---

# T02: Implemented 7 session subcommands for agentdctl CLI with cobra

**Implemented 7 session subcommands for agentdctl CLI with cobra**

## What Happened

Created cmd/agentdctl/session.go with 7 session management subcommands using spf13/cobra: session new (with required --workspace-id, --runtime-class flags), session list, session status (positional session-id), session prompt (positional session-id + required --text), session stop, session remove, session attach. Also created minimal cmd/agentdctl/main.go stub for build verification - defines socketPath global with default /var/run/agentd/ari.sock, root command with persistent --socket flag, and adds sessionCmd. Helper functions: getClient() creates ARI client, outputJSON() pretty-prints JSON, handleError() prints error and exits, parseLabels() parses comma-separated key=value pairs.

## Verification

Verified go build ./cmd/agentdctl passes. All 7 session subcommands registered under session command. Required flags validated by cobra. Positional args validated by cobra. Connection error handling works with nonexistent socket. All Must-Haves met.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./cmd/agentdctl` | 0 | ✅ pass | 1000ms |
| 2 | `./agentdctl --help` | 0 | ✅ pass | 500ms |
| 3 | `./agentdctl session --help` | 0 | ✅ pass | 500ms |
| 4 | `./agentdctl session new --help` | 0 | ✅ pass | 500ms |
| 5 | `./agentdctl session prompt --help` | 0 | ✅ pass | 500ms |
| 6 | `./agentdctl session new` | 1 | ✅ pass | 500ms |
| 7 | `./agentdctl session prompt test-id` | 1 | ✅ pass | 500ms |
| 8 | `./agentdctl session status` | 1 | ✅ pass | 500ms |
| 9 | `./agentdctl --socket /tmp/nonexistent.sock session list` | 1 | ✅ pass | 500ms |

## Deviations

None

## Known Issues

None

## Files Created/Modified

- `cmd/agentdctl/session.go`
- `cmd/agentdctl/main.go`
