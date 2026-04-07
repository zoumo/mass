# S02: Live Shim Reconnect and Truthful Session Rebuild

**Goal:** Discover live shim sockets on daemon startup, reconnect to them, rebuild in-memory runtime state from live shim truth plus SQLite metadata, and surface mismatches as degraded/blocked instead of guessing.
**Demo:** After this: After this: restarting `agentd` while shims stay alive reconnects recovered sessions, restores truthful running state, and allows prompt/stop only when reconciliation reaches healthy.

## Tasks
