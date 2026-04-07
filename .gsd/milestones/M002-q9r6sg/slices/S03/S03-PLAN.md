# S03: Atomic Event Resume and Damaged-Tail Tolerance

**Goal:** Make event-log reopen and replay tolerant of damaged-tail data, and provide one atomic catch-up-to-live boundary so reconnect cannot miss events between history replay and subscription.
**Demo:** After this: After this: a recovered session with a truncated `events.jsonl` tail still reports truthful status, replays only the durable event prefix, and refuses live operation if resume certainty cannot be established.

## Tasks
