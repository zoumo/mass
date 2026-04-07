# S04: Reconciled Workspace Ref Truth and Safe Cleanup

**Goal:** Make workspace cleanup and destructive session operations depend on persisted/reconciled workspace reference truth instead of volatile in-memory counters.
**Demo:** After this: After this: after restart, workspaces referenced by recovered or uncertain sessions cannot be cleaned up; once sessions stop and refs reconcile to zero, cleanup succeeds normally.

## Tasks
