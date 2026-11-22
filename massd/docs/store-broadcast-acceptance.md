# Independent stores and realtime acceptance

> **Historical transport note.** This page records the storage/Broadcast
> acceptance at harness v0.2.10, including the HTTP/SSE transport used at that
> time. Current product suites use the CLI and WebSocket JSON-RPC; the
> transport-specific commands below are historical evidence, not current usage.

This describes the implemented storage/Broadcast scope approved at harness
v0.2.10. The newer native-history load/replay contract is outside this delivery.
Earlier ticket-specific documents describe their own implementation stage.

`massd` selects one MetaStore authority at startup: SQLite by default, or file
when `metaStore.backend: file` is explicit. The independent file EventStore
commits event bytes and its SQLite query projection before Service publishes a
persistent SessionEvent. With no subscribers, history still commits. A SQL
failure after file append seals affected history access; Service retains the
original UUID for explicit settlement after EventStore reconciliation. Broadcast
does not perform this recovery or retry partially delivered events.

Service resolves ID and namespace/name to one Broadcast for the persistent
Session identity. The Broadcast runs unnamed Handlers in insertion order;
required failures stop only the current event, ordinary failures continue.
Subscriptions each use one bounded channel/queue Handler. Cancellation and
overflow detach only that subscription, fix the cause before observable closure
and preserve admitted buffered events. Go exposes Events, Err and Close; Events
returns the same receive-only channel.

State projection is an ordinary asynchronous Handler. Writes may lag or fail;
notifications continue without any promise of eventual convergence. Identity,
version and terminal-state checks protect resources. Mandatory lifecycle writes
remain synchronous Service responsibilities. Stop/close preserve healthy
Broadcasts and subscriptions; concrete upstream delivery fences and the Service
lane barrier settle final history/publish calls. Application shutdown closes
owned objects and reports cleanup failure; stream termination is not proof that
every asynchronous observation was consumed or an Agent was released.

## Reproducible offline acceptance

Run from `mass-go/` (all fixtures, protocol peers and data are synthetic/owned):

```sh
rtk proxy go test -race ./pkg/events ./pkg/metastore/... ./pkg/eventstore/...
rtk proxy go test -race ./pkg/service -run 'TestSQLReplicaFailureNeverPublishesUncoordinatedFact|TestStateWrites|TestFailedState|TestActiveState|TestRuntimeStopSuite|TestSessionWorkSuite'
rtk proxy go test -race ./tests/integration -run 'Test(File)?EntrypointSuite'
```

Both entrypoint suites start the actual daemon and invoke the CLI, HTTP and SSE
(the historical transport).
They cover CRUD, identity/name addressing, live facts already queryable at receipt,
multi-Runtime isolation and lifecycle. `TestMetadataRestartAndEventRebuild` adds
unobserved execution, ordinary restart followed by event-SQL loss/rebuild, exact
resource/history/cursor preservation and offline ID/name subscriptions. A real
sentinel executable records attempted Agent activation; query/rebuild/observation
must leave it uncalled. Concurrent SSE reads provide bounded absence evidence during offline queries;
the explicit shutdown terminal cause is checked separately. Owned daemon,
mock processes, listeners and data directories are checked for cleanup.

`TestSQLReplicaFailureNeverPublishesUncoordinatedFact` runs with both metadata
authorities and real event files/SQLite. An actual SQL trigger aborts projection
after the file append. HTTP cannot serve incomplete history, SSE receives no
premature fact, metadata stays usable and explicit recovery publishes the original
UUID without adding file bytes. Rebuilding again with ordinary Handlers and a
subscription already registered invokes neither, even with no Runtime factories.

The public MetaStore/EventStore suites additionally cover conditional versions,
generation, uniqueness, snapshots, independent lifecycle, original-ID retries,
cursor/filter/progress stability, corruption and process-interruption recovery.
Broadcast/Service suites cover mutation isolation, required/ordinary failures,
send/cancel/overflow races, stable buffered termination, asynchronous state faults,
close/resume reuse and final-delivery/cleanup fences.
`TestSameNameNewIdentityKeepsBroadcastsIsolated` replaces an offline metadata
fixture under each authority and checks that old/new identity Broadcasts and
subscriptions remain separate. This fixture does not define a product Session
deletion or event-retention policy.

`GOFLAGS=-p=2 rtk proxy make check-go` at the repository root is the full
lint/race/build/format check. Current external v0.2.11 examples include replay
fields and an eighteenth event branch, which the approved v0.2.10 implementation
does not accept. Report those failures explicitly. A frozen temporary copy with
identical Go source and only `event-examples.json` replaced by its v0.2.10 bytes
can verify the approved scope; that result is not a current-workspace full pass.
Exact commands, fixture origin/hashes, failed attempts and completed results are
recorded in `.scratch/store-split/evidence/06/` at the repository root.

No real model/native Agent or arbitrary hardware power-loss guarantee is
established. Session deletion/event retention and Driver evidence before physical
tail repair remain undecided. There is no implicit migration, cascading deletion,
new load/replay implementation, push, merge or deployment in this acceptance.
