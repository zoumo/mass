# S04 — Research

**Date:** 2026-04-03

## Summary

Slice S04 implements the Session Manager with CRUD operations and state machine validation for session lifecycle transitions. The design doc (docs/design/agentd/agentd.md) defines five session states: `created`, `running`, `paused:warm`, `paused:cold`, and `stopped`, with specific transition rules. However, pkg/meta/models.go currently defines different states (`running`, `stopped`, `paused`, `error`) — this mismatch must be resolved by updating pkg/meta to align with the design specification.

The Session Manager wraps pkg/meta Store, adding:
1. State machine validation for all transitions
2. Business logic: prevent Delete on running sessions
3. Integration points for RuntimeClassRegistry and WorkspaceManager

## Recommendation

**Update pkg/meta/models.go SessionState to match design doc, then implement Session Manager in pkg/agentd/session.go.**

Aligning pkg/meta with the design doc is the cleanest approach — it ensures consistency across persistence, business logic, and API layers. The Store already persists any string value for state, so the change is primarily updating constants and tests. Session Manager then adds state machine validation on top of the corrected Store operations.

## Implementation Landscape

### Key Files

- `pkg/meta/models.go` — **needs update**: SessionState constants must change from `running/stopped/paused/error` to `created/running/paused:warm/paused:cold/stopped`
- `pkg/meta/session.go` — **needs update**: UpdateSession method signature uses SessionState; tests use old constants
- `pkg/meta/session_test.go` — **needs update**: Tests reference old SessionState values
- `pkg/meta/schema.sql` — **no change needed**: Sessions table stores state as TEXT, accepts any string
- `pkg/agentd/session.go` — **create**: SessionManager struct with CRUD + state machine
- `pkg/agentd/session_test.go` — **create**: Tests for state transitions, CRUD operations, Delete protection

### Build Order

1. **Update pkg/meta SessionState constants** — align with design doc first, re-run tests to verify persistence layer works
2. **Create pkg/agentd/session.go** — define SessionState, SessionManager, state machine, CRUD methods
3. **Create pkg/agentd/session_test.go** — table-driven tests for transitions, edge cases
4. **Integration verification** — test Session Manager + Store together

### Verification Approach

```bash
# 1. Run pkg/meta tests after SessionState update
go test ./pkg/meta/... -v

# 2. Run Session Manager tests
go test ./pkg/agentd/... -v

# 3. Build verification
go build ./pkg/agentd/...

# 4. Integration test: Session Manager CRUD round-trips through Store
go test ./pkg/agentd/... -run TestSessionManagerCRUD -v
```

## Constraints

- **S02 dependency**: pkg/meta Store must work with updated SessionState values. Tests in pkg/meta/session_test.go use old constants (`SessionStateRunning`, `SessionStateStopped`, `SessionStatePaused`) — must update.
- **State machine enforcement**: Invalid transitions must return errors. Example: cannot go from `Created` directly to `Paused:Warm` without going through `Running`.
- **Delete protection**: Requirement R004 specifies "prevent Delete on running sessions". Session Manager must check state before allowing deletion.
- **String persistence**: SQLite stores state as TEXT — accepts any string value. No schema change needed.

## State Machine Transitions

From docs/design/agentd/agentd.md:

| From | To | Trigger | Valid |
|------|-----|---------|-------|
| `created` | `running` | session/prompt (first turn) | ✅ |
| `created` | `stopped` | cancel (before starting) | ✅ |
| `running` | `paused:warm` | turn completed | ✅ |
| `running` | `stopped` | stop/error/timeout | ✅ |
| `paused:warm` | `running` | session/prompt (next turn) | ✅ |
| `paused:warm` | `paused:cold` | idle timeout | ✅ |
| `paused:warm` | `stopped` | stop/error/timeout | ✅ |
| `paused:cold` | `running` | session/load + prompt | ✅ |
| `paused:cold` | `stopped` | stop/error/timeout | ✅ |

**Invalid transitions** (must return error):
- `created` → `paused:*` (must go through `running` first)
- `stopped` → any (terminal state)
- `paused:cold` → `created` (no backwards transitions)
- Any transition not in the valid table

**Delete protection**:
- Can delete: `created`, `paused:cold`, `stopped` (no active process)
- Cannot delete: `running`, `paused:warm` (process is active)

## Session Manager Interface

From design doc:

```go
type SessionManager interface {
    Create(ctx context.Context, opts CreateSessionOpts) (*Session, error)
    Get(ctx context.Context, id string) (*Session, error)
    List(ctx context.Context, filter SessionFilter) ([]*Session, error)
    Update(ctx context.Context, id string, opts UpdateSessionOpts) error
    Delete(ctx context.Context, id string) error
}

type CreateSessionOpts struct {
    ID           string
    RuntimeClass string            // resolved via RuntimeClassRegistry
    Workspace    string            // workspace ID (prepared by WorkspaceManager)
    Room         string            // room name (optional)
    RoomAgent    string            // agent name in room (optional)
    Labels       map[string]string
    SystemPrompt string
}

type UpdateSessionOpts struct {
    State  SessionState  // must be valid transition from current state
    Labels map[string]string
}
```

## Common Pitfalls

- **State constant mismatch**: If pkg/meta SessionState and pkg/agentd SessionState diverge, type conversion errors will occur at compile time. Update pkg/meta first to avoid this.
- **Transition validation order**: Must check current state before allowing UpdateSession. Store.UpdateSession just writes whatever value is passed — Session Manager must validate.
- **Delete on wrong state**: Requirement says "prevent Delete on running sessions" but the design doc shows `running` and `paused:warm` both have active processes. Should prevent Delete on both.

## Open Risks

- **S02 test updates**: pkg/meta/session_test.go has 7 tests using old SessionState constants. May need substantial updates.
- **Integration timing**: S05 (Process Manager) depends on S04 for session state transitions. If state machine is incomplete, Process Manager integration will fail.

## Forward Intelligence

For downstream slices (S05 Process Manager, S06 ARI Service):

- **State transitions**: Process Manager will call Session Manager to transition states when agent process starts/stops/pauses. S04 must expose `Transition(ctx, sessionID, toState)` method for Process Manager to call.
- **Delete protection**: ARI session/remove method will call Session Manager Delete. Must return clear error when session is in `running` or `paused:warm` state.
- **Integration points**: Session Manager needs RuntimeClassRegistry (for Create validation) and Store reference. Pass these in constructor.