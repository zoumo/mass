---
id: M010
title: "CLI Consolidation: subcommands layout + workspace UX fixes"
status: complete
completed_at: 2026-04-11T13:20:05.788Z
key_decisions:
  - (none)
key_files:
  - cmd/agentd/main.go
  - cmd/agentd/subcommands/root.go
  - cmd/agentdctl/main.go
  - cmd/agentdctl/subcommands/root.go
  - cmd/agentdctl/subcommands/cliutil/cliutil.go
  - cmd/agentdctl/subcommands/workspace/command.go
  - cmd/agentdctl/subcommands/workspace/create/command.go
lessons_learned:
  - (none)
---

# M010: CLI Consolidation: subcommands layout + workspace UX fixes

**Moved cmd/agentd and cmd/agentdctl into subcommands/ layout and reshaped workspace CLI per review recommendations**

## What Happened

Three slices. S01: cmd/agentd refactored into subcommands/server, subcommands/shim, subcommands/workspacemcp — main.go is now 8 lines. S02: cmd/agentdctl refactored into subcommands/{agent,agentrun,daemon,shim,workspace} with shared cliutil package and getClient closure injection from root — eliminated package globals. S03: workspace command reshaped per review — create split into local/git/empty/-f subcommands, workspace get added, workspace send made positional, emptyDir removed from help.

## Success Criteria Results

All criteria met. make build passes. agentdctl workspace --help and agentdctl workspace create --help match acceptance criteria exactly.

## Definition of Done Results



## Requirement Outcomes



## Deviations

None.

## Follow-ups

None.
