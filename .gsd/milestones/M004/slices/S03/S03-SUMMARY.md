---
id: S03
parent: M004
milestone: M004
provides:
  - ["Capstone proof that Room runtime (S01) and routing engine (S02) compose correctly end-to-end", "Evidence that active-member guards, auto-start on delivery, bidirectional routing, and 3-agent participation all work together"]
requires:
  - slice: S01
    provides: Room lifecycle ARI surface (room/create, room/status, room/delete)
  - slice: S02
    provides: Routing engine (room/send, deliverPrompt, auto-start)
affects:
  []
key_files:
  - ["pkg/ari/server_test.go"]
key_decisions:
  - ["T01 skipped testing.Short() guard — no existing test in the file uses it, adding it would be inconsistent"]
patterns_established:
  - ["Multi-step integration test pattern: sequential ARI calls building up state, with roomStatus verification after each mutation, and full teardown with post-delete error check", "Teardown guard test pattern: attempt operations in wrong order, assert specific error codes/messages, then demonstrate correct ordering succeeds"]
observability_surfaces:
  - none
drill_down_paths:
  - [".gsd/milestones/M004/slices/S03/tasks/T01-SUMMARY.md", ".gsd/milestones/M004/slices/S03/tasks/T02-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-08T06:30:12.527Z
blocker_discovered: false
---

# S03: End-to-End Multi-Agent Integration Proof

**Proved the full Room lifecycle end-to-end with 2 integration tests: 3-agent bidirectional message exchange with state transitions, and teardown ordering guards — the capstone proof for M004.**

## What Happened

S03 delivers the capstone integration proof for M004 by exercising the complete Room lifecycle in two complementary tests that compose all building blocks from S01 (room lifecycle) and S02 (routing engine).

**T01 — TestARIMultiAgentRoundTrip (0.84s):** The primary 13-step end-to-end test proving the M004 demo claim. Creates a Room, bootstraps 3 agents (agent-a, agent-b, agent-c) all starting in "created" state, then executes bidirectional message exchange: A→B delivery with auto-start verification (agent-b transitions created→running), B→A reply proving bidirectional routing works, and A→C delivery proving all 3 agents participate. Each roomSend asserts Delivered==true and non-empty StopReason. State transitions are verified at each step via roomStatus. Clean teardown: all 3 sessions stopped, room deleted, post-delete room/status error confirmed.

**T02 — TestARIRoomTeardownGuards (1.10s):** Complementary error-path test proving teardown ordering constraints: (1) room/delete returns CodeInvalidParams with "active member" when sessions are still running, (2) session/remove returns CodeInvalidParams (ErrDeleteProtected) on running sessions, (3) both operations succeed after sessions are properly stopped. This ensures the active-member guard from S01 and the session lifecycle from S02 compose correctly under adversarial ordering.

Both tests pass deterministically. The full ARI short test suite (47 tests) passes with no regressions.

## Verification

1. `go test ./pkg/ari/ -count=1 -v -run TestARIMultiAgentRoundTrip -timeout 120s` — PASS (0.88s)
2. `go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s` — PASS (1.10s)
3. `go test ./pkg/ari/ -count=1 -short -timeout 120s` — PASS (7.9s, 47 tests, no regressions)

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01 deviated from plan by skipping the testing.Short() guard since no other tests in the file use it. T02 had no deviations.

## Known Limitations

Tests verify delivery success (Delivered==true) not content fidelity — mockagent returns EndTurn for any prompt without echoing content. Attribution is text-prefix format only, not structured metadata.

## Follow-ups

None.

## Files Created/Modified

- `pkg/ari/server_test.go` — Added TestARIMultiAgentRoundTrip (13-step 3-agent lifecycle) and TestARIRoomTeardownGuards (teardown ordering constraints)
