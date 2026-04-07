# S01: Fail-Closed Recovery Posture and Discovery Contract

**Goal:** Expose recovery posture as a first-class status surface, keep read-only inspection available under uncertainty, fail closed on operational methods, and normalize the shim discovery-root/socket contract that restart recovery depends on.
**Demo:** After this: After this: operators can inspect sessions after an uncertain restart and see an explicit healthy/degraded/blocked recovery posture, while prompt/stop/cleanup-style operations are rejected until certainty is re-established.

## Tasks
