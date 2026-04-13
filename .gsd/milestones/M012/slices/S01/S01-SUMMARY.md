---
id: S01
parent: M012
milestone: M012
provides:
  - (none)
requires:
  []
affects:
  []
key_files:
  - ["pkg/jsonrpc/server.go", "pkg/jsonrpc/client.go", "pkg/jsonrpc/errors.go", "pkg/jsonrpc/peer.go", "pkg/jsonrpc/server_test.go", "pkg/jsonrpc/client_test.go"]
key_decisions:
  - (none)
patterns_established:
  - (none)
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-13T16:11:19.940Z
blocker_discovered: false
---

# S01: pkg/jsonrpc/ Transport-Agnostic Framework

**New pkg/jsonrpc/ framework with Server+Client+RPCError+Peer, all 18 protocol tests passing**

## What Happened

Built the shared JSON-RPC foundation. Server is transport-agnostic (accepts net.Listener), uses ServiceDesc pattern for method registration, supports interceptor chain. Client wraps sourcegraph/jsonrpc2 with a bounded 256-buffer FIFO notification worker that prevents slow handlers from blocking response dispatch. RPCError carries code/message/data. Peer is injected into handler context for server-initiated notifications.

## Verification

go test ./pkg/jsonrpc/... -v -count=1 passes all 18 tests; make build passes

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

None.
