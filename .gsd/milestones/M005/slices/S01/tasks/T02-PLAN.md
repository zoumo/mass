---
estimated_steps: 20
estimated_files: 2
skills_used: []
---

# T02: Add event ordering design to shim-rpc-spec and stability statement to agent-shim

Update the shim-layer docs. This task is independent of T01 — it touches different files.

shim-rpc-spec.md changes (408 lines, moderate update):
- Add new event ordering fields alongside existing SequenceMeta:
  - turnId (string) — correlates events to a specific prompt turn
  - streamSeq (int) — orders events within a turn, monotonic per turn
  - phase (string) — categorizes event timing: 'thinking', 'acting', 'tool_call'
- Document ordering rules:
  - seq continues as global log sequence (for recovery/dedup)
  - turnId assigned on turn_start, cleared on turn_end
  - streamSeq resets to 0 on each turn_start, increments within turn
  - runtime/stateChange excluded from turn ordering (uses seq only, no turnId)
- Document replay semantics: chat/replay orders by (turnId, streamSeq) within a turn, falls back to seq across turns
- Shim RPC methods stay UNCHANGED: session/prompt, session/cancel, session/subscribe, runtime/status, runtime/history, runtime/stop. NO renaming.
- Add a 'Turn-Aware Event Ordering' section (or similar heading) that the verification script can check for

agent-shim.md changes (179 lines, light update):
- Add explicit M005 stability statement near the top or in a dedicated section:
  'agent-shim retains existing RPC boundary in M005. The shim continues to serve session/* + runtime/* RPC, bundle/state separation, and single-session-per-shim design.'
- Add M005 scope note: 'The M005 refactoring primary is agentd. agent-shim's only enhancement is event ordering (turnId, streamSeq, phase in event envelopes).'
- Do NOT rename any session/* references in these files. The shim surface stays session-centric.

Key constraint per D060: agent-shim retains existing RPC surface. Only event ordering is enhanced.

## Inputs

- ``docs/design/runtime/shim-rpc-spec.md` — current shim RPC spec with seq-only ordering (408 lines)`
- ``docs/design/runtime/agent-shim.md` — current agent-shim design doc (179 lines)`

## Expected Output

- ``docs/design/runtime/shim-rpc-spec.md` — updated with turnId/streamSeq/phase ordering design, replay semantics section`
- ``docs/design/runtime/agent-shim.md` — updated with M005 stability statement and event ordering scope`

## Verification

grep -q 'turnId' docs/design/runtime/shim-rpc-spec.md && grep -q 'streamSeq' docs/design/runtime/shim-rpc-spec.md && grep -q 'phase' docs/design/runtime/shim-rpc-spec.md && grep -qi 'M005' docs/design/runtime/agent-shim.md && echo 'T02 verify pass'
