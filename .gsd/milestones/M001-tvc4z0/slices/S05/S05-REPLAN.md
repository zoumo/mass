# S05 Replan

**Milestone:** M001-tvc4z0
**Slice:** S05
**Blocker Task:** T03
**Created:** 2026-04-03T05:14:35.769Z

## Blocker Description

ACP NewSession RPC call hangs during handshake between shim and mockagent in test environment. Debug logging shows Initialize succeeds but NewSession never returns. Manual shim execution works correctly - issue is specific to test subprocess execution.

## What Changed

Added T04 to debug and fix the ACP NewSession hang. T01-T03 remain as-is since they are implemented. The verification failure in T03 is due to this blocker.
