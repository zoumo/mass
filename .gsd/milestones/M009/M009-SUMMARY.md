---
id: M009
title: "Simplify ACP Client in Runtime"
status: complete
completed_at: 2026-04-11T12:48:44.332Z
key_decisions:
  - (none)
key_files:
  - pkg/runtime/client.go
  - pkg/runtime/runtime.go
  - pkg/runtime/client_test.go
  - internal/testutil/mockagent/main.go
lessons_learned:
  - (none)
---

# M009: Simplify ACP Client in Runtime

**Removed TerminalManager and fs/terminal implementations from pkg/runtime; ACP client now only handles SessionUpdate and RequestPermission**

## What Happened

Single-slice milestone. Deleted ~900 lines of terminal and fs implementation code. The acpClient still satisfies acp.Client (all 9 methods present) but 7 of them return not-supported. The Initialize handshake no longer advertises fs capabilities so conformant ACP agents will never invoke these paths. Mockagent and runtime tests updated accordingly.

## Success Criteria Results

- go build ./... ✅\n- go test ./pkg/runtime/... ✅\n- terminal.go and terminal_test.go deleted ✅\n- acpClient has no terminalMgr field ✅\n- Manager has no terminalMgr field ✅\n- fs/terminal methods return not-supported ✅\n- Initialize sends empty fs capabilities ✅

## Definition of Done Results



## Requirement Outcomes



## Deviations

None.

## Follow-ups

None.
