# Native SDK resume and asynchronous history readiness

Inspected 2026-10-21. This is implementation evidence; the authoritative MASS
contract is [harness load](../../harness/modules/service/load.md).

The sibling `agent-sdk-go` checkout implements
`codex.Client.Load(ctx, HistoryOptions) (*LoadedThread, error)` and
`claude.Load(ctx, LoadOptions) (*LoadedSession, error)` at local main commit `0187824` (not pushed).
Load resumes the same native session, asynchronously replays retained main history,
and leaves the resumed session available for input after successful Done/Err.
Ordinary resume does not gain replay. Both handles provide Records, Done, Err,
and Close. MASS must own a replay context beyond HTTP admission.

Codex yields raw full-turn JSON and decoded native turns/items in ascending
pagination order. Continue through the original Client.StartTurn after completion;
subscribe to live notifications before Load. Close unsubscribes the loaded thread
without closing the shared connection. Unknown dispatched resume ownership keeps
input blocked and requires closing the original Client to settle the attachment.
Claude yields selected-main-chain entries with raw message JSON, UUID, parent and
timestamp. LoadedSession.Client becomes available after successful replay; Close
closes its owned native child. Canceling a successful load context leaves the
resumed client usable. Claude verifies the original transcript prefix unchanged,
allowing only appended known native metadata with matching identity.

Neither SDK produces MASS events. Native errors, unsupported layouts and explicit
resource limits must remain visible. MASS must adapt normal conversion → Service
validation → EventStore → Broadcast, with input blocked until local completion and
current native-session evidence established. Do not inject fake live notifications
or revive old permission handles. Native initialization does not prove a new
observed Claude session ID; requested and observed identity remain distinct.

This task does not upgrade MASS's SDK dependency or implement a Driver load path.
Existing filtering, item conversion and descendant checks still require explicit
integration. SDK acceptance and TypeSpec compilation do not prove that pipeline.

Full SDK offline checks, race, vet and both review axes passed. Public-interface
tests cover resume, replay, continuation and failures.
An isolated installed Claude 2.1.258 probe passed native resume initialization plus
SDK replay of two owned synthetic records, without prompt/model/tool execution;
it also established native atis-latch/mode metadata appends. Codex capability
inspection used installed 0.163.0; generated bindings retain the 0.162.1 source pin.
A subsequent explicit local real-agent run at SDK 0187824 passed both providers:
two real model turns, fresh-client Load, pending-input gating, ordered replay and
third-turn context recall on the same native session after load-context
cancellation. Codex PageSize=1 replayed both real turns; Claude replayed all five
native main-chain records byte-for-byte, including a thinking record. Owned
clients were closed/reaped. Native CLI login was inherited without extracting or
copying credentials; only newly created validation histories were read. Codex
required the same canonical cwd from creation through Load (macOS /var aliases
are not normalized by its exact native-path check).

Real complex tool/compaction/subagent histories and MASS runtime integration
remain unverified. See the sibling SDK's docs/specs/async-history-validation.md
for the precise source, environment, initial probe failures and accepted scope.
