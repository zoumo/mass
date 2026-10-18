Language-independent behavior: [event handlers](../handlers.md).

# Construction-time event handlers

`Service::new(data_dir, providers, registrations, shutdown_timeout)` registers construction-time factories; daemon `load_service` composes that service and `mass_http::router` serves it. The normal `massd` uses an empty registration list. There is no dynamic loader, HTTP registration API, plugin configuration, SQLite implementation, or default business action.

`HandlerRegistration::new` selects all events, committed events, or named unified event types. Selection is declarative and runs before enqueueing; no extension callback runs under an Events, session lifecycle, admission or runtime-map lock. Factories and callbacks run in workers. A callback must yield during asynchronous waits; blocking or CPU-bound work must use an appropriate external executor. Construction-time code is trusted in-process code, not a sandbox.

`EventHandler::handle` receives `&ImmutableEvent` and `&HandlerContext`; `close` releases extension resources. The event's private `Arc<Value>` has only a shared read accessor. A handler can modify its own JSON copy, but cannot obtain mutable access to the original, replace its identity/seq/payload, intercept a commit or delete history. SSE and handler delivery share the same immutable committed object. Context contains the session identity, unique connection ID, transient/committed delivery kind and a connection-bound command executor.

`CommittedEventConsumer::consume` receives a `CommittedEvent` that only the core can construct after successful append and flush. Registration automatically excludes transients. This interface is a best-effort committed notification, not durable consumer progress: reliable synchronization must read the Journal using its own persistent checkpoint and recover missed wakes. No projection is implemented in this ticket.

## Ordering and overload

There is one worker and one bounded FIFO per `(sessionId, registration)` for the lifetime of the service. Each worker awaits a callback before taking the next event. Transients retain input order without gaining a durable seq. Different registrations and sessions have independent workers; registration order does not determine action completion order.

Archive and disconnect detach the runtime producer but retain non-runtime consumers. Resume reuses the existing worker and queue for the same sessionId. Each queued delivery carries the context of its originating connection; a replacement connection cannot start another worker that bypasses the old queue. The handler factory is not called again on resume. A disabled registration remains disabled for that session until service restart. History reads and strict local resume validation do not enqueue old history.

The configured nonzero capacity bounds waiting events, in addition to at most one executing event. Enqueue uses `try_send`, never awaits capacity, and creates no per-event task. On overflow the affected lane is disabled with a diagnostic, drains its already accepted queue, then calls `close`. New events continue through Journal, SSE and healthy lanes. A callback error or panic disables that lane, discards its unprocessed queue, and attempts `close`; factory/close panics are caught and diagnosed. Handler diagnostics go directly to tracing, so handler failures cannot recursively manufacture session events.

A pending-creation guard rolls back consumers if native thread open, metadata save or the creating request fails before runtime installation. It removes the provisional session lanes and signals workers to cancel pending callbacks, attempt `close` within the configured deadline, then release their resources. This cleanup does not depend on a later request or service stop. Successful session consumers are accepted only after metadata and runtime installation.

Workers retained for an archived session are intentional service-lifetime consumer state. Finished task handles are reaped when opening a connection. Shutdown removes all lane senders, drains accepted deliveries and calls `close`, using one configurable total consumer deadline; workers exceeding it are aborted and awaited. Timed-out callbacks cannot be promised a completed `close`. The handle collection is taken and emptied during shutdown; dropping the service aborts retained workers. Ordinary action queues have no crash-recovery or exactly-once promise.

## Commands

`context.commands.prompt`, `cancel` and `set_config_option` reuse HTTP command validation, cancellation admission and the attachment-bound native command Interface. These futures execute outside event locks. They remain attached to the original runtime channels and admission gate. Closing a runtime rejects old work; no command looks up a replacement connection or implicitly resumes an agent. Native dispatch rechecks closed/cancellation generation immediately before publication. An extension that triggers further events consumes them later through its normal bounded queue and must design its own business loop prevention.

## Real-agent acceptance

Run `NATIVE_TEST=migrated_fault_handlers make test-native` with the reviewed
Codex executable and digest. Append/Stop cases use `migrated_fault`; archive and
old-command cases use `migrated_archive`. Rust test constructors inject consumers
and real delivery gates, never fabricated native facts. See [current test
coverage](../testing.md) for execution groups and historical stage
coverage for its recorded baseline.

## Native Ticket 06

The current constructor is `Service::new(data_dir, providers, registrations,
shutdown_timeout)` composed by the daemon `load_service`. Native Codex facts use
a bounded attachment core-delivery worker. `native_recorded` still describes a
native echo/start observation and can precede the Journal commit. Only the core's
append+flush creates a committed immutable envelope. A real append failure seals
the writer and cannot emit the failed durable event to either Handler or SSE.
Repairing the path does not reenable that writer. Explicit archive/release/resume
uses a new writer after strict validation; old Handler contexts keep their original
attachment and fail on prompt/cancel/config rather than rerouting by name.

The same-session registration queue continues across resume, with each event's
originating context. Healthy lanes and other attachments do not wait for a slow,
failing, panicking or overflowed Handler. Factory/callback/close are trusted
cooperative code; blocking native calls must use an appropriate external executor.
Service shutdown drains accepted core facts before runtime cleanup and then runs
the configured total Handler drain/close deadline. Live SSE subscriptions receive
queued publications and terminate after that shutdown. No finite deadline is
promised for infinitely blocked file I/O or non-cooperative in-process code.

Real native validation uses the Rust `migrated_fault_handlers`,
`migrated_fault_append`, and `migrated_fault_stop` cases through `make test-native`.
These inject consumers or delivery delay without fabricating native messages.
See Ticket 06 evidence and the
[exact capacity/upstream boundary](../design/native-runtime-connectors.md#native-ticket-06-bounded-core-delivery-and-shared-transport-pressure).
